package core

// Observability for block production: counters, the greppable LIVENESS log
// lines, and the snapshot the watchdog and any status endpoint read.
//
// Everything here is purely observational. Nothing in this file is read by
// ValidateBlock, commitBlock or any transaction apply path, and none of it
// feeds a consensus decision; it exists so that a stalled or degraded proposer
// says so loudly instead of retrying in silence (the chain's previous halts
// were diagnosed from stdout text that no metric or alert covered).
//
// Log conventions, for operators who grep:
//
//	LIVENESS:       something that affects whether blocks are produced
//	NONDETERMINISM: a transaction type that cannot be proposed deterministically
//
// Attacker-influenced text (error strings that echo payload bytes) is always
// truncated and stripped of control characters before it is logged.

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"nhbchain/core/types"
	"nhbchain/observability"
)

// buildTelemetry is the mutable state behind the liveness reporting.
type buildTelemetry struct {
	consecutive             atomic.Int64
	fallbacks               atomic.Uint64
	panicsRecovered         atomic.Uint64
	localValidationFailures atomic.Uint64
	ownUncommitted          atomic.Uint64
	lastErr                 atomic.Pointer[string]

	mu            sync.Mutex
	fallbackLogs  uint64
	impossibleLog uint64
	nondetLogAt   map[types.TxType]time.Time

	// Watchdog state: the last committed height observed and when it last
	// moved. Process start (the first observation) counts as a commit.
	wdHeight     uint64
	wdAdvancedAt time.Time
	wdStalled    bool
	wdLastAlert  time.Time
}

// LivenessSnapshot is a point-in-time view of block-production health.
type LivenessSnapshot struct {
	// Height is the committed chain height.
	Height uint64
	// SinceLastCommit is how long the committed height has not advanced (the
	// process start counts as an advance).
	SinceLastCommit time.Duration
	// Stalled is true while SinceLastCommit exceeds the configured threshold.
	Stalled bool
	// ConsecutiveBuildFailures counts back-to-back proposal builds that could
	// not include the offered transactions.
	ConsecutiveBuildFailures int
	LastBuildError           string
	EmptyBlockFallbacks      uint64
	TxPanicsRecovered        uint64
	LocalValidationFailures  uint64
	// MempoolSize and InFlight are the resident and leased transaction counts;
	// StrikeRecords is how many residents have a recorded failure history.
	MempoolSize   int
	InFlight      int
	StrikeRecords int
	// BFT* describe the consensus engine when one is attached.
	BFTHeight      uint64
	BFTRound       int
	BFTLockedRound int
}

// LivenessSnapshot returns the current block-production health.
func (n *Node) LivenessSnapshot() LivenessSnapshot {
	if n == nil {
		return LivenessSnapshot{}
	}
	cfg := n.buildConfigSnapshot()
	snap := LivenessSnapshot{}
	if n.chain != nil {
		snap.Height = n.chain.GetHeight()
	}
	snap.SinceLastCommit = n.observeCommitProgress(snap.Height, n.localNow())
	snap.Stalled = snap.SinceLastCommit >= cfg.StallAfter
	snap.ConsecutiveBuildFailures = int(n.build.consecutive.Load())
	if last := n.build.lastErr.Load(); last != nil {
		snap.LastBuildError = *last
	}
	snap.EmptyBlockFallbacks = n.build.fallbacks.Load()
	snap.TxPanicsRecovered = n.build.panicsRecovered.Load()
	snap.LocalValidationFailures = n.build.localValidationFailures.Load()
	snap.MempoolSize, snap.InFlight = n.mempoolCounts()
	snap.StrikeRecords = n.poison.size()
	if n.bftEngine != nil {
		snap.BFTHeight, snap.BFTRound, snap.BFTLockedRound = n.bftEngine.Status()
	}
	return snap
}

