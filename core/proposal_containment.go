package core

// Block-production containment.
//
// Block production is the entire chain: if a proposer cannot build a block,
// nothing else matters. Before this file existed, CreateBlock aborted the
// WHOLE proposal on any apply error that classifyProposalError did not
// recognise, and an aborted transaction was never removed from the mempool,
// so a single unclassified error from one submitted transaction (or from a
// pair of ordinary transactions that each pass admission on their own) failed
// every proposal on every round, forever.
//
// The structural guarantee implemented here (G1): for any set of at most
// Blocks.MaxTxs candidate transactions, CreateBlock returns a valid block --
// it does not return an error and it does not panic -- provided the empty
// block itself can be built (state copy, lifecycle and evidence processing
// succeed) and no single transaction apply outlasts the budget. It works in
// layers, every one of them PROPOSER-LOCAL:
//
//   - Waves. A wave executes all candidates against a fresh copy of committed
//     state and collects every failure instead of stopping at the first. Any
//     failure discards the wave's state (a failed transaction may have
//     partially mutated it -- there is deliberately no per-transaction
//     rollback) and the survivors run again from another fresh copy. Each
//     wave either finishes clean or removes at least one candidate, waves are
//     capped, and a wall-clock budget is checked between transactions.
//   - Panic containment. Each apply runs under recover().
//   - Fallbacks. On wave or budget exhaustion the clean prefix of the last
//     wave is re-verified and used; failing that, an EMPTY block is built.
//     That is exactly the block a proposer already builds on every idle
//     round, so validators already accept it.
//   - Mempool hygiene (strike book, backoff, TTL, leases) so that whatever
//     cannot apply stops being offered and is eventually evicted locally.
//
// Why this cannot cause consensus divergence: classifyProposalError and this
// file run only inside CreateBlock. ValidateBlock and commitBlock never call
// them; they re-execute the block's transactions and reject it on any apply
// error, exactly as before. The final, successful wave is a zero-failure
// sequential execution of precisely the transactions placed in the block, run
// from a fresh copy of the same state through the same BeginBlock, seed,
// reconcile, lifecycle, evidence and finalize steps that ValidateBlock runs,
// with the dependency graph recomputed on that exact list. So every block a
// hardened proposer emits satisfies the predicate every validator (hardened
// or not) already applies. Nothing here adds a wire field, a state key or a
// consensus rule, and nothing here reads a clock, iterates a map or draws a
// random number on any path that ValidateBlock or commitBlock executes: the
// wall clock is used only for budgets, TTLs and lease expiry, all of which
// merely choose which transactions this proposer includes.
//
// What this file deliberately does NOT do: it does not change how any
// transaction applies, it does not touch ProcessBlockLifecycle (a lifecycle
// failure that persists on the empty block cannot be contained proposer-side;
// it is detected, logged and alarmed instead), and it does not snapshot or
// revert individual transactions inside a block (a proposer-only revert would
// risk diverging from what validators compute).

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"reflect"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/ethereum/go-ethereum/rlp"
	gethtrie "github.com/ethereum/go-ethereum/trie"
	"github.com/syndtr/goleveldb/leveldb"
	leveldberrors "github.com/syndtr/goleveldb/leveldb/errors"

	"nhbchain/core/types"
	"nhbchain/observability"
)

// ---- configuration ---------------------------------------------------------

const (
	defaultBuildBudget     = time.Second
	defaultMaxWaves        = 6
	defaultPrefixBudget    = 300 * time.Millisecond
	defaultSlowTx          = 250 * time.Millisecond
	defaultStrikeLimit     = 3
	defaultSkipTTL         = 2 * time.Hour
	defaultAbsoluteTTL     = 24 * time.Hour
	defaultInflightLease   = 30 * time.Second
	defaultStallAfter      = 60 * time.Second
	defaultIsolationK      = 2
	defaultIsolationBudget = 2 * time.Second
	defaultMaxEvictions    = 256

	// sdcBudgetShare is the fraction of the build budget after which solo
	// dry-run confirmation stops so it cannot starve the rest of the build.
	sdcBudgetShare = 0.6

	// maxIsolationCulprits bounds how many transactions one joint-pass
	// isolation run will bisect out.
	maxIsolationCulprits = 8

	// maxPerTxLogLines caps per-transaction log detail per build; anything
	// beyond it is only counted.
	maxPerTxLogLines = 5

	// maxLoggedErrorLen and maxPanicStackLen bound attacker-influenced text
	// that reaches logs or retained state.
	maxLoggedErrorLen = 160
	maxPanicStackLen  = 4 << 10
)

// buildConfig holds the tunables of block-production containment. The zero
// value is not usable; start from defaultBuildConfig. Defaults come from the
// approved design; every knob is also reachable through an environment
// variable read once in NewNode (see loadBuildConfig) so operators can tune a
// running deployment without a config-schema change.
type buildConfig struct {
	// Budget bounds all waves of one CreateBlock call. It defaults to one
	// second and is lowered to half the BFT proposal timeout at start-up
	// (SetProposalBuildBudget) so a slow build cannot burn the round.
	Budget       time.Duration
	budgetPinned bool // set when NHB_PROPOSAL_BUILD_BUDGET_MS is present
	// MaxWaves caps execution waves per build (exploratory plus final).
	MaxWaves int
	// PrefixBudget bounds the single verification wave over the clean prefix
	// that runs when the main budget or wave cap is exhausted.
	PrefixBudget time.Duration
	// SlowTx flags a transaction whose apply took longer than this even
	// though it succeeded; it is excluded like any other failure.
	SlowTx time.Duration
	// Strikes is N: failures across distinct builds before a transaction is
	// eligible for confirmed eviction.
	Strikes int
	// SkipTTL evicts a transaction that has only ever been skipped (a
	// designed-transient cause) once its first skip is older than this.
	SkipTTL time.Duration
	// AbsoluteTTL evicts any transaction with a failure record once its first
	// failure is older than this.
	AbsoluteTTL time.Duration
	// InflightLease is how long a proposal-offered transaction stays hidden
	// from GetMempool before the lease lapses.
	InflightLease time.Duration
	// StallAfter is the watchdog threshold for "no block committed".
	StallAfter time.Duration
	// IsolationK is the consecutive whole-build failures that start async
	// isolation; IsolationBudget bounds one isolation run.
	IsolationK      int
	IsolationBudget time.Duration
	// MaxEvictions caps evictions per build so confirmed-eviction work cannot
	// dominate a build.
	MaxEvictions int
	// AllowClockDependent disables the wall-clock read detector (dev networks).
	AllowClockDependent bool
	// ExcludeTypes is a static operator deny-list of transaction types this
	// proposer will not include; matching transactions stay resident.
	ExcludeTypes map[types.TxType]struct{}
}

