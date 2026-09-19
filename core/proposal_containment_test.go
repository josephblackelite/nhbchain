package core

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// ---- log capture ------------------------------------------------------------

// slogCapture records every log record so tests can assert on the greppable
// LIVENESS / NONDETERMINISM lines and their structured fields.
type slogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (c *slogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (c *slogCapture) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	c.records = append(c.records, r.Clone())
	c.mu.Unlock()
	return nil
}
func (c *slogCapture) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *slogCapture) WithGroup(string) slog.Handler      { return c }

// find returns the records whose message starts with prefix.
func (c *slogCapture) find(prefix string) []slog.Record {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []slog.Record
	for _, r := range c.records {
		if strings.HasPrefix(r.Message, prefix) {
			out = append(out, r)
		}
	}
	return out
}

// attr reads one structured attribute of a record.
func attr(r slog.Record, key string) (slog.Value, bool) {
	var found slog.Value
	var ok bool
	r.Attrs(func(a slog.Attr) bool {
		if a.Key == key {
			found, ok = a.Value, true
			return false
		}
		return true
	})
	return found, ok
}

// captureLogs routes the default logger into a capture for the test's duration.
func captureLogs(t *testing.T) *slogCapture {
	t.Helper()
	capture := &slogCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return capture
}

// ---- F4: the clamp no longer leaks in-flight marks --------------------------

// Before: GetMempool marked EVERY transaction it returned as in flight, but a
// proposal can hold at most Blocks.MaxTxs, and the release on success only
// covered what was skipped -- so on a burst above the cap the overflow was hidden
// from every later GetMempool call, permanently.
func TestCreateBlockDoesNotHideTransactionsBeyondTheProposalCap(t *testing.T) {
	node := newTestNode(t)
	cfg := node.globalConfigSnapshot()
	cfg.Blocks.MaxTxs = 2
	if err := node.SetGlobalConfig(cfg); err != nil {
		t.Fatalf("set config: %v", err)
	}
	txs := livenessFundedTransfers(t, node, 5)

	// Round 1: five on offer, two fit.
	offered := node.GetMempool()
	block, err := node.CreateBlock(offered)
	if err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	if len(block.Transactions) != 2 {
		t.Fatalf("expected the two-transaction cap, got %d", len(block.Transactions))
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// The three that did not fit must be offered again, and all get through.
	seen := map[*types.Transaction]bool{}
	for _, tx := range block.Transactions {
		seen[tx] = true
	}
	for round := 2; round <= 4 && len(seen) < len(txs); round++ {
		offered := node.GetMempool()
		if len(offered) == 0 {
			t.Fatalf("round %d: transactions beyond the cap are permanently hidden (%d of %d included so far)", round, len(seen), len(txs))
		}
		b, err := node.CreateBlock(offered)
		if err != nil {
			t.Fatalf("round %d: CreateBlock: %v", round, err)
		}
		for _, tx := range b.Transactions {
			seen[tx] = true
		}
		if err := node.CommitBlock(b); err != nil {
			t.Fatalf("round %d: commit: %v", round, err)
		}
	}
	if len(seen) != len(txs) {
		t.Fatalf("only %d of %d transactions were ever included", len(seen), len(txs))
	}
}

// A proposal that is built and then never finishes (the round failed before the
// bft engine could requeue it, a consumer that only polls GetMempool) must not
// hide its transactions forever: the lease lapses.
func TestAbandonedProposalTransactionsReturnAfterTheLease(t *testing.T) {
	node := newTestNode(t)
	clock := time.Unix(1_800_000_000, 0)
	node.setLocalClock(func() time.Time { return clock })
	livenessFundedTransfers(t, node, 3)

	if got := len(node.GetMempool()); got != 3 {
		t.Fatalf("first offer = %d", got)
	}
	if got := len(node.GetMempool()); got != 0 {
		t.Fatalf("the leased transactions must not be offered again immediately, got %d", got)
	}
	before := livenessMetricValue(t, "nhb_mempool_inflight_leases_expired_total", nil)
	clock = clock.Add(defaultInflightLease + time.Second)
	if got := len(node.GetMempool()); got != 3 {
		t.Fatalf("after the lease lapsed all three must be offered again, got %d", got)
	}
	if got := livenessMetricValue(t, "nhb_mempool_inflight_leases_expired_total", nil); got-before != 3 {
		t.Fatalf("expected three lapsed leases to be counted, got %v", got-before)
	}
}

// ---- infrastructure errors --------------------------------------------------

// An error that says something about this node's database (not the transaction)
// aborts the wave: excluding transactions cannot fix a broken database, so no
// transaction may be struck for it. The build falls back to the empty block and
// the failure is counted as infrastructure.
func TestInfrastructureErrorAbortsTheWaveWithoutStrikingTheTransaction(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 4)
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		if tx == txs[2] {
			return context.DeadlineExceeded
		}
		return nil
	})
	before := livenessMetricValue(t, "nhb_consensus_build_failures_total", map[string]string{"reason": "infra"})

	block, err := node.CreateBlock(node.GetMempool())
	if err != nil {
		t.Fatalf("CreateBlock must fall back to an empty block: %v", err)
	}
	if len(block.Transactions) != 0 {
		t.Fatalf("expected the empty fallback, got %d transactions", len(block.Transactions))
	}
	for i, tx := range txs {
		if _, ok := livenessStrikes(t, node, tx); ok {
			t.Fatalf("transaction %d was struck for an infrastructure error that is not its fault", i)
		}
		if !livenessResident(t, node, tx) {
			t.Fatalf("transaction %d must stay resident", i)
		}
	}
	if got := livenessMetricValue(t, "nhb_consensus_build_failures_total", map[string]string{"reason": "infra"}); got-before != 1 {
		t.Fatalf("expected one infra build failure to be counted, got %v", got-before)
	}
	livenessWaitIsolation(t, node)
}

