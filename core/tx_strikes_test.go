package core

import (
	"fmt"
	"math/big"
	"runtime"
	"testing"
	"time"

	"nhbchain/core/types"
)

// ---- the strike book as a data structure ------------------------------------

func TestStrikeBookNeverExceedsItsCapacity(t *testing.T) {
	const capacity = 8192
	book := newTxStrikeBook(capacity)
	now := time.Unix(1_800_000_000, 0)

	var before runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)

	// 3x the cap of unique failing keys.
	for i := 0; i < 3*capacity; i++ {
		book.noteStrike(fmt.Sprintf("key-%08d", i), uint64(i), now, "boom")
		if size := book.size(); size > capacity {
			t.Fatalf("after %d inserts the book holds %d records, cap %d", i+1, size, capacity)
		}
	}
	if got := book.size(); got != capacity {
		t.Fatalf("expected the book to be exactly full, got %d", got)
	}
	if got, want := book.overflowCount(), uint64(3*capacity-capacity); got != want {
		t.Fatalf("overflow evictions = %d, want %d", got, want)
	}
	if got := book.takeOverflow(); got != uint64(2*capacity) {
		t.Fatalf("takeOverflow = %d, want %d", got, 2*capacity)
	}
	if got := book.takeOverflow(); got != 0 {
		t.Fatalf("takeOverflow must report only new evictions, got %d", got)
	}

	// The oldest keys are the ones that were dropped; the newest survive.
	if _, ok := book.peek("key-00000000"); ok {
		t.Fatalf("the oldest record should have been evicted")
	}
	if _, ok := book.peek(fmt.Sprintf("key-%08d", 3*capacity-1)); !ok {
		t.Fatalf("the newest record should still be present")
	}

	// Memory stays in the low megabytes (the design budget is about 2 MB).
	var after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&after)
	if growth := int64(after.HeapAlloc) - int64(before.HeapAlloc); growth > 8<<20 {
		t.Fatalf("strike book grew the heap by %d bytes for %d records; expected under 8 MiB", growth, capacity)
	}
}

func TestStrikeBookBackoffSchedule(t *testing.T) {
	book := newTxStrikeBook(16)
	now := time.Unix(1_800_000_000, 0)

	// Quarantine strikes: eligible again on the very next height after the
	// first, held back one further height after the second.
	if n := book.noteStrike("k", 10, now, "e"); n != 1 {
		t.Fatalf("first strike count = %d", n)
	}
	if v, _ := book.gate("k", 10, now, 0, 0); v != gateBackoff {
		t.Fatalf("a transaction that just failed must be held back at the same height")
	}
	if v, _ := book.gate("k", 11, now, 0, 0); v != gateOK {
		t.Fatalf("after one strike the transaction is offered again at the next height")
	}
	if n := book.noteStrike("k", 11, now, "e"); n != 2 {
		t.Fatalf("second strike count = %d", n)
	}
	if v, _ := book.gate("k", 12, now, 0, 0); v != gateBackoff {
		t.Fatalf("after the second strike the transaction sits out one more height")
	}
	if v, _ := book.gate("k", 13, now, 0, 0); v != gateOK {
		t.Fatalf("after the second strike the transaction is offered again two heights later")
	}

	// Designed-transient skips: no delay for the first two, then 2, 4, 8, ...
	// capped at 32.
	want := []uint64{0, 0, 2, 4, 8, 16, 32, 32, 32}
	for i, delay := range want {
		n := book.noteSkip("s", 100, now, "paused")
		if n != i+1 {
			t.Fatalf("skip %d: count = %d", i+1, n)
		}
		rec, ok := book.peek("s")
		if !ok || rec.retryAfter != 100+delay {
			t.Fatalf("skip %d: retryAfter = %d, want %d", i+1, rec.retryAfter, 100+delay)
		}
		if rec.strikes != 0 {
			t.Fatalf("skip %d must not add a strike", i+1)
		}
	}
}