// observeCommitProgress records the committed height seen at now and returns
// how long it has been unchanged.
func (n *Node) observeCommitProgress(height uint64, now time.Time) time.Duration {
	n.build.mu.Lock()
	defer n.build.mu.Unlock()
	if n.build.wdAdvancedAt.IsZero() {
		n.build.wdAdvancedAt = now
		n.build.wdHeight = height
	}
	if height != n.build.wdHeight {
		n.build.wdHeight = height
		n.build.wdAdvancedAt = now
	}
	since := now.Sub(n.build.wdAdvancedAt)
	if since < 0 {
		since = 0
	}
	return since
}

// livenessFields are the structured fields every LIVENESS line carries, so a
// single grep-and-read separates "peer down" from "build failing" from "own
// proposals rejected" from "locked".
func (n *Node) livenessFields(event string) []any {
	snap := n.LivenessSnapshot()
	return []any{
		slog.String("event", event),
		slog.Uint64("height", snap.Height),
		slog.Float64("since_commit_s", snap.SinceLastCommit.Seconds()),
		slog.Int("consecutive_build_failures", snap.ConsecutiveBuildFailures),
		slog.String("last_build_error", snap.LastBuildError),
		slog.Int("mempool", snap.MempoolSize),
		slog.Int("inflight", snap.InFlight),
		slog.Int("quarantined", snap.StrikeRecords),
		slog.Uint64("bft_height", snap.BFTHeight),
		slog.Int("bft_round", snap.BFTRound),
		slog.Int("bft_locked_round", snap.BFTLockedRound),
	}
}

// shouldLogRateLimited reports whether the count-th occurrence of a repeating
// event should be logged: the first three, then every tenth.
func shouldLogRateLimited(count uint64) bool {
	return count <= 3 || count%10 == 0
}

// noteWholeBuildFailure records that the full build could not produce a block
// and may start isolation.
func (n *Node) noteWholeBuildFailure(ctx *buildCtx, err error) {
	consecutive := n.build.consecutive.Add(1)
	text := sanitizeLogText(err.Error())
	n.build.lastErr.Store(&text)
	metrics := observability.Consensus()
	metrics.RecordBuildFailure(buildFailureReason(err))
	metrics.SetConsecutiveBuildFailures(int(consecutive))
	if ctx.cfg.IsolationK > 0 && int(consecutive) >= ctx.cfg.IsolationK {
		n.maybeStartIsolation(ctx)
	}
}

// noteEmptyFallback records that the proposal fell back to an empty block.
func (n *Node) noteEmptyFallback(ctx *buildCtx, cause error) {
	n.build.fallbacks.Add(1)
	observability.Consensus().RecordEmptyBlockFallback()
	n.build.mu.Lock()
	n.build.fallbackLogs++
	emit := shouldLogRateLimited(n.build.fallbackLogs)
	n.build.mu.Unlock()
	if emit {
		slog.Error("LIVENESS: block build failed; proposing empty block",
			n.livenessFields("empty_block_fallback")...)
	}
}

// noteBuildImpossible records that even the empty block could not be built:
// the fault is in node state, lifecycle or evidence processing, not in any
// transaction, and this node cannot propose until it is fixed.
func (n *Node) noteBuildImpossible(ctx *buildCtx, fullErr, emptyErr error) {
	text := sanitizeLogText(emptyErr.Error())
	n.build.lastErr.Store(&text)
	observability.Consensus().RecordBuildFailure(buildFailureReason(emptyErr))
	n.build.mu.Lock()
	n.build.impossibleLog++
	emit := shouldLogRateLimited(n.build.impossibleLog)
	n.build.mu.Unlock()
	if emit {
		fields := n.livenessFields("build_impossible")
		fields = append(fields,
			slog.String("full_build_error", sanitizeLogText(fullErr.Error())),
			slog.String("empty_build_error", text))
		slog.Error("LIVENESS: block build failed and empty-block fallback failed; this node cannot propose", fields...)
	}
}