func defaultBuildConfig() buildConfig {
	return buildConfig{
		Budget:          defaultBuildBudget,
		MaxWaves:        defaultMaxWaves,
		PrefixBudget:    defaultPrefixBudget,
		SlowTx:          defaultSlowTx,
		Strikes:         defaultStrikeLimit,
		SkipTTL:         defaultSkipTTL,
		AbsoluteTTL:     defaultAbsoluteTTL,
		InflightLease:   defaultInflightLease,
		StallAfter:      defaultStallAfter,
		IsolationK:      defaultIsolationK,
		IsolationBudget: defaultIsolationBudget,
		MaxEvictions:    defaultMaxEvictions,
	}
}

// loadBuildConfig applies environment overrides on top of base. Malformed
// values are ignored and reported in the returned warnings rather than
// failing start-up: a typo in a tuning knob must never stop a validator.
func loadBuildConfig(base buildConfig, getenv func(string) string) (buildConfig, []string) {
	cfg := base
	var warnings []string
	get := func(name string) string { return strings.TrimSpace(getenv(name)) }

	positiveInt := func(name string, apply func(int)) {
		v := get(name)
		if v == "" {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			warnings = append(warnings, fmt.Sprintf("%s=%q ignored: want a positive integer", name, v))
			return
		}
		apply(n)
	}
	duration := func(name string, unit time.Duration, apply func(time.Duration)) {
		v := get(name)
		if v == "" {
			return
		}
		if n, err := strconv.Atoi(v); err == nil {
			if n <= 0 {
				warnings = append(warnings, fmt.Sprintf("%s=%q ignored: want a positive value", name, v))
				return
			}
			apply(time.Duration(n) * unit)
			return
		}
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			apply(d)
			return
		}
		warnings = append(warnings, fmt.Sprintf("%s=%q ignored: want a positive number or duration", name, v))
	}

	duration("NHB_PROPOSAL_BUILD_BUDGET_MS", time.Millisecond, func(d time.Duration) {
		cfg.Budget = d
		cfg.budgetPinned = true
	})
	positiveInt("NHB_PROPOSAL_MAX_WAVES", func(n int) { cfg.MaxWaves = n })
	positiveInt("NHB_POISON_STRIKES", func(n int) { cfg.Strikes = n })
	duration("NHB_POISON_SKIP_TTL", time.Second, func(d time.Duration) { cfg.SkipTTL = d })
	duration("NHB_INFLIGHT_LEASE_SECS", time.Second, func(d time.Duration) { cfg.InflightLease = d })
	duration("NHB_LIVENESS_STALL_SECS", time.Second, func(d time.Duration) { cfg.StallAfter = d })
	if v := get("NHB_ALLOW_CLOCK_DEPENDENT_TXS"); v != "" {
		allow, err := strconv.ParseBool(v)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("NHB_ALLOW_CLOCK_DEPENDENT_TXS=%q ignored: want true or false", v))
		} else {
			cfg.AllowClockDependent = allow
		}
	}
	if v := get("NHB_PROPOSER_EXCLUDE_TXTYPES"); v != "" {
		set, bad := parseTxTypeSet(v)
		for _, entry := range bad {
			warnings = append(warnings, fmt.Sprintf("NHB_PROPOSER_EXCLUDE_TXTYPES entry %q ignored: want a transaction type byte such as 0x03", entry))
		}
		cfg.ExcludeTypes = set
	}
	return cfg, warnings
}

// parseTxTypeSet parses a comma-separated list of transaction type bytes
// ("0x03,0x05" or decimal). Entries that do not parse are returned in bad.
func parseTxTypeSet(csv string) (set map[types.TxType]struct{}, bad []string) {
	set = make(map[types.TxType]struct{})
	for _, part := range strings.Split(csv, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseUint(part, 0, 8)
		if err != nil {
			bad = append(bad, part)
			continue
		}
		set[types.TxType(n)] = struct{}{}
	}
	return set, bad
}

// buildConfigSnapshot returns a copy of the active configuration.
func (n *Node) buildConfigSnapshot() buildConfig {
	if n == nil {
		return defaultBuildConfig()
	}
	n.buildMu.RLock()
	cfg := n.buildCfg
	n.buildMu.RUnlock()
	if cfg.Budget <= 0 || cfg.MaxWaves <= 0 {
		// A Node assembled without NewNode (some tests) has a zero config.
		def := defaultBuildConfig()
		def.AllowClockDependent = cfg.AllowClockDependent
		def.ExcludeTypes = cfg.ExcludeTypes
		return def
	}
	return cfg
}

// setBuildConfig lets callers (tests, start-up wiring) adjust the active
// configuration atomically.
func (n *Node) setBuildConfig(mutate func(*buildConfig)) {
	if n == nil || mutate == nil {
		return
	}
	n.buildMu.Lock()
	if n.buildCfg.Budget <= 0 || n.buildCfg.MaxWaves <= 0 {
		n.buildCfg = defaultBuildConfig()
	}
	mutate(&n.buildCfg)
	n.buildMu.Unlock()
}

// SetProposalBuildBudget lowers the wall-clock budget for assembling one block
// proposal to d, capped at the one-second default. cmd wiring passes half the
// BFT proposal timeout: the proposer's own local validation, the broadcast and
// the peer's validation all follow the build, and the peer's round timers are
// already running while the proposer builds. An operator override through
// NHB_PROPOSAL_BUILD_BUDGET_MS wins over this call.
func (n *Node) SetProposalBuildBudget(d time.Duration) {
	if n == nil || d <= 0 {
		return
	}
	n.setBuildConfig(func(cfg *buildConfig) {
		if cfg.budgetPinned {
			return
		}
		if d > defaultBuildBudget {
			d = defaultBuildBudget
		}
		cfg.Budget = d
	})
}

// localNow is the proposer-local operations clock used for budgets, TTLs and
// leases. It is deliberately separate from currentTime(), which supplies the
// consensus block timestamp.
func (n *Node) localNow() time.Time {
	n.buildMu.RLock()
	clock := n.buildClock
	n.buildMu.RUnlock()
	if clock == nil {
		return time.Now()
	}
	return clock()
}

// setLocalClock replaces the operations clock (tests only).
func (n *Node) setLocalClock(clock func() time.Time) {
	n.buildMu.Lock()
	n.buildClock = clock
	n.buildMu.Unlock()
}

// ---- test seams ------------------------------------------------------------

// proposalHooks are test seams: nil in production. They only ever run inside
// CreateBlock's proposer-side execution, never in ValidateBlock/commitBlock.
type proposalHooks struct {
	// apply runs before each real ApplyTransaction; a non-nil return is treated
	// as that transaction's apply error and a panic is contained like a panic
	// inside apply. wave is 1-based; solo dry runs pass wave -1.
	apply func(wave, index int, tx *types.Transaction) error
	// applied runs right after a transaction applied without error, on the
	// same state copy, so a test can make that copy read the clock the way a
	// transaction that stamped a wall-clock value into state would.
	applied func(sp *StateProcessor, tx *types.Transaction)
	// lifecycle and evidence run just before the corresponding stage of a
	// clean wave.
	lifecycle func(sp *StateProcessor, txs []*types.Transaction) error
	evidence  func(sp *StateProcessor, txs []*types.Transaction) error
}