// ---- the greppable LIVENESS lines -------------------------------------------

func TestLivenessAndNondeterminismLogLines(t *testing.T) {
	logs := captureLogs(t)
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 2)

	// Empty-block fallback.
	node.setProposalLifecycleHook(func(sp *StateProcessor, blockTxs []*types.Transaction) error {
		if len(blockTxs) > 0 {
			return errors.New("injected lifecycle failure")
		}
		return nil
	})
	if _, err := node.CreateBlock(node.GetMempool()); err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	fallback := logs.find("LIVENESS: block build failed; proposing empty block")
	if len(fallback) != 1 {
		t.Fatalf("expected one empty-fallback line, got %d", len(fallback))
	}
	for _, key := range []string{"event", "height", "since_commit_s", "consecutive_build_failures", "last_build_error", "mempool", "inflight", "quarantined", "bft_round"} {
		if _, ok := attr(fallback[0], key); !ok {
			t.Fatalf("the LIVENESS line lacks the structured field %q", key)
		}
	}
	if v, _ := attr(fallback[0], "last_build_error"); !strings.Contains(v.String(), "injected lifecycle failure") {
		t.Fatalf("the line must carry the last build error, got %q", v.String())
	}
	if len(logs.find("proposal containment")) == 0 {
		t.Fatalf("expected the aggregated containment line")
	}

	// Nothing can be built at all.
	node.setProposalLifecycleHook(func(sp *StateProcessor, blockTxs []*types.Transaction) error {
		return errors.New("injected lifecycle failure, always")
	})
	if _, err := node.CreateBlock(node.GetMempool()); err == nil {
		t.Fatalf("expected an error")
	}
	if len(logs.find("LIVENESS: block build failed and empty-block fallback failed; this node cannot propose")) == 0 {
		t.Fatalf("expected the cannot-propose line")
	}
	livenessWaitIsolation(t, node)
	node.setProposalLifecycleHook(nil)

	// A panic that escapes every inner recover.
	node.setProposalLifecycleHook(func(sp *StateProcessor, blockTxs []*types.Transaction) error {
		panic("injected")
	})
	if _, err := node.CreateBlock(node.GetMempool()); err == nil {
		t.Fatalf("expected an error")
	}
	if len(logs.find("LIVENESS: panic in block build")) == 0 {
		t.Fatalf("expected the build-panic line")
	}
	livenessWaitIsolation(t, node)
	node.setProposalLifecycleHook(nil)

	// Wall-clock reads are announced per type.
	payer, payee := livenessKey(t), livenessKey(t)
	livenessFund(t, node, payer.PubKey().Address().Bytes(), 1_000_000_000, 0)
	create := livenessSign(t, payer, types.TxTypeCreateEscrow, 0,
		escrowCreatePayload(t, payee.PubKey().Address().Bytes(), 100, time.Now().Add(time.Hour).Unix(), 1), nil, 0)
	livenessInject(node, create)
	if _, err := node.CreateBlock([]*types.Transaction{create}); err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	found := logs.find("NONDETERMINISM: transaction type 0x03 (CreateEscrow) reads the wall clock")
	if len(found) != 1 {
		t.Fatalf("expected one NONDETERMINISM line for CreateEscrow, got %d", len(found))
	}
	// ... and rate limited: a second exclusion inside a minute is silent.
	if _, err := node.CreateBlock([]*types.Transaction{create}); err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	if got := len(logs.find("NONDETERMINISM: transaction type 0x03")); got != 1 {
		t.Fatalf("the NONDETERMINISM line must be rate limited per type, got %d", got)
	}

	// Per-transaction detail is capped per build; the rest is only counted.
	_ = txs
	many := make([]*types.Transaction, 0, 12)
	for i := 0; i < 12; i++ {
		many = append(many, livenessSign(t, livenessKey(t), types.TxTypeTransfer, 0, nil, make([]byte, 20), 1_000_000))
	}
	logs.mu.Lock()
	logs.records = nil
	logs.mu.Unlock()
	if _, err := node.CreateBlock(many); err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	perTx := 0
	for _, r := range logs.find("transaction excluded from block proposal") {
		if r.Level >= slog.LevelWarn {
			perTx++
		}
	}
	if perTx != maxPerTxLogLines {
		t.Fatalf("expected at most %d per-transaction warn lines per build, got %d", maxPerTxLogLines, perTx)
	}
}