// noteBuildSucceeded ends a failure streak.
func (n *Node) noteBuildSucceeded(ctx *buildCtx) {
	if previous := n.build.consecutive.Swap(0); previous > 0 {
		observability.Consensus().SetConsecutiveBuildFailures(0)
		slog.Info("block build recovered", slog.Int64("after_consecutive_failures", previous))
	}
}

// noteNondeterminism logs, at most once a minute per transaction type, that a
// type cannot be proposed because it reads the wall clock while applying.
func (n *Node) noteNondeterminism(t types.TxType, now time.Time) {
	n.build.mu.Lock()
	if n.build.nondetLogAt == nil {
		n.build.nondetLogAt = make(map[types.TxType]time.Time)
	}
	last, seen := n.build.nondetLogAt[t]
	emit := !seen || now.Sub(last) >= time.Minute
	if emit {
		n.build.nondetLogAt[t] = now
	}
	n.build.mu.Unlock()
	if emit {
		slog.Error(fmt.Sprintf("NONDETERMINISM: transaction type 0x%02X (%s) reads the wall clock during apply and is not proposable until the deterministic-time fix ships",
			byte(t), types.TxTypeName(t)))
	}
}

// NoteProposalOutcome is called by the BFT engine (through an optional
// interface, so its many test doubles need not implement it) when a proposal
// this validator authored did not turn into a committed block. kind is one of:
//
//	"build_failed"            CreateBlock returned an error
//	"local_validation_failed" the block it built failed this node's own ValidateBlock
//	"not_committed"           the round ended without the proposal committing
//
// A rejection of the node's own block that names a transaction (a
// *blockApplyError) is charged to that transaction: it applied while building
// but not while validating, which is exactly what a transaction whose outcome
// depends on something other than state looks like, and no amount of
// proposer-side re-execution can find it any other way.
//
// Locking: the "not_committed" kind is reported by the BFT engine while it
// holds its own lock, so that branch must stay a plain counter bump and must
// never call back into the engine (LivenessSnapshot reads the engine's status
// and would deadlock). The other kinds are reported with no lock held.
func (n *Node) NoteProposalOutcome(height uint64, round int, kind string, txCount int, err error) {
	if n == nil {
		return
	}
	switch kind {
	case "local_validation_failed":
		n.build.localValidationFailures.Add(1)
		observability.Consensus().RecordLocalValidationFailure()
		fields := n.livenessFields("local_validation_failed")
		fields = append(fields, slog.Int("round", round), slog.Int("txs", txCount))
		if err != nil {
			fields = append(fields, slog.String("error", sanitizeLogText(err.Error())))
		}
		slog.Error("LIVENESS: local validation of own proposal failed", fields...)
		n.chargeRejectedProposalTx(height, err)
	case "not_committed":
		n.build.ownUncommitted.Add(1)
	}
}

// chargeRejectedProposalTx strikes the transaction a validation failure of the
// node's own last block points at, and evicts it once it has been the cause of
// a rejected proposal Strikes times.
func (n *Node) chargeRejectedProposalTx(height uint64, err error) {
	var applyErr *blockApplyError
	if err == nil || !errors.As(err, &applyErr) {
		return
	}
	rec := n.lastBuilt.Load()
	if rec == nil || rec.height != height || applyErr.Index < 0 || applyErr.Index >= len(rec.txs) {
		return
	}
	tx := rec.txs[applyErr.Index]
	key, keyErr := transactionKey(tx)
	if keyErr != nil {
		return
	}
	cfg := n.buildConfigSnapshot()
	strikes := n.poison.noteStrike(key, height, n.localNow(), "rejected by local validation: "+applyErr.Err.Error())
	if strikes < cfg.Strikes {
		return
	}
	n.dropTransactionsFromMempool([]*types.Transaction{tx})
	observability.Mempool().RecordEviction("strikes")
	slog.Warn("transaction evicted from the local mempool",
		slog.String("txType", types.TxTypeName(tx.Type)),
		slog.String("key", truncateText(key, 24)),
		slog.String("reason", "repeatedly rejected by local validation"))
}