func (n *Node) currentHooks() *proposalHooks {
	if n == nil {
		return nil
	}
	return n.hooks.Load()
}

func (n *Node) updateHooks(mutate func(*proposalHooks)) {
	for {
		old := n.hooks.Load()
		next := &proposalHooks{}
		if old != nil {
			*next = *old
		}
		mutate(next)
		if n.hooks.CompareAndSwap(old, next) {
			return
		}
	}
}

func (n *Node) setProposalApplyHook(h func(wave, index int, tx *types.Transaction) error) {
	n.updateHooks(func(p *proposalHooks) { p.apply = h })
}

func (n *Node) setProposalAppliedHook(h func(sp *StateProcessor, tx *types.Transaction)) {
	n.updateHooks(func(p *proposalHooks) { p.applied = h })
}

func (n *Node) setProposalLifecycleHook(h func(sp *StateProcessor, txs []*types.Transaction) error) {
	n.updateHooks(func(p *proposalHooks) { p.lifecycle = h })
}

func (n *Node) setProposalEvidenceHook(h func(sp *StateProcessor, txs []*types.Transaction) error) {
	n.updateHooks(func(p *proposalHooks) { p.evidence = h })
}

// ---- errors ----------------------------------------------------------------

var (
	errBuildBudget = errors.New("block build budget exhausted")
	errBuildWaves  = errors.New("block build did not converge within the wave limit")
	// errClockDependent marks a transaction that read the wall clock while
	// applying. Its outcome (and, for escrow creation, the state it writes)
	// depends on the second in which it runs, so a block containing it cannot
	// be re-executed identically by a validator a moment later.
	errClockDependent = errors.New("transaction reads the wall clock during apply and cannot be proposed deterministically")
	errSlowTx         = errors.New("transaction apply exceeded the per-transaction time limit")
)

// txPanicError is the error a recovered apply panic is turned into.
type txPanicError struct {
	Value any
	Stack []byte
}

func (e *txPanicError) Error() string {
	return "tx apply panic: " + truncateText(fmt.Sprint(e.Value), maxLoggedErrorLen)
}

func newTxPanicError(value any) *txPanicError {
	stack := debug.Stack()
	if len(stack) > maxPanicStackLen {
		stack = stack[:maxPanicStackLen]
	}
	return &txPanicError{Value: value, Stack: stack}
}

// Build stages, for classification of whole-build failures.
const (
	stageState     = "state"
	stageLifecycle = "block lifecycle"
	stageEvidence  = "process evidence"
)

// buildStageError marks a failure that is not attributable to any single
// transaction: state set-up, lifecycle or evidence processing.
type buildStageError struct {
	Stage string
	Err   error
}

func (e *buildStageError) Error() string { return e.Stage + ": " + e.Err.Error() }
func (e *buildStageError) Unwrap() error { return e.Err }

// buildPanicError is a panic that escaped a build stage and was contained at
// the top of CreateBlock.
type buildPanicError struct{ Value any }

func (e *buildPanicError) Error() string {
	return "block build panic: " + truncateText(fmt.Sprint(e.Value), maxLoggedErrorLen)
}

// blockApplyError is returned by ValidateBlock/commitBlock when a transaction
// of the block fails to apply. Its message is identical to the plain wrapped
// error it replaces ("apply transaction N: ..."), so acceptance and logs do
// not change; the Index is what lets the proposer attribute a rejection of
// its own block to the transaction that caused it.
type blockApplyError struct {
	Index int
	Err   error
}

func (e *blockApplyError) Error() string {
	return fmt.Sprintf("apply transaction %d: %v", e.Index, e.Err)
}
func (e *blockApplyError) Unwrap() error { return e.Err }

// isInfrastructureError reports whether err says something about this node's
// ability to read or write state rather than about the transaction being
// applied. Such an error must abort the wave (excluding transactions cannot
// fix a broken database) and must never count against a transaction. It is an
// allowlist: every other error is attributable to the transaction.
func isInfrastructureError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var missing *gethtrie.MissingNodeError
	if errors.As(err, &missing) {
		return true
	}
	if errors.Is(err, os.ErrClosed) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EIO) {
		return true
	}
	if errors.Is(err, leveldb.ErrClosed) {
		return true
	}
	var corrupted *leveldberrors.ErrCorrupted
	if errors.As(err, &corrupted) {
		return true
	}
	// Other packages can opt in without this package importing them.
	var infra interface{ Infrastructure() bool }
	if errors.As(err, &infra) && infra.Infrastructure() {
		return true
	}
	return false
}

// classifyWaveFailure extends classifyProposalError with the two verdicts only
// the wave engine can produce.
func classifyWaveFailure(err error) proposalTxDisposition {
	switch {
	case errors.Is(err, errClockDependent):
		return proposalDispositionNondeterministic
	case errors.Is(err, errSlowTx):
		return proposalDispositionQuarantine
	}
	return classifyProposalError(err)
}

func dispositionLabel(d proposalTxDisposition) string {
	switch d {
	case proposalDispositionPrune:
		return "prune"
	case proposalDispositionSkip:
		return "skip"
	case proposalDispositionQuarantine:
		return "quarantine"
	case proposalDispositionNondeterministic:
		return "nondeterministic"
	default:
		return "abort"
	}
}

// buildFailureReason maps a whole-build error to the bounded metric label.
func buildFailureReason(err error) string {
	var (
		panicErr *buildPanicError
		stageErr *buildStageError
	)
	switch {
	case err == nil:
		return "other"
	case errors.As(err, &panicErr):
		return "panic"
	case errors.Is(err, errBuildBudget):
		return "budget"
	case errors.Is(err, errBuildWaves):
		return "waves"
	case errors.As(err, &stageErr):
		switch stageErr.Stage {
		case stageLifecycle:
			return "lifecycle"
		case stageEvidence:
			return "evidence"
		case stageState:
			return "state_copy"
		}
	case isInfrastructureError(err):
		return "infra"
	}
	return "other"
}

// ---- log text hygiene ------------------------------------------------------

// truncateText bounds text to max bytes without splitting a UTF-8 sequence.
func truncateText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := text[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// sanitizeLogText makes attacker-influenced text safe to log: bounded length,
// valid UTF-8 and no control characters (log injection).
func sanitizeLogText(text string) string {
	text = truncateText(strings.ToValidUTF8(text, "?"), maxLoggedErrorLen)
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, text)
}

// ---- wall-clock read detector ----------------------------------------------

// realWallClock reports whether fn is the process wall clock (time.Now) or
// unset (StateProcessor.now falls back to time.Now for a nil func). A test or
// harness that installs its own deterministic clock is not a hazard: every
// re-execution sees the same values.
func realWallClock(fn func() time.Time) bool {
	if fn == nil {
		return true
	}
	return reflect.ValueOf(fn).Pointer() == reflect.ValueOf(time.Now).Pointer()
}