// Attacker-influenced error text must not be able to inject log lines or flood.
func TestSanitizeLogTextStripsControlCharactersAndBoundsLength(t *testing.T) {
	got := sanitizeLogText("bad\nLIVENESS: forged\r\x00" + strings.Repeat("x", 500) + "\xff")
	if strings.ContainsAny(got, "\n\r\x00") {
		t.Fatalf("control characters survived: %q", got)
	}
	if len(got) > maxLoggedErrorLen {
		t.Fatalf("length %d exceeds %d", len(got), maxLoggedErrorLen)
	}
}

// ---- the watchdog -------------------------------------------------------------

func TestWatchdogReportsAStallItsRepeatsAndTheRecovery(t *testing.T) {
	logs := captureLogs(t)
	node := newTestNode(t)
	node.setBuildConfig(func(cfg *buildConfig) { cfg.StallAfter = 60 * time.Second })
	start := time.Unix(1_800_000_000, 0)

	node.livenessCheck(start) // the first observation: process start counts as a commit
	node.livenessCheck(start.Add(30 * time.Second))
	if len(logs.find("LIVENESS: no block committed")) != 0 {
		t.Fatalf("no alert may fire before the threshold")
	}
	if v := livenessMetricValue(t, "nhb_consensus_liveness_stalled", nil); v != 0 {
		t.Fatalf("stalled gauge = %v before the threshold", v)
	}

	node.livenessCheck(start.Add(61 * time.Second))
	stall := logs.find("LIVENESS: no block committed for 61s")
	if len(stall) != 1 {
		t.Fatalf("expected the stall line with the elapsed time in it, got %d", len(stall))
	}
	if v, _ := attr(stall[0], "event"); v.String() != "no_commit" {
		t.Fatalf("event = %q", v.String())
	}
	if v := livenessMetricValue(t, "nhb_consensus_liveness_stalled", nil); v != 1 {
		t.Fatalf("stalled gauge = %v during a stall", v)
	}
	if v := livenessMetricValue(t, "nhb_consensus_seconds_since_last_commit", nil); v != 61 {
		t.Fatalf("seconds since last commit = %v, want 61", v)
	}

	node.livenessCheck(start.Add(90 * time.Second)) // still stalled, but inside the re-alert interval
	if got := len(logs.find("LIVENESS: no block committed")); got != 1 {
		t.Fatalf("the alert must not repeat within a minute, got %d lines", got)
	}
	node.livenessCheck(start.Add(125 * time.Second))
	if got := len(logs.find("LIVENESS: no block committed")); got != 2 {
		t.Fatalf("a continuing stall must be re-reported every minute, got %d lines", got)
	}

	// A block commits: the next tick reports recovery, once.
	livenessMine(t, node)
	node.livenessCheck(start.Add(130 * time.Second))
	if got := len(logs.find("LIVENESS: recovered")); got != 1 {
		t.Fatalf("expected one recovery line, got %d", got)
	}
	if v := livenessMetricValue(t, "nhb_consensus_liveness_stalled", nil); v != 0 {
		t.Fatalf("stalled gauge = %v after recovery", v)
	}
	if v := livenessMetricValue(t, "nhb_consensus_last_commit_height", nil); v < 1 {
		t.Fatalf("last commit height = %v", v)
	}
	node.livenessCheck(start.Add(140 * time.Second))
	if got := len(logs.find("LIVENESS: recovered")); got != 1 {
		t.Fatalf("recovery is reported once, got %d", got)
	}
}