func TestStrikeBookTTLs(t *testing.T) {
	book := newTxStrikeBook(16)
	start := time.Unix(1_800_000_000, 0)
	const skipTTL, absTTL = 2 * time.Hour, 24 * time.Hour

	// A transaction with no record is never held back or expired.
	if v, _ := book.gate("clean", 5, start.Add(1000*time.Hour), skipTTL, absTTL); v != gateOK {
		t.Fatalf("a transaction with no failure history must always pass the gate")
	}

	// Skip-class: expires once its first skip is older than the skip TTL.
	book.noteSkip("skipped", 1, start, "paused")
	if v, _ := book.gate("skipped", 5, start.Add(skipTTL-time.Minute), skipTTL, absTTL); v != gateOK {
		t.Fatalf("a skipped transaction inside the skip TTL is offered")
	}
	if v, reason := book.gate("skipped", 5, start.Add(skipTTL+time.Minute), skipTTL, absTTL); v != gateExpired || reason != "skip_ttl" {
		t.Fatalf("a skipped transaction past the skip TTL expires, got verdict=%v reason=%q", v, reason)
	}

	// A struck transaction is not covered by the skip TTL, but is by the
	// absolute one.
	book.noteStrike("struck", 1, start, "boom")
	if v, _ := book.gate("struck", 10, start.Add(skipTTL+time.Hour), skipTTL, absTTL); v != gateOK {
		t.Fatalf("a struck transaction is governed by the absolute TTL, not the skip TTL")
	}
	if v, reason := book.gate("struck", 10, start.Add(absTTL+time.Minute), skipTTL, absTTL); v != gateExpired || reason != "absolute_ttl" {
		t.Fatalf("a struck transaction past the absolute TTL expires, got verdict=%v reason=%q", v, reason)
	}
}

func TestStrikeBookForgetResetAndSweep(t *testing.T) {
	book := newTxStrikeBook(16)
	now := time.Unix(1_800_000_000, 0)
	for _, key := range []string{"a", "b", "c", "d"} {
		book.noteStrike(key, 1, now, "e")
	}
	book.forget("a")
	if _, ok := book.peek("a"); ok {
		t.Fatalf("forget must drop the record")
	}
	book.forgetAll([]string{"b", "missing"})
	if book.size() != 2 {
		t.Fatalf("size after forgetAll = %d, want 2", book.size())
	}
	book.resetStrikes("c")
	if rec, _ := book.peek("c"); rec.strikes != 0 {
		t.Fatalf("resetStrikes must clear the strike count, got %d", rec.strikes)
	}
	if removed := book.sweep(map[string]struct{}{"d": {}}); removed != 1 {
		t.Fatalf("sweep removed %d records, want 1", removed)
	}
	if _, ok := book.peek("d"); !ok || book.size() != 1 {
		t.Fatalf("sweep must keep exactly the live record")
	}
	// The book is safe on a nil receiver: a Node built without NewNode has none.
	var nilBook *txStrikeBook
	nilBook.noteStrike("x", 1, now, "e")
	nilBook.forget("x")
	if v, _ := nilBook.gate("x", 1, now, 0, 0); v != gateOK || nilBook.size() != 0 {
		t.Fatalf("a nil strike book must behave as an empty one")
	}
}

// ---- the strike book wired into the node -----------------------------------