// installClockGuard wraps this state copy's clock so every read during apply is
// counted. It returns the same values the underlying clock does -- it adds no
// new time source and changes no result -- and touches only this wave's own
// copy. A nil return means the guard is not needed (a deterministic clock is
// installed, or the operator disabled the detector).
func installClockGuard(sp *StateProcessor, allow bool) *atomic.Uint64 {
	if sp == nil || allow || !realWallClock(sp.nowFunc) {
		return nil
	}
	reads := new(atomic.Uint64)
	base := sp.nowFunc
	if base == nil {
		base = time.Now
	}
	sp.nowFunc = func() time.Time {
		reads.Add(1)
		return base()
	}
	return reads
}

func clockReads(reads *atomic.Uint64) uint64 {
	if reads == nil {
		return 0
	}
	return reads.Load()
}

// ---- build context ---------------------------------------------------------

// buildStats aggregates one build's outcomes for a single log line and for
// the metrics; it never retains per-transaction data beyond bounded counters.
type buildStats struct {
	pruned           int
	skipped          int
	quarantined      int
	nondeterministic int
	slow             int
	panics           int
	evicted          int
	denied           int
	perTxLogged      int
	errClasses       map[string]int
}

func (s *buildStats) total() int {
	return s.pruned + s.skipped + s.quarantined + s.nondeterministic + s.evicted + s.denied
}

// buildCtx carries everything one CreateBlock call needs. It is owned by the
// goroutine running the build and is not safe for concurrent use.
type buildCtx struct {
	n         *Node
	cfg       buildConfig
	input     []*types.Transaction // exactly as the caller offered them
	window    []*types.Transaction // candidates after precheck and the MaxTxs clamp
	height    uint64
	prevHash  []byte
	timestamp int64
	validator []byte

	now      func() time.Time
	started  time.Time
	deadline time.Time // zero means unbounded

	keys       map[*types.Transaction]string
	pruned     []*types.Transaction
	prunedKeys map[string]struct{}
	failedKeys map[string]struct{}

	stats    buildStats
	waves    int
	fellBack bool
	included []*types.Transaction

	// skipEvidence omits the evidence stage from a wave. Isolation trials set
	// it: processPendingEvidenceForState mutates the Node-wide penalty ledger,
	// which is not scoped to the state copy a trial runs on, so a trial that
	// ran it could make the ledger believe a penalty was already applied and
	// change what the node's real passes over the next block compute (see
	// TestKnownIssueEvidenceSlashingIsNotIdempotentAcrossPasses). Trials give
	// up detecting evidence-stage failures rather than risk that.
	skipEvidence bool
}

func (n *Node) newBuildCtx(txs []*types.Transaction) *buildCtx {
	cfg := n.buildConfigSnapshot()
	c := &buildCtx{
		n:          n,
		cfg:        cfg,
		input:      txs,
		now:        n.localNow,
		keys:       make(map[*types.Transaction]string),
		prunedKeys: make(map[string]struct{}),
		failedKeys: make(map[string]struct{}),
	}
	c.stats.errClasses = make(map[string]int)
	c.timestamp = n.currentTime().Unix()
	c.height = n.chain.GetHeight() + 1
	c.prevHash = n.chain.Tip()
	c.validator = n.validatorKey.PubKey().Address().Bytes()
	c.started = c.now()
	c.deadline = c.started.Add(cfg.Budget)
	return c
}

func (c *buildCtx) expired() bool {
	return !c.deadline.IsZero() && !c.now().Before(c.deadline)
}

func (c *buildCtx) elapsed() time.Duration { return c.now().Sub(c.started) }

// keyOf returns tx's mempool key, or "" when it cannot be computed.
func (c *buildCtx) keyOf(tx *types.Transaction) string {
	if tx == nil {
		return ""
	}
	if key, ok := c.keys[tx]; ok {
		return key
	}
	key, err := transactionKey(tx)
	if err != nil {
		key = ""
	}
	c.keys[tx] = key
	return key
}

func (c *buildCtx) markPruned(tx *types.Transaction, key string) {
	c.pruned = append(c.pruned, tx)
	if key != "" {
		c.prunedKeys[key] = struct{}{}
	}
}

func (c *buildCtx) noteErrorClass(err error) {
	if err == nil || len(c.stats.errClasses) >= 64 {
		return
	}
	c.stats.errClasses[sanitizeLogText(err.Error())]++
}

// ---- CreateBlock -----------------------------------------------------------

// CreateBlock assembles a block proposal from txs. See the file comment for
// the guarantee it provides; in short, it returns a valid block for any
// candidate set (an empty one in the worst case) and returns an error only
// when even the empty block cannot be built, which is a node-level fault (state
// copy, epoch lifecycle or evidence processing), not something a transaction
// can cause and not something excluding transactions can fix.
func (n *Node) CreateBlock(txs []*types.Transaction) (block *types.Block, err error) {
	ctx := n.newBuildCtx(txs)
	defer func() {
		n.finishBuild(ctx, block, err)
	}()

	block, err = n.safeBuild(ctx, n.buildWithContainment)
	if err == nil {
		return block, nil
	}
	fullErr := err
	n.noteWholeBuildFailure(ctx, fullErr)

	empty, emptyErr := n.safeBuild(ctx, n.buildEmpty)
	if emptyErr == nil {
		ctx.fellBack = true
		n.noteEmptyFallback(ctx, fullErr)
		return empty, nil
	}
	n.noteBuildImpossible(ctx, fullErr, emptyErr)
	return nil, fmt.Errorf("%w (empty-block fallback also failed: %v)", fullErr, emptyErr)
}

// safeBuild runs one build strategy and converts a panic that escaped every
// inner recover into an error, so nothing in CreateBlock can crash the node.
// Locks taken inside a strategy are released by defer, so unwinding a panic
// through it cannot leave one held.
func (n *Node) safeBuild(ctx *buildCtx, build func(*buildCtx) (*types.Block, error)) (block *types.Block, err error) {
	defer func() {
		if r := recover(); r != nil {
			block, err = nil, &buildPanicError{Value: r}
			n.reportBuildPanic(r, debug.Stack())
		}
	}()
	return build(ctx)
}

func (n *Node) reportBuildPanic(value any, stack []byte) {
	if len(stack) > maxPanicStackLen {
		stack = stack[:maxPanicStackLen]
	}
	slog.Error("LIVENESS: panic in block build",
		slog.String("event", "build_panic"),
		slog.String("panic", sanitizeLogText(fmt.Sprint(value))),
		slog.String("stack", string(stack)),
	)
}