func TestLivenessSnapshotReportsBuildAndMempoolState(t *testing.T) {
	node := newTestNode(t)
	livenessFundedTransfers(t, node, 3)
	node.GetMempool() // leases all three
	snap := node.LivenessSnapshot()
	if snap.MempoolSize != 3 || snap.InFlight != 3 {
		t.Fatalf("snapshot mempool=%d inflight=%d, want 3 and 3", snap.MempoolSize, snap.InFlight)
	}
	if snap.ConsecutiveBuildFailures != 0 || snap.Stalled {
		t.Fatalf("a fresh node has no failures and is not stalled: %+v", snap)
	}
	var nilNode *Node
	if got := nilNode.LivenessSnapshot(); got != (LivenessSnapshot{}) {
		t.Fatalf("a nil node reports an empty snapshot")
	}
}

// ---- metrics -------------------------------------------------------------------

func TestContainmentMetricsAreRegisteredWithTheDesignedNames(t *testing.T) {
	node := newTestNode(t)
	livenessFundedTransfers(t, node, 2)
	// Touch every labelled vector so it appears in the registry.
	if _, err := node.CreateBlock(node.GetMempool()); err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error { return errors.New("x") })
	if _, err := node.CreateBlock(livenessFundedTransfers(t, node, 1)); err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	node.setProposalApplyHook(nil)
	node.livenessCheck(time.Now())

	for _, name := range []string{
		"nhb_consensus_build_failures_consecutive",
		"nhb_consensus_build_duration_seconds",
		"nhb_consensus_build_waves",
		"nhb_consensus_seconds_since_last_commit",
		"nhb_consensus_last_commit_height",
		"nhb_consensus_liveness_stalled",
		"nhb_consensus_tx_panics_recovered_total",
		"nhb_consensus_empty_block_fallbacks_total",
		"nhb_consensus_local_validation_failures_total",
		"nhb_mempool_tx_failures_total",
		"nhb_mempool_strike_records",
		"nhb_mempool_strike_overflow_evictions_total",
		"nhb_mempool_inflight_leases_expired_total",
		"nhb_consensus_block_interval_seconds",
	} {
		if !livenessMetricRegistered(t, name) {
			t.Errorf("metric %s is not registered", name)
		}
	}
	// The labelled counters only have children once used; the ones this test
	// exercised must have them, with bounded label values.
	if v := livenessMetricValue(t, "nhb_mempool_tx_failures_total", map[string]string{"disposition": "quarantine"}); v < 1 {
		t.Errorf("expected a quarantine failure to be counted, got %v", v)
	}
	if v := livenessMetricValue(t, "nhb_consensus_build_waves", nil); v < 1 {
		t.Errorf("expected build waves to be observed, got %v", v)
	}
}