// A poison record that the LRU dropped comes back at strike 1, and CreateBlock
// still succeeds throughout: containment never depends on the book.
func TestStrikeBookOverflowNeverBreaksContainment(t *testing.T) {
	node := newTestNode(t)
	node.poison = newTxStrikeBook(4)
	honest := livenessKey(t)
	livenessFund(t, node, honest.PubKey().Address().Bytes(), 1_000_000_000, 0)

	// Ten distinct poison transactions (unfunded senders, unaffordable
	// transfers), struck one build at a time so the 4-entry book overflows.
	poisons := make([]*types.Transaction, 0, 10)
	for i := 0; i < 10; i++ {
		poisons = append(poisons, livenessSign(t, livenessKey(t), types.TxTypeTransfer, 0, nil, make([]byte, 20), 1_000_000))
	}
	transfer := livenessTransfer(t, honest, 0)
	for i, poison := range poisons {
		block, err := node.CreateBlock([]*types.Transaction{poison, transfer})
		if err != nil {
			t.Fatalf("poison %d: CreateBlock failed with a full strike book: %v", i, err)
		}
		if !livenessBlockContains(t, block, transfer) {
			t.Fatalf("poison %d: the valid transfer must still be included", i)
		}
		if size := livenessStrikeRecords(node); size > 4 {
			t.Fatalf("poison %d: strike book holds %d records, cap 4", i, size)
		}
	}
	if node.poison.overflowCount() == 0 {
		t.Fatalf("expected the small strike book to have overflowed")
	}

	// The very first poison was pushed out of the book; striking it again starts
	// its count over at 1.
	if _, ok := livenessStrikes(t, node, poisons[0]); ok {
		t.Fatalf("the oldest poison should have been evicted from the strike book")
	}
	if _, err := node.CreateBlock([]*types.Transaction{poisons[0], transfer}); err != nil {
		t.Fatalf("CreateBlock after re-striking: %v", err)
	}
	rec, ok := livenessStrikes(t, node, poisons[0])
	if !ok || rec.strikes != 1 {
		t.Fatalf("an evicted poison must re-enter at strike 1, got %+v ok=%v", rec, ok)
	}
}

// Every way a transaction can leave the mempool must drop its strike record.
func TestStrikeRecordsAreForgottenWhenATransactionLeavesTheMempool(t *testing.T) {
	newStruck := func(t *testing.T, node *Node, value int64) *types.Transaction {
		t.Helper()
		tx := livenessSign(t, livenessKey(t), types.TxTypeTransfer, 0, nil, make([]byte, 20), value)
		livenessInject(node, tx)
		key, err := transactionKey(tx)
		if err != nil {
			t.Fatalf("key: %v", err)
		}
		node.poison.noteStrike(key, 1, time.Now(), "boom")
		if _, ok := livenessStrikes(t, node, tx); !ok {
			t.Fatalf("test setup: the strike was not recorded")
		}
		return tx
	}

	t.Run("commit", func(t *testing.T) {
		node := newTestNode(t)
		tx := newStruck(t, node, 1)
		node.markTransactionsCommitted([]*types.Transaction{tx})
		if _, ok := livenessStrikes(t, node, tx); ok {
			t.Fatalf("a committed transaction must be forgotten")
		}
	})
	t.Run("drop", func(t *testing.T) {
		node := newTestNode(t)
		tx := newStruck(t, node, 2)
		node.dropTransactionsFromMempool([]*types.Transaction{tx})
		if _, ok := livenessStrikes(t, node, tx); ok {
			t.Fatalf("a dropped transaction must be forgotten")
		}
	})
	t.Run("replace-by-fee", func(t *testing.T) {
		node := newTestNode(t)
		node.SetTransactionSimulationEnabled(false)
		key := livenessKey(t)
		livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000_000_000, 0)
		to := make([]byte, 20)
		cheap := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeTransfer, To: to, Value: big.NewInt(1), GasLimit: 21_000, GasPrice: big.NewInt(1)}
		if err := cheap.Sign(key.PrivateKey); err != nil {
			t.Fatalf("sign: %v", err)
		}
		if err := node.AddTransaction(cheap); err != nil {
			t.Fatalf("add: %v", err)
		}
		cheapKey, _ := transactionKey(cheap)
		node.poison.noteStrike(cheapKey, 1, time.Now(), "boom")
		rich := &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeTransfer, To: to, Value: big.NewInt(1), GasLimit: 21_000, GasPrice: big.NewInt(9)}
		if err := rich.Sign(key.PrivateKey); err != nil {
			t.Fatalf("sign: %v", err)
		}
		if err := node.AddTransaction(rich); err != nil {
			t.Fatalf("replace: %v", err)
		}
		if _, ok := livenessStrikes(t, node, cheap); ok {
			t.Fatalf("a replaced transaction must be forgotten")
		}
	})
	t.Run("mempool-trim", func(t *testing.T) {
		node := newTestNode(t)
		first := newStruck(t, node, 3)
		newStruck(t, node, 4)
		newStruck(t, node, 5)
		node.SetMempoolLimit(2) // trims the OLDEST entry
		if _, ok := livenessStrikes(t, node, first); ok {
			t.Fatalf("a transaction trimmed out of the mempool must be forgotten")
		}
	})
	t.Run("sweep", func(t *testing.T) {
		node := newTestNode(t)
		resident := newStruck(t, node, 6)
		// A record whose transaction is no longer resident (a path that forgot to
		// forget): the periodic sweep inside GetMempool removes it.
		node.poison.noteStrike("orphan-record", 1, time.Now(), "boom")
		for i := 0; i < strikeBookSweepInterval; i++ {
			node.GetMempool()
		}
		if _, ok := node.poison.peek("orphan-record"); ok {
			t.Fatalf("the sweep must remove records for transactions no longer resident")
		}
		if _, ok := livenessStrikes(t, node, resident); !ok {
			t.Fatalf("the sweep must keep records for resident transactions")
		}
	})
}