// buildWithContainment is the full build: precheck, then waves until one is
// clean, then the prefix fallback. It returns an error only for failures that
// are not attributable to a transaction (or when it runs out of budget or
// waves); CreateBlock then falls back to the empty block.
func (n *Node) buildWithContainment(ctx *buildCtx) (*types.Block, error) {
	cands := n.precheck(ctx, ctx.input)
	ctx.window = cands

	var last *waveResult
	for wave := 1; wave <= ctx.cfg.MaxWaves; wave++ {
		if ctx.expired() {
			break
		}
		w, err := n.executeWave(ctx, wave, cands)
		ctx.waves = wave
		if err != nil {
			return nil, err
		}
		if w.clean() {
			return n.seal(ctx, w)
		}
		last = w
		n.recordWaveOutcome(ctx, w)
		if w.BudgetHit {
			break
		}
		cands = w.Kept
	}
	return n.prefixFallback(ctx, last)
}

// buildEmpty builds a block with no transactions. Only state set-up,
// lifecycle and evidence processing can fail here.
func (n *Node) buildEmpty(ctx *buildCtx) (*types.Block, error) {
	ctx.deadline = time.Time{}
	w, err := n.executeWave(ctx, ctx.waves+1, nil)
	ctx.waves++
	if err != nil {
		return nil, err
	}
	if !w.clean() {
		return nil, errBuildWaves
	}
	return n.seal(ctx, w)
}

// prefixFallback is used when the wave cap or the budget is exhausted. The
// transactions of the last wave that applied before its first failure ran
// against a state polluted only by successful applies; re-verify exactly that
// prefix in a fresh wave with its own short budget and use it if it comes back
// clean. Otherwise the caller falls back to the empty block.
func (n *Node) prefixFallback(ctx *buildCtx, last *waveResult) (*types.Block, error) {
	if last == nil {
		return nil, errBuildBudget
	}
	exhausted := errBuildWaves
	if last.BudgetHit || ctx.expired() {
		exhausted = errBuildBudget
	}
	prefix := last.CleanPrefix
	if len(prefix) == 0 {
		return nil, exhausted
	}
	ctx.deadline = ctx.now().Add(ctx.cfg.PrefixBudget)
	w, err := n.executeWave(ctx, ctx.waves+1, prefix)
	ctx.waves++
	if err != nil {
		return nil, err
	}
	if w.clean() {
		return n.seal(ctx, w)
	}
	n.recordWaveOutcome(ctx, w)
	return nil, exhausted
}

// precheck drops candidates that can never be proposed before any state is
// touched, and applies the MaxTxs clamp. It never fails a build.
func (n *Node) precheck(ctx *buildCtx, txs []*types.Transaction) []*types.Transaction {
	if len(txs) == 0 {
		return nil
	}
	maxTxs := n.globalConfigSnapshot().Blocks.MaxTxs
	out := make([]*types.Transaction, 0, len(txs))
	var expiredVouchers []*types.Transaction
	for _, tx := range txs {
		if maxTxs > 0 && int64(len(out)) >= maxTxs {
			break
		}
		if tx == nil {
			continue
		}
		// A mint voucher past its expiry can never apply again.
		if tx.Type == types.TxTypeMint {
			voucher, _, err := decodeMintTransaction(tx.Data)
			if err != nil || voucher == nil || voucher.Expiry <= ctx.timestamp {
				expiredVouchers = append(expiredVouchers, tx)
				continue
			}
		}
		if tx.Type == types.TxTypeSwapVoucherMint {
			submission, err := decodeSwapVoucherMintTransaction(tx.Data)
			if err != nil || submission == nil || submission.Voucher == nil || submission.Voucher.Expiry <= ctx.timestamp {
				expiredVouchers = append(expiredVouchers, tx)
				continue
			}
		}
		// A transaction that cannot be hashed, whose sender cannot be
		// recovered, or that cannot be RLP-encoded can never be placed in a
		// block (ComputeTxRoot and the scheduler would fail on it), so it is
		// dropped here instead of failing the whole build later.
		if ctx.keyOf(tx) == "" {
			ctx.markPruned(tx, "")
			ctx.stats.pruned++
			continue
		}
		if _, err := rlp.EncodeToBytes(tx); err != nil {
			ctx.markPruned(tx, ctx.keyOf(tx))
			n.dropTransactionsFromMempool([]*types.Transaction{tx})
			ctx.stats.pruned++
			continue
		}
		if _, denied := ctx.cfg.ExcludeTypes[tx.Type]; denied {
			ctx.stats.denied++
			continue
		}
		out = append(out, tx)
	}
	if len(expiredVouchers) > 0 {
		for _, tx := range expiredVouchers {
			ctx.markPruned(tx, ctx.keyOf(tx))
		}
		n.markTransactionsCommitted(expiredVouchers)
	}
	return out
}

// ---- waves -----------------------------------------------------------------

// txFailure is one candidate that did not apply in a wave.
type txFailure struct {
	Index       int
	Tx          *types.Transaction
	Err         error
	Disposition proposalTxDisposition
	Panicked    bool
}

// waveResult is the outcome of executing one candidate list against a fresh
// state copy.
type waveResult struct {
	Wave      int
	Ordered   []*types.Transaction // candidate list in canonical execution order
	GraphRoot []byte               // execution graph root of Ordered
	Kept      []*types.Transaction // candidates that applied cleanly, in order
	Failures  []txFailure
	// CleanPrefix is the longest run of Ordered that applied before the first
	// failure (or before the budget ran out when nothing had failed).
	CleanPrefix []*types.Transaction
	BudgetHit   bool
	// State is set only for a clean wave: fully finalized and ready to seal.
	State *StateProcessor
}

func (w *waveResult) clean() bool {
	return w != nil && w.State != nil && len(w.Failures) == 0 && !w.BudgetHit
}

// freshBlockStateLocked returns a private copy of the current state set up
// exactly as ValidateBlock sets up its own: pause view, quota config,
// BeginBlock for the block being built, and the two idempotent one-time state
// repairs that must precede a block's transactions. The caller must hold
// n.stateMu for writing.
func (n *Node) freshBlockStateLocked(ctx *buildCtx) (*StateProcessor, error) {
	if err := n.refreshModulePauses(); err != nil {
		return nil, &buildStageError{Stage: stageState, Err: err}
	}
	sp, err := n.state.Copy()
	if err != nil {
		return nil, &buildStageError{Stage: stageState, Err: err}
	}
	sp.SetPauseView(n)
	sp.SetQuotaConfig(n.moduleQuotaSnapshot())
	sp.BeginBlock(ctx.height, time.Unix(ctx.timestamp, 0).UTC())
	if err := sp.SeedGenesisNHBSupplyOnce(); err != nil {
		sp.EndBlock()
		return nil, &buildStageError{Stage: stageState, Err: fmt.Errorf("seed genesis NHB supply: %w", err)}
	}
	if err := sp.ReconcileNHBMintSupplyDriftOnce(); err != nil {
		sp.EndBlock()
		return nil, &buildStageError{Stage: stageState, Err: fmt.Errorf("reconcile NHB mint supply drift: %w", err)}
	}
	return sp, nil
}