func livenessMetricRegistered(t testing.TB, name string) bool {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, family := range families {
		if family.GetName() == name {
			return true
		}
	}
	return false
}

// ---- concurrency -----------------------------------------------------------------

// The escrow and trade engines are shared by every copy of the state processor
// and are re-pointed at the current copy by every apply. An admission-time
// simulation running concurrently with a wave could redirect one mid-apply and
// send the wave's writes into the wrong trie. The wave holds the state lock for
// its apply loop (as ValidateBlock and commitBlock do), so every block built
// while admissions hammer the node must still validate. Run under -race where
// the platform supports it (the Makefile's test target does).
func TestCreateBlockConcurrentWithAdmissionSimulationsProducesValidBlocks(t *testing.T) {
	node := newTestNode(t)
	clock := time.Unix(1_800_000_000, 0)
	node.SetTimeSource(func() time.Time { return clock })
	node.stateMu.Lock()
	node.state.nowFunc = func() time.Time { return clock } // deterministic: escrow is proposable
	node.stateMu.Unlock()

	senders := make([]*crypto.PrivateKey, 8)
	for i := range senders {
		senders[i] = livenessKey(t)
		livenessFund(t, node, senders[i].PubKey().Address().Bytes(), 1_000_000_000_000, 0)
	}
	payee := livenessKey(t).PubKey().Address().Bytes()
	deadline := clock.Add(time.Hour).Unix()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := uint64(1); ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			key := senders[int(i)%len(senders)]
			tx := livenessSign(t, key, types.TxTypeCreateEscrow, 0, escrowCreatePayload(t, payee, 10, deadline, i), nil, 0)
			_ = node.validateTransaction(tx) // the admission-time simulation
		}
	}()

	for round := 0; round < 40; round++ {
		txs := make([]*types.Transaction, 0, len(senders))
		for i, key := range senders {
			txs = append(txs, livenessSign(t, key, types.TxTypeCreateEscrow, 0,
				escrowCreatePayload(t, payee, 10, deadline, uint64(1000+round*10+i)), nil, 0))
		}
		block, err := node.CreateBlock(txs)
		if err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("round %d: CreateBlock: %v", round, err)
		}
		if err := node.ValidateBlock(block); err != nil {
			close(stop)
			wg.Wait()
			t.Fatalf("round %d: a block built while admissions ran does not validate: %v", round, err)
		}
	}
	close(stop)
	wg.Wait()
}

// realWallClock recognises the process clock (and only it), so a deterministic
// clock installed by a test or harness is not mistaken for a hazard.
func TestRealWallClockRecognisesOnlyTheProcessClock(t *testing.T) {
	if !realWallClock(time.Now) || !realWallClock(nil) {
		t.Fatalf("time.Now and an unset clock are the wall clock")
	}
	fixed := time.Unix(1, 0)
	if realWallClock(func() time.Time { return fixed }) {
		t.Fatalf("a fixed clock is deterministic, not the wall clock")
	}
}

func TestParseTxTypeSet(t *testing.T) {
	set, bad := parseTxTypeSet("0x03, 5 ,0xFF,zz,,0x1FF")
	if len(set) != 3 {
		t.Fatalf("expected three types, got %v", set)
	}
	for _, typ := range []types.TxType{0x03, 0x05, 0xFF} {
		if _, ok := set[typ]; !ok {
			t.Fatalf("missing type 0x%02X", byte(typ))
		}
	}
	if len(bad) != 2 {
		t.Fatalf("expected two unparseable entries, got %v", bad)
	}
}