// GetMempool holds a just-failed transaction back for the rest of the height
// and offers it again at the next one, and never lets a held-back transaction
// take a slot in the MaxTxs window.
func TestGetMempoolHonoursBackoffAndDoesNotLetHeldBackTransactionsDisplaceOthers(t *testing.T) {
	node := newTestNode(t)
	cfg := node.globalConfigSnapshot()
	cfg.Blocks.MaxTxs = 3
	if err := node.SetGlobalConfig(cfg); err != nil {
		t.Fatalf("set config: %v", err)
	}

	txs := make([]*types.Transaction, 0, 6)
	for i := 0; i < 6; i++ {
		tx := livenessSign(t, livenessKey(t), types.TxTypeTransfer, 0, nil, make([]byte, 20), int64(i+1))
		txs = append(txs, tx)
	}
	livenessInject(node, txs...)

	// The first three are held back (struck at height 1, still building it).
	for _, tx := range txs[:3] {
		key, _ := transactionKey(tx)
		node.poison.noteStrike(key, 1, time.Now(), "boom")
	}
	offered := node.GetMempool()
	if len(offered) != 3 {
		t.Fatalf("expected the 3 eligible transactions, got %d", len(offered))
	}
	for _, tx := range offered {
		for _, held := range txs[:3] {
			if tx == held {
				t.Fatalf("a held-back transaction was offered")
			}
		}
	}

	// Once the offered ones are released and the height advances, the held-back
	// ones come back.
	node.RequeueTransactions(offered)
	block, err := node.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit: %v", err)
	}
	// GetMempool returns the whole eligible list (all six now) and leases only
	// the first MaxTxs of it -- and those first three are the transactions that
	// were held back, which have earned their turn back.
	again := node.GetMempool()
	if len(again) != 6 {
		t.Fatalf("expected every transaction eligible at the next height, got %d", len(again))
	}
	for i, held := range txs[:3] {
		if again[i] != held {
			t.Fatalf("expected the previously held-back transaction %d to head the window", i)
		}
	}
}

func TestGetMempoolLeasesOnlyTheProposalWindowAndLeasesLapse(t *testing.T) {
	node := newTestNode(t)
	cfg := node.globalConfigSnapshot()
	cfg.Blocks.MaxTxs = 2
	if err := node.SetGlobalConfig(cfg); err != nil {
		t.Fatalf("set config: %v", err)
	}
	clock := time.Unix(1_800_000_000, 0)
	node.setLocalClock(func() time.Time { return clock })

	for i := 0; i < 5; i++ {
		livenessInject(node, livenessSign(t, livenessKey(t), types.TxTypeTransfer, 0, nil, make([]byte, 20), int64(i+1)))
	}

	first := node.GetMempool()
	if len(first) != 5 {
		t.Fatalf("the full ordered list is still returned, got %d", len(first))
	}
	if got := livenessInFlight(node); got != 2 {
		t.Fatalf("only the MaxTxs window is leased, got %d leases", got)
	}
	// The next call skips the leased pair and offers (and leases) the next window.
	second := node.GetMempool()
	if len(second) != 3 {
		t.Fatalf("expected the 3 unleased transactions, got %d", len(second))
	}
	if got := livenessInFlight(node); got != 4 {
		t.Fatalf("expected 4 leases, got %d", got)
	}

	// Nothing ever finished those proposals. After the lease lapses everything
	// is offered again -- no transaction stays hidden forever.
	clock = clock.Add(defaultInflightLease + time.Second)
	third := node.GetMempool()
	if len(third) != 5 {
		t.Fatalf("expected every transaction offered again after the leases lapsed, got %d", len(third))
	}
}