// applyTx applies one transaction to sp under recover(). A panic becomes a
// *txPanicError; the state copy that saw it is always discarded by the caller.
func (n *Node) applyTx(hooks *proposalHooks, sp *StateProcessor, wave, index int, tx *types.Transaction) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = newTxPanicError(r)
			n.build.panicsRecovered.Add(1)
			observability.Consensus().RecordTxPanicRecovered()
		}
	}()
	if hooks != nil && hooks.apply != nil {
		if herr := hooks.apply(wave, index, tx); herr != nil {
			return herr
		}
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		return err
	}
	if hooks != nil && hooks.applied != nil {
		hooks.applied(sp, tx)
	}
	return nil
}

// executeWave runs cands, in canonical order, against a fresh state copy.
//
// It holds n.stateMu for the whole wave, as ValidateBlock and commitBlock do
// for their identical apply loops. That matters beyond mutual exclusion: the
// escrow and trade engines are shared by every copy of the state processor and
// are re-pointed at the current copy by every apply, so a concurrent
// admission-time simulation could otherwise redirect one of them mid-apply and
// send this wave's writes into the wrong trie. The lock is released by defer,
// so a panic cannot leave it held, and no mempool lock is taken while it is
// held.
//
// A wave that has any failure (or runs out of budget) returns without running
// lifecycle or evidence processing; only a wave in which every candidate
// applied runs them and returns a finalized State. An error return means the
// failure is not attributable to a transaction.
func (n *Node) executeWave(ctx *buildCtx, wave int, cands []*types.Transaction) (*waveResult, error) {
	ordered, graphRoot, err := computeDependencyGraph(cands)
	if err != nil {
		return nil, &buildStageError{Stage: stageState, Err: fmt.Errorf("canonical scheduler failed: %w", err)}
	}

	n.stateMu.Lock()
	defer n.stateMu.Unlock()

	sp, err := n.freshBlockStateLocked(ctx)
	if err != nil {
		return nil, err
	}
	reads := installClockGuard(sp, ctx.cfg.AllowClockDependent)
	hooks := n.currentHooks()

	res := &waveResult{
		Wave:      wave,
		Ordered:   ordered,
		GraphRoot: graphRoot,
		Kept:      make([]*types.Transaction, 0, len(ordered)),
	}
	for i, tx := range ordered {
		if ctx.expired() {
			res.BudgetHit = true
			break
		}
		before := clockReads(reads)
		started := ctx.now()
		applyErr := n.applyTx(hooks, sp, wave, i, tx)
		took := ctx.now().Sub(started)
		if applyErr == nil && clockReads(reads) != before {
			applyErr = errClockDependent
		}
		if applyErr == nil && ctx.cfg.SlowTx > 0 && took > ctx.cfg.SlowTx {
			applyErr = errSlowTx
		}
		if applyErr == nil {
			res.Kept = append(res.Kept, tx)
			continue
		}
		disposition := classifyWaveFailure(applyErr)
		if disposition == proposalDispositionAbort {
			sp.EndBlock()
			return nil, applyErr
		}
		if len(res.Failures) == 0 {
			res.CleanPrefix = ordered[:i:i]
		}
		var panicErr *txPanicError
		res.Failures = append(res.Failures, txFailure{
			Index:       i,
			Tx:          tx,
			Err:         applyErr,
			Disposition: disposition,
			Panicked:    errors.As(applyErr, &panicErr),
		})
	}
	if len(res.Failures) == 0 {
		res.CleanPrefix = res.Kept
	}
	if len(res.Failures) > 0 || res.BudgetHit {
		sp.EndBlock()
		return res, nil
	}

	if hooks != nil && hooks.lifecycle != nil {
		if herr := hooks.lifecycle(sp, res.Kept); herr != nil {
			sp.EndBlock()
			return nil, &buildStageError{Stage: stageLifecycle, Err: herr}
		}
	}
	if err := sp.ProcessBlockLifecycle(ctx.height, ctx.timestamp); err != nil {
		sp.EndBlock()
		return nil, &buildStageError{Stage: stageLifecycle, Err: err}
	}
	if !ctx.skipEvidence {
		if hooks != nil && hooks.evidence != nil {
			if herr := hooks.evidence(sp, res.Kept); herr != nil {
				sp.EndBlock()
				return nil, &buildStageError{Stage: stageEvidence, Err: herr}
			}
		}
		if err := n.processPendingEvidenceForState(sp, ctx.height); err != nil {
			sp.EndBlock()
			return nil, &buildStageError{Stage: stageEvidence, Err: err}
		}
	}
	sp.FinalizeBlock()
	res.State = sp
	return res, nil
}

// builtRecord remembers the transactions of the block this node most recently
// sealed, so a rejection of that block can be attributed to a transaction.
type builtRecord struct {
	height uint64
	txs    []*types.Transaction
}

// seal turns a clean wave into the block CreateBlock returns.
func (n *Node) seal(ctx *buildCtx, w *waveResult) (*types.Block, error) {
	defer w.State.EndBlock()

	txRoot, err := ComputeTxRoot(w.Kept)
	if err != nil {
		return nil, fmt.Errorf("compute tx root: %w", err)
	}
	header := &types.BlockHeader{
		Height:             ctx.height,
		Timestamp:          ctx.timestamp,
		PrevHash:           ctx.prevHash,
		Validator:          ctx.validator,
		ExecutionGraphRoot: w.GraphRoot,
		TxRoot:             txRoot,
		StateRoot:          w.State.PendingRoot().Bytes(),
	}
	block := types.NewBlock(header, w.Kept)
	if hash, hashErr := header.Hash(); hashErr == nil {
		n.stateMu.Lock()
		n.selfProposedHash = hash
		n.stateMu.Unlock()
	}
	ctx.included = w.Kept
	n.lastBuilt.Store(&builtRecord{height: ctx.height, txs: w.Kept})
	return block, nil
}

// ---- recording -------------------------------------------------------------

// logTxFailure logs at most maxPerTxLogLines per build at warn level; the rest
// are only counted (the aggregated line reports totals and top error classes).
func (n *Node) logTxFailure(ctx *buildCtx, f txFailure, key string) {
	level := slog.LevelDebug
	if ctx.stats.perTxLogged < maxPerTxLogLines {
		ctx.stats.perTxLogged++
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "transaction excluded from block proposal",
		slog.Uint64("height", ctx.height),
		slog.String("txType", types.TxTypeName(f.Tx.Type)),
		slog.String("key", truncateText(key, 24)),
		slog.String("disposition", dispositionLabel(f.Disposition)),
		slog.String("reason", sanitizeLogText(f.Err.Error())),
	)
}

// recordWaveOutcome applies the verdicts of a non-clean wave to the mempool
// and the strike book. Each transaction is recorded at most once per build.
//
//   - Prune: dropped from the mempool immediately (unchanged behaviour).
//   - Skip: a designed-transient cause. No strike; it stays resident and gets
//     a height backoff after repeated skips.
//   - Quarantine / Nondeterministic: a strike; at N strikes (or at once for a
//     contained panic) a solo dry run decides between eviction and reset.
func (n *Node) recordWaveOutcome(ctx *buildCtx, w *waveResult) {
	metrics := observability.Mempool()
	var prunes []*types.Transaction
	var confirm []txFailure
	now := ctx.now()

	for _, f := range w.Failures {
		key := ctx.keyOf(f.Tx)
		if key == "" {
			continue
		}
		if _, seen := ctx.failedKeys[key]; seen {
			continue
		}
		ctx.failedKeys[key] = struct{}{}
		ctx.noteErrorClass(f.Err)
		metrics.RecordTxFailure(dispositionLabel(f.Disposition))
		n.logTxFailure(ctx, f, key)

		switch f.Disposition {
		case proposalDispositionPrune:
			ctx.stats.pruned++
			ctx.markPruned(f.Tx, key)
			prunes = append(prunes, f.Tx)
		case proposalDispositionSkip:
			ctx.stats.skipped++
			n.poison.noteSkip(key, ctx.height, now, f.Err.Error())
		default: // quarantine, nondeterministic
			if f.Disposition == proposalDispositionNondeterministic {
				ctx.stats.nondeterministic++
				n.noteNondeterminism(f.Tx.Type, now)
			} else {
				ctx.stats.quarantined++
			}
			if f.Panicked {
				ctx.stats.panics++
			}
			if errors.Is(f.Err, errSlowTx) {
				ctx.stats.slow++
			}
			strikes := n.poison.noteStrike(key, ctx.height, now, f.Err.Error())
			if f.Panicked || strikes >= ctx.cfg.Strikes {
				confirm = append(confirm, f)
			}
		}
	}

	if len(prunes) > 0 {
		n.dropTransactionsFromMempool(prunes)
		for range prunes {
			metrics.RecordEviction("classified_prune")
		}
	}
	for _, f := range confirm {
		n.confirmAndEvict(ctx, f)
	}
	metrics.SetStrikeRecords(n.poison.size())
}

// soloOutcome is the result of applying one transaction alone against
// committed state.
type soloOutcome int

const (
	soloPassed soloOutcome = iota // applied cleanly on its own
	soloFailed                    // failed (or read the clock, or was slow) on its own
	soloInfra                     // an infrastructure error: says nothing about the tx
)

// soloDryRun applies tx alone against a fresh copy of committed state, with
// the same guards a wave applies. It is the confirmation step that protects
// innocent transactions: only a transaction that fails even on its own is
// evicted for accumulating strikes, so an attacker who arranges for a valid
// transaction to lose a same-block conflict cannot get it evicted that way.
func (n *Node) soloDryRun(ctx *buildCtx, tx *types.Transaction) soloOutcome {
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	sp, err := n.freshBlockStateLocked(ctx)
	if err != nil {
		return soloInfra
	}
	defer sp.EndBlock()
	reads := installClockGuard(sp, ctx.cfg.AllowClockDependent)
	started := ctx.now()
	err = n.applyTx(n.currentHooks(), sp, -1, 0, tx)
	took := ctx.now().Sub(started)
	switch {
	case err != nil && isInfrastructureError(err):
		return soloInfra
	case err != nil:
		return soloFailed
	case clockReads(reads) != 0:
		return soloFailed
	case ctx.cfg.SlowTx > 0 && took > ctx.cfg.SlowTx:
		return soloFailed
	}
	return soloPassed
}

// confirmAndEvict decides the fate of a transaction that reached the strike
// limit (or panicked): evict it if it fails alone, otherwise reset its strikes
// and treat it as a designed-transient skip (backoff and TTL still apply).
func (n *Node) confirmAndEvict(ctx *buildCtx, f txFailure) {
	if ctx.stats.evicted >= ctx.cfg.MaxEvictions {
		return // leftovers are retried on their next failure
	}
	if ctx.cfg.Budget > 0 && float64(ctx.elapsed()) > sdcBudgetShare*float64(ctx.cfg.Budget) {
		return
	}
	key := ctx.keyOf(f.Tx)
	switch n.soloDryRun(ctx, f.Tx) {
	case soloFailed:
		n.evictFromMempool(ctx, f.Tx, "strikes")
	case soloPassed:
		n.poison.resetStrikes(key)
		n.poison.noteSkip(key, ctx.height, ctx.now(), "lost a same-block conflict; passes alone")
	}
}

// evictFromMempool removes tx from the local mempool and forgets its history.
func (n *Node) evictFromMempool(ctx *buildCtx, tx *types.Transaction, reason string) {
	key := ctx.keyOf(tx)
	n.dropTransactionsFromMempool([]*types.Transaction{tx})
	ctx.markPruned(tx, key)
	ctx.stats.evicted++
	observability.Mempool().RecordEviction(reason)
	level := slog.LevelDebug
	if ctx.stats.perTxLogged < maxPerTxLogLines {
		ctx.stats.perTxLogged++
		level = slog.LevelWarn
	}
	slog.Log(context.Background(), level, "transaction evicted from the local mempool",
		slog.Uint64("height", ctx.height),
		slog.String("txType", types.TxTypeName(tx.Type)),
		slog.String("key", truncateText(key, 24)),
		slog.String("reason", reason),
	)
}

// ---- completion ------------------------------------------------------------

// finishBuild runs once per CreateBlock, on every return path. It reconciles
// the in-flight marks GetMempool set and reports what happened.
//
// Every candidate that is neither in the returned block nor evicted is
// released, so a fallback (or a clamp, or an error) can never leave a
// transaction permanently hidden from GetMempool -- with the empty-block
// fallback in place that would otherwise turn into silent censorship. The
// included transactions stay marked until commit, a failed round's requeue, or
// lease expiry releases them.
func (n *Node) finishBuild(ctx *buildCtx, block *types.Block, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("LIVENESS: panic while finishing block build", slog.String("panic", sanitizeLogText(fmt.Sprint(r))))
		}
	}()

	included := make(map[string]struct{})
	var includedKeys []string
	if block != nil {
		for _, tx := range block.Transactions {
			if key := ctx.keyOf(tx); key != "" {
				included[key] = struct{}{}
				includedKeys = append(includedKeys, key)
			}
		}
	}
	// Only the window the caller's GetMempool call could have leased matters.
	window := ctx.input
	if maxTxs := n.globalConfigSnapshot().Blocks.MaxTxs; maxTxs > 0 && int64(len(window)) > maxTxs {
		window = window[:maxTxs]
	}
	var release []string
	for _, tx := range window {
		key := ctx.keyOf(tx)
		if key == "" {
			continue
		}
		if _, ok := included[key]; ok {
			continue
		}
		if _, ok := ctx.prunedKeys[key]; ok {
			continue
		}
		release = append(release, key)
	}
	n.releaseInflightKeys(release)
	// A transaction that applied cleanly in the final wave has no failure
	// history worth keeping.
	n.poison.forgetAll(includedKeys)

	elapsed := ctx.elapsed()
	observability.Consensus().ObserveBuild(elapsed, ctx.waves)
	observability.Mempool().SetStrikeRecords(n.poison.size())
	observability.Mempool().RecordStrikeOverflowEvictions(n.poison.takeOverflow())

	if err == nil && !ctx.fellBack {
		n.noteBuildSucceeded(ctx)
	}
	n.logContainment(ctx, block, err, elapsed)
}