// Family (b)/(h) style eviction by age: a transaction that has only ever been
// skipped is evicted by the skip TTL, one that keeps failing by the absolute
// TTL, and both are logged and counted.
func TestGetMempoolEvictsExpiredFailureHistories(t *testing.T) {
	node := newTestNode(t)
	clock := time.Unix(1_800_000_000, 0)
	node.setLocalClock(func() time.Time { return clock })

	skipped := livenessSign(t, livenessKey(t), types.TxTypeTransfer, 0, nil, make([]byte, 20), 1)
	struck := livenessSign(t, livenessKey(t), types.TxTypeTransfer, 0, nil, make([]byte, 20), 2)
	fresh := livenessSign(t, livenessKey(t), types.TxTypeTransfer, 0, nil, make([]byte, 20), 3)
	livenessInject(node, skipped, struck, fresh)
	skippedKey, _ := transactionKey(skipped)
	struckKey, _ := transactionKey(struck)
	node.poison.noteSkip(skippedKey, 1, clock, "paused")
	node.poison.noteStrike(struckKey, 1, clock, "boom")

	before := livenessMetricValue(t, "nhb_mempool_evictions_total", map[string]string{"reason": "ttl"})

	clock = clock.Add(defaultSkipTTL + time.Minute)
	offered := node.GetMempool()
	if livenessResident(t, node, skipped) {
		t.Fatalf("a transaction skipped for longer than the skip TTL must be evicted")
	}
	if !livenessResident(t, node, struck) || !livenessResident(t, node, fresh) {
		t.Fatalf("only the skip-class transaction expires at the skip TTL")
	}
	// The struck one is also still inside its one-height backoff (it "just
	// failed" at the height being built), so only the fresh one is offered.
	if len(offered) != 1 || offered[0] != fresh {
		t.Fatalf("expected only the transaction with no failure history to be offered, got %d", len(offered))
	}

	node.RequeueTransactions(offered)
	clock = clock.Add(defaultAbsoluteTTL)
	node.GetMempool()
	if livenessResident(t, node, struck) {
		t.Fatalf("a transaction failing for longer than the absolute TTL must be evicted")
	}
	if !livenessResident(t, node, fresh) {
		t.Fatalf("a transaction with no failure history is never evicted by age")
	}
	if got := livenessMetricValue(t, "nhb_mempool_evictions_total", map[string]string{"reason": "ttl"}); got-before != 2 {
		t.Fatalf("expected 2 ttl evictions to be counted, got %v", got-before)
	}
}

// The operator deny-list keeps a type out of every proposal without striking or
// evicting anything.
func TestExcludedTransactionTypesStayResidentWithoutStrikes(t *testing.T) {
	node := newTestNode(t)
	node.setBuildConfig(func(cfg *buildConfig) {
		cfg.ExcludeTypes = map[types.TxType]struct{}{types.TxTypeTransferZNHB: {}}
	})
	sender := livenessKey(t)
	livenessFund(t, node, sender.PubKey().Address().Bytes(), 1_000_000, 1_000_000)
	excluded := livenessSign(t, sender, types.TxTypeTransferZNHB, 0, nil, make([]byte, 20), 1)
	other := livenessTransfer(t, livenessKey(t), 0)
	livenessInject(node, excluded, other)

	offered := node.GetMempool()
	for _, tx := range offered {
		if tx == excluded {
			t.Fatalf("GetMempool offered an operator-excluded type")
		}
	}
	// A caller that supplies the transaction directly (CreateBlock over gRPC)
	// still cannot get it into a block.
	block, err := node.CreateBlock([]*types.Transaction{excluded})
	if err != nil {
		t.Fatalf("CreateBlock: %v", err)
	}
	if len(block.Transactions) != 0 {
		t.Fatalf("an excluded type must not be proposed, got %d transactions", len(block.Transactions))
	}
	if !livenessResident(t, node, excluded) {
		t.Fatalf("an excluded transaction stays resident")
	}
	if _, ok := livenessStrikes(t, node, excluded); ok {
		t.Fatalf("exclusion is a policy, not a failure: no strike may be recorded")
	}
}