func topErrorClasses(classes map[string]int, limit int) []string {
	type kv struct {
		text  string
		count int
	}
	all := make([]kv, 0, len(classes))
	for text, count := range classes {
		all = append(all, kv{text, count})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].count != all[j].count {
			return all[i].count > all[j].count
		}
		return all[i].text < all[j].text
	})
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]string, len(all))
	for i, item := range all {
		out[i] = fmt.Sprintf("%dx %s", item.count, item.text)
	}
	return out
}

// logContainment emits one aggregated line per build, and only when something
// other than a plain success happened.
func (n *Node) logContainment(ctx *buildCtx, block *types.Block, err error, elapsed time.Duration) {
	s := &ctx.stats
	if s.total() == 0 && s.panics == 0 && !ctx.fellBack && err == nil {
		return
	}
	slog.Warn("proposal containment",
		slog.Uint64("height", ctx.height),
		slog.Int("waves", ctx.waves),
		slog.Int("offered", len(ctx.input)),
		slog.Int("included", len(ctx.included)),
		slog.Int("pruned", s.pruned),
		slog.Int("skipped", s.skipped),
		slog.Int("quarantined", s.quarantined),
		slog.Int("nondeterministic", s.nondeterministic),
		slog.Int("evicted", s.evicted),
		slog.Int("panics", s.panics),
		slog.Int("slow", s.slow),
		slog.Int("denied", s.denied),
		slog.Bool("empty_fallback", ctx.fellBack),
		slog.Bool("block_built", block != nil),
		slog.Int64("duration_ms", elapsed.Milliseconds()),
		slog.Any("top_errors", topErrorClasses(s.errClasses, 3)),
	)
}

// ---- isolation -------------------------------------------------------------

// maybeStartIsolation starts asynchronous isolation of the candidates of the
// build that just failed, unless one is already running. It never blocks the
// build that triggered it.
func (n *Node) maybeStartIsolation(ctx *buildCtx) {
	if len(ctx.window) == 0 {
		return
	}
	if !n.isolating.CompareAndSwap(false, true) {
		return
	}
	cands := append([]*types.Transaction(nil), ctx.window...)
	go func() {
		defer n.isolating.Store(false)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("LIVENESS: panic in transaction isolation", slog.String("panic", sanitizeLogText(fmt.Sprint(r))))
			}
		}()
		n.isolate(cands)
	}()
}

// isolate looks for the transactions responsible for repeated whole-block
// build failures, in two passes, and evicts them from the local mempool.
//
//  1. Solo pass: every candidate is applied alone against committed state;
//     one that fails, panics, reads the clock or is slow is evicted.
//  2. Joint pass: if the survivors together still fail in lifecycle
//     processing while an empty block builds fine, the smallest failing prefix
//     is bisected out and its last transaction evicted, up to
//     maxIsolationCulprits times. The evidence stage is not run by trials
//     (see buildCtx.skipEvidence).
//
// No lock is held across builds: each trial takes n.stateMu only for its own
// duration. A failure that persists on the empty block is not attributable to
// any transaction and is left for the watchdog to report.
func (n *Node) isolate(cands []*types.Transaction) {
	ctx := n.newBuildCtx(nil)
	ctx.deadline = ctx.started.Add(ctx.cfg.IsolationBudget)
	ctx.skipEvidence = true

	remaining := make([]*types.Transaction, 0, len(cands))
	for i, tx := range cands {
		if ctx.expired() {
			remaining = append(remaining, cands[i:]...)
			break
		}
		switch n.soloDryRun(ctx, tx) {
		case soloFailed:
			n.evictFromMempool(ctx, tx, "isolation")
		case soloInfra:
			return
		default:
			remaining = append(remaining, tx)
		}
	}
	n.isolateJoint(ctx, remaining)
	observability.Mempool().SetStrikeRecords(n.poison.size())
	if ctx.stats.evicted > 0 {
		slog.Warn("transaction isolation evicted candidates",
			slog.Uint64("height", ctx.height),
			slog.Int("evicted", ctx.stats.evicted),
			slog.Int("examined", len(cands)),
		)
	}
}

// trialStageFails runs cands through a complete wave (lifecycle and evidence
// included, nothing sealed) and reports whether it failed in a stage that is
// not attributable to a single transaction. infra is true when the trial could
// not be evaluated.
func (n *Node) trialStageFails(ctx *buildCtx, cands []*types.Transaction) (fails, infra bool) {
	w, err := n.executeWave(ctx, 0, cands)
	if err != nil {
		var stageErr *buildStageError
		if errors.As(err, &stageErr) && (stageErr.Stage == stageLifecycle || stageErr.Stage == stageEvidence) {
			return true, false
		}
		return false, true
	}
	if w != nil && w.State != nil {
		w.State.EndBlock()
	}
	return false, false
}

func (n *Node) isolateJoint(ctx *buildCtx, remaining []*types.Transaction) {
	if len(remaining) == 0 {
		return
	}
	if fails, infra := n.trialStageFails(ctx, nil); fails || infra {
		return // fails even when empty: not attributable to a transaction
	}
	for culprits := 0; culprits < maxIsolationCulprits && len(remaining) > 0 && !ctx.expired(); culprits++ {
		fails, infra := n.trialStageFails(ctx, remaining)
		if infra || !fails {
			return
		}
		lo, hi := 1, len(remaining)
		for lo < hi {
			if ctx.expired() {
				return
			}
			mid := (lo + hi) / 2
			midFails, midInfra := n.trialStageFails(ctx, remaining[:mid])
			if midInfra {
				return
			}
			if midFails {
				hi = mid
			} else {
				lo = mid + 1
			}
		}
		// Confirm the boundary before evicting: the prefix ending at the
		// culprit must fail and the one before it must not.
		if atFails, atInfra := n.trialStageFails(ctx, remaining[:lo]); atInfra || !atFails {
			return
		}
		if beforeFails, beforeInfra := n.trialStageFails(ctx, remaining[:lo-1]); beforeInfra || beforeFails {
			return
		}
		culprit := remaining[lo-1]
		n.evictFromMempool(ctx, culprit, "isolation")
		remaining = append(append([]*types.Transaction(nil), remaining[:lo-1]...), remaining[lo:]...)
	}
}