func TestBuildConfigFromEnvironment(t *testing.T) {
	env := map[string]string{
		"NHB_PROPOSAL_BUILD_BUDGET_MS":  "750",
		"NHB_PROPOSAL_MAX_WAVES":        "4",
		"NHB_POISON_STRIKES":            "5",
		"NHB_POISON_SKIP_TTL":           "90m",
		"NHB_INFLIGHT_LEASE_SECS":       "45",
		"NHB_LIVENESS_STALL_SECS":       "120",
		"NHB_ALLOW_CLOCK_DEPENDENT_TXS": "true",
		"NHB_PROPOSER_EXCLUDE_TXTYPES":  "0x03, 0x05 ,bogus,300",
	}
	cfg, warnings := loadBuildConfig(defaultBuildConfig(), func(name string) string { return env[name] })
	if cfg.Budget != 750*time.Millisecond || !cfg.budgetPinned {
		t.Fatalf("budget = %v pinned=%v", cfg.Budget, cfg.budgetPinned)
	}
	if cfg.MaxWaves != 4 || cfg.Strikes != 5 || cfg.SkipTTL != 90*time.Minute ||
		cfg.InflightLease != 45*time.Second || cfg.StallAfter != 120*time.Second || !cfg.AllowClockDependent {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if _, ok := cfg.ExcludeTypes[types.TxTypeCreateEscrow]; !ok {
		t.Fatalf("0x03 must be excluded")
	}
	if _, ok := cfg.ExcludeTypes[types.TxTypeRefundEscrow]; !ok {
		t.Fatalf("0x05 must be excluded")
	}
	if len(cfg.ExcludeTypes) != 2 || len(warnings) != 2 {
		t.Fatalf("malformed entries are ignored with a warning each: types=%v warnings=%v", cfg.ExcludeTypes, warnings)
	}

	// Malformed scalars are ignored, never fatal.
	bad, warnings := loadBuildConfig(defaultBuildConfig(), func(name string) string {
		return map[string]string{"NHB_PROPOSAL_MAX_WAVES": "-3", "NHB_POISON_STRIKES": "many"}[name]
	})
	if bad.MaxWaves != defaultMaxWaves || bad.Strikes != defaultStrikeLimit || len(warnings) != 2 {
		t.Fatalf("malformed values must fall back to defaults with a warning: %+v %v", bad, warnings)
	}

	// SetProposalBuildBudget lowers the budget but is capped at the default and
	// yields to an explicit operator pin.
	node := newTestNode(t)
	node.SetProposalBuildBudget(400 * time.Millisecond)
	if got := node.buildConfigSnapshot().Budget; got != 400*time.Millisecond {
		t.Fatalf("budget after SetProposalBuildBudget = %v", got)
	}
	node.SetProposalBuildBudget(10 * time.Second)
	if got := node.buildConfigSnapshot().Budget; got != defaultBuildBudget {
		t.Fatalf("budget must be capped at %v, got %v", defaultBuildBudget, got)
	}
	node.setBuildConfig(func(c *buildConfig) { c.Budget = 5 * time.Second; c.budgetPinned = true })
	node.SetProposalBuildBudget(100 * time.Millisecond)
	if got := node.buildConfigSnapshot().Budget; got != 5*time.Second {
		t.Fatalf("an operator-pinned budget must not be overridden, got %v", got)
	}
}
