package core

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	"nhbchain/core/types"
)

// Performance and boundedness. The containment layer must not just be correct:
// it must return a block within a bound no matter how much poison is on offer,
// and it must not let poison starve the honest transactions queued behind it.

// perfRoundBound is the hard per-CreateBlock bound the tests enforce from
// outside the call (a goroutine plus a timeout), so a regression that
// reintroduces a hang fails the test instead of blocking the whole test binary.
// The design's worst case is roughly the build budget plus the prefix budget
// plus one empty-block build, about 1.3 s; two seconds leaves room for a slow
// CI machine.
const perfRoundBound = 2 * time.Second

type perfResult struct {
	block *types.Block
	err   error
	took  time.Duration
}

// createBlockBounded runs CreateBlock in a goroutine and fails the test if it
// does not return within timeout.
func createBlockBounded(t *testing.T, node *Node, txs []*types.Transaction, timeout time.Duration) perfResult {
	t.Helper()
	done := make(chan perfResult, 1)
	go func() {
		started := time.Now()
		block, err := node.CreateBlock(txs)
		done <- perfResult{block: block, err: err, took: time.Since(started)}
	}()
	select {
	case res := <-done:
		return res
	case <-time.After(timeout):
		t.Fatalf("CreateBlock did not return within %s for %d candidate transactions", timeout, len(txs))
		return perfResult{}
	}
}

// poisonTransactions builds n transactions from distinct signers that can never
// apply and are cheap to fail: half are unaffordable transfers, half release an
// escrow that does not exist.
func poisonTransactions(t *testing.T, n int) []*types.Transaction {
	t.Helper()
	out := make([]*types.Transaction, 0, n)
	to := make([]byte, 20)
	to[19] = 0x05
	for i := 0; i < n; i++ {
		key := livenessKey(t)
		if i%2 == 0 {
			out = append(out, livenessSign(t, key, types.TxTypeTransfer, 0, nil, to, 1_000_000))
			continue
		}
		garbage := bytes.Repeat([]byte{byte(i), byte(i >> 8), 0xEE, 0x11}, 8)
		out = append(out, livenessSign(t, key, types.TxTypeReleaseEscrow, 0, garbage, nil, 0))
	}
	return out
}

// 4000 poison transactions sit at the head of the mempool with 500 valid
// transfers queued behind them. Every round must return promptly and without
// error, the honest transfers must all be included within a bounded number of
// rounds, and the poison must drain. The failure history stays within the strike
// book's cap the whole time.
func TestPerfFloodOfPoisonNeverStallsBlockProduction(t *testing.T) {
	if testing.Short() {
		t.Skip("drains a 4000-transaction poison flood over dozens of blocks")
	}
	node := newTestNode(t)
	const poisonCount, validCount, maxRounds = 4000, 500, 90

	poison := poisonTransactions(t, poisonCount)
	valid := make([]*types.Transaction, 0, validCount)
	for i := 0; i < validCount; i++ {
		key := livenessKey(t)
		livenessFund(t, node, key.PubKey().Address().Bytes(), 1_000_000_000, 0)
		valid = append(valid, livenessTransfer(t, key, 0))
	}
	livenessInject(node, poison...)
	livenessInject(node, valid...)

	included := 0
	var slowest time.Duration
	rounds := 0
	for round := 1; round <= maxRounds; round++ {
		rounds = round
		offered := node.GetMempool()
		res := createBlockBounded(t, node, offered, 10*time.Second)
		if res.err != nil {
			t.Fatalf("round %d: CreateBlock failed while %d transactions were on offer: %v", round, len(offered), res.err)
		}
		if res.took > perfRoundBound {
			t.Fatalf("round %d: CreateBlock took %s, over the %s bound", round, res.took, perfRoundBound)
		}
		if res.took > slowest {
			slowest = res.took
		}
		for _, tx := range res.block.Transactions {
			for _, v := range valid {
				if tx == v {
					included++
				}
			}
		}
		if err := node.CommitBlock(res.block); err != nil {
			t.Fatalf("round %d: CommitBlock: %v", round, err)
		}
		if records := livenessStrikeRecords(node); records > defaultStrikeBookCapacity {
			t.Fatalf("round %d: strike book holds %d records, cap %d", round, records, defaultStrikeBookCapacity)
		}
		if included == validCount && node.MempoolSize() == 0 {
			break
		}
	}
	livenessWaitIsolation(t, node)

	if included != validCount {
		t.Fatalf("only %d of %d valid transfers were included after %d rounds", included, validCount, rounds)
	}
	if left := node.MempoolSize(); left != 0 {
		t.Fatalf("%d poison transactions are still resident after %d rounds", left, rounds)
	}
	t.Logf("drained %d poison transactions and included %d valid ones in %d rounds; slowest CreateBlock %s", poisonCount, validCount, rounds, slowest)
}

// 64 KiB payloads, up to the mempool's byte cap. Hashing, key derivation and
// RLP encoding of large payloads must not push a round past its bound.
func TestPerfLargePayloadPoisonStaysWithinTheRoundBound(t *testing.T) {
	node := newTestNode(t)
	honest := livenessKey(t)
	livenessFund(t, node, honest.PubKey().Address().Bytes(), 1_000_000_000, 0)
	maxBytes := node.globalConfigSnapshot().Mempool.MaxBytes

	payload := bytes.Repeat([]byte{0xAB}, 64*1024)
	var total int64
	count := 0
	for total < maxBytes && count < 300 {
		key := livenessKey(t)
		tx := livenessSign(t, key, types.TxTypeReleaseEscrow, 0, payload, nil, 0)
		livenessInject(node, tx)
		total += int64(len(payload))
		count++
	}
	transfer := livenessTransfer(t, honest, 0)
	livenessInject(node, transfer)

	for round := 1; round <= 12; round++ {
		res := createBlockBounded(t, node, node.GetMempool(), 10*time.Second)
		if res.err != nil {
			t.Fatalf("round %d: CreateBlock: %v", round, res.err)
		}
		if res.took > perfRoundBound {
			t.Fatalf("round %d: CreateBlock took %s with %d large poison transactions on offer", round, res.took, count)
		}
		if err := node.CommitBlock(res.block); err != nil {
			t.Fatalf("round %d: CommitBlock: %v", round, err)
		}
		if !livenessResident(t, node, transfer) {
			t.Logf("large-payload flood of %d transactions (%d MiB): valid transfer included by round %d", count, total>>20, round)
			return
		}
	}
	t.Fatalf("the valid transfer was still not included after 12 rounds")
}

// A wave chain: every wave excludes exactly one more transaction, so the wave
// cap is exhausted long before the candidate set converges. The clean prefix of
// the last wave is then re-verified and used, so the block still carries most of
// the honest transactions instead of collapsing to the empty block.
func TestPerfWaveChainExhaustsTheWaveCapAndUsesTheVerifiedPrefix(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 20)
	waveCap := node.buildConfigSnapshot().MaxWaves
	if waveCap != 6 {
		t.Fatalf("test assumes the default wave cap of 6, got %d", waveCap)
	}
	// In wave w (1-based) the transaction at position len-w fails, but only in
	// the exploratory waves; the verification wave over the prefix (wave 7)
	// applies everything. Excluding the tail one transaction at a time never
	// converges within six waves.
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		if wave >= 1 && wave <= waveCap && tx == txs[len(txs)-wave] {
			return errors.New("injected chain failure")
		}
		return nil
	})

	res := createBlockBounded(t, node, node.GetMempool(), 10*time.Second)
	if res.err != nil {
		t.Fatalf("CreateBlock: %v", res.err)
	}
	wantIncluded := len(txs) - waveCap
	if got := len(res.block.Transactions); got != wantIncluded {
		t.Fatalf("expected the verified prefix of %d transactions, got %d", wantIncluded, got)
	}
	if err := node.ValidateBlock(res.block); err != nil {
		t.Fatalf("the prefix block must validate: %v", err)
	}
	for i := 0; i < wantIncluded; i++ {
		if res.block.Transactions[i] != txs[i] {
			t.Fatalf("the prefix must keep the original order; position %d differs", i)
		}
	}
	// The six failed tail transactions are not lost: they carry a strike and
	// are released, not left in flight.
	for _, tx := range txs[wantIncluded:] {
		if !livenessResident(t, node, tx) {
			t.Fatalf("a transaction excluded by a wave must stay resident")
		}
	}
	if got := livenessInFlight(node); got != wantIncluded {
		t.Fatalf("expected only the %d included transactions leased, got %d", wantIncluded, got)
	}
}

// The wave chain again, when even the first transaction keeps failing: the last
// wave's clean prefix is empty and the block is the empty block, still not an
// error.
func TestPerfWaveChainWithNoCleanPrefixFallsBackToTheEmptyBlock(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 12)
	waveCap := node.buildConfigSnapshot().MaxWaves
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		if wave >= 1 && wave <= waveCap && tx == txs[wave-1] {
			return errors.New("injected chain failure at the head")
		}
		return nil
	})
	res := createBlockBounded(t, node, node.GetMempool(), 10*time.Second)
	if res.err != nil {
		t.Fatalf("CreateBlock: %v", res.err)
	}
	// Wave w excludes txs[w-1]; the last wave's first failure is at index 0, so
	// its clean prefix is empty and the empty block is built.
	if len(res.block.Transactions) != 0 {
		t.Fatalf("expected the empty block, got %d transactions", len(res.block.Transactions))
	}
	if err := node.ValidateBlock(res.block); err != nil {
		t.Fatalf("the empty fallback block must validate: %v", err)
	}
	if got := node.LivenessSnapshot().ConsecutiveBuildFailures; got != 1 {
		t.Fatalf("expected the exhausted build to be counted as a failure, got %d", got)
	}
}

// The build budget is enforced between transactions using an injectable clock,
// so the boundary cases are deterministic (no real sleeps).
func TestPerfBudgetIsEnforcedBetweenTransactions(t *testing.T) {
	t.Run("prefix fallback", func(t *testing.T) {
		node := newTestNode(t)
		txs := livenessFundedTransfers(t, node, 20)
		clock := time.Unix(1_800_000_000, 0)
		node.setLocalClock(func() time.Time { return clock })
		// Wave 1: every apply "takes" 100ms of the 1s budget, so the budget runs
		// out after 10 transactions with no failure yet. The verification wave
		// is quick, but the prefix is still cut to what its first-pass cost says
		// fits the prefix budget: with a 300ms prefix budget and a 70 percent
		// share, the transactions finishing at 100ms and 200ms fit and the one
		// finishing at 300ms does not.
		node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
			if wave == 1 {
				clock = clock.Add(100 * time.Millisecond)
			} else {
				clock = clock.Add(time.Millisecond)
			}
			return nil
		})
		res := createBlockBounded(t, node, node.GetMempool(), 10*time.Second)
		if res.err != nil {
			t.Fatalf("CreateBlock: %v", res.err)
		}
		included := len(res.block.Transactions)
		if included != 2 {
			t.Fatalf("expected the 2 leading transactions whose first-pass cost fits the prefix budget, got %d", included)
		}
		for i := 0; i < included; i++ {
			if res.block.Transactions[i] != txs[i] {
				t.Fatalf("the prefix must keep the original order; position %d differs", i)
			}
		}
		if err := node.ValidateBlock(res.block); err != nil {
			t.Fatalf("the prefix block must validate: %v", err)
		}
		// None of the 20 was at fault: all stay resident, and only the included
		// ones are leased.
		if node.MempoolSize() != len(txs) {
			t.Fatalf("no transaction may be lost to a budget cut, mempool has %d of %d", node.MempoolSize(), len(txs))
		}
		if got := livenessInFlight(node); got != included {
			t.Fatalf("expected %d leases, got %d", included, got)
		}
		if _, ok := livenessStrikes(t, node, txs[15]); ok {
			t.Fatalf("running out of budget is not a transaction's fault: no strike")
		}
	})

	t.Run("budget already gone before any progress", func(t *testing.T) {
		node := newTestNode(t)
		livenessFundedTransfers(t, node, 5)
		clock := time.Unix(1_800_000_000, 0)
		node.setLocalClock(func() time.Time { return clock })
		node.setBuildConfig(func(cfg *buildConfig) { cfg.Budget = time.Nanosecond })
		node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
			clock = clock.Add(time.Second)
			return nil
		})
		res := createBlockBounded(t, node, node.GetMempool(), 10*time.Second)
		if res.err != nil {
			t.Fatalf("CreateBlock: %v", res.err)
		}
		if len(res.block.Transactions) != 0 {
			t.Fatalf("with no budget the block is empty, got %d transactions", len(res.block.Transactions))
		}
	})
}

// A window whose total apply time is longer than the build budget, with every
// transaction costing the same in every wave (so re-running a prefix costs what
// it cost the first time), must still produce a non-empty block every round and
// must drain, rather than collapse to the empty block round after round. The
// cases include transactions that cost more than the share of the prefix budget
// the trim allows but less than the per-transaction slow limit: those are never
// struck, so the build has to keep at least one of them to make progress.
func TestPerfUniformlySlowWindowStillYieldsABlockEveryRound(t *testing.T) {
	every := func(d time.Duration) func(int) time.Duration {
		return func(int) time.Duration { return d }
	}
	cases := []struct {
		name  string
		count int
		// costOf is the apply cost of the transaction at position i of the first
		// window; the same transaction costs the same in every wave and round.
		costOf func(i int) time.Duration
	}{
		{"20 transactions at 100ms", 20, every(100 * time.Millisecond)},
		{"60 transactions at 30ms", 60, every(30 * time.Millisecond)},
		{"500 transactions at 3ms", 500, every(3 * time.Millisecond)},
		{"10 transactions at 230ms", 10, every(230 * time.Millisecond)},
		{"a 220ms head transaction then 400 at 3ms", 401, func(i int) time.Duration {
			if i == 0 {
				return 220 * time.Millisecond
			}
			return 3 * time.Millisecond
		}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			node := newTestNode(t)
			livenessFundedTransfers(t, node, tc.count)
			budget := node.buildConfigSnapshot().Budget
			window := node.GetMempool()
			if len(window) != tc.count {
				t.Fatalf("expected %d candidates, got %d", tc.count, len(window))
			}
			costs := make(map[*types.Transaction]time.Duration, len(window))
			var total time.Duration
			for i, tx := range window {
				costs[tx] = tc.costOf(i)
				total += costs[tx]
			}
			if total <= budget {
				t.Fatalf("the window must be slower than the %s budget to exercise the fallback", budget)
			}
			clock := time.Unix(1_800_000_000, 0)
			node.setLocalClock(func() time.Time { return clock })
			node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
				clock = clock.Add(costs[tx])
				return nil
			})

			resident := node.MempoolSize()
			for round := 1; round <= 3; round++ {
				if round > 1 {
					window = node.GetMempool()
				}
				res := createBlockBounded(t, node, window, 10*time.Second)
				if res.err != nil {
					t.Fatalf("round %d: CreateBlock: %v", round, res.err)
				}
				included := len(res.block.Transactions)
				if included == 0 {
					t.Fatalf("round %d: the block is empty although %d transactions are resident", round, resident)
				}
				if err := node.ValidateBlock(res.block); err != nil {
					t.Fatalf("round %d: the block must validate: %v", round, err)
				}
				if err := node.CommitBlock(res.block); err != nil {
					t.Fatalf("round %d: CommitBlock: %v", round, err)
				}
				if left := node.MempoolSize(); left != resident-included {
					t.Fatalf("round %d: mempool holds %d, expected %d after including %d of %d", round, left, resident-included, included, resident)
				}
				resident -= included
				snap := node.LivenessSnapshot()
				if snap.EmptyBlockFallbacks != 0 || snap.ConsecutiveBuildFailures != 0 {
					t.Fatalf("round %d: build fell back to an empty block (fallbacks=%d consecutive failures=%d last error=%q)",
						round, snap.EmptyBlockFallbacks, snap.ConsecutiveBuildFailures, snap.LastBuildError)
				}
				if resident == 0 {
					break
				}
			}
		})
	}
}

// The wave that ran out of budget also had a failing transaction. Only the run
// before that failure is a candidate prefix, and it is trimmed on the timings of
// exactly those transactions; the failing one is still struck.
func TestPerfBudgetCutWaveWithAFailureUsesTheTrimmedCleanPrefix(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 20)
	clock := time.Unix(1_800_000_000, 0)
	node.setLocalClock(func() time.Time { return clock })
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		clock = clock.Add(100 * time.Millisecond)
		if wave == 1 && tx == txs[3] {
			return errors.New("injected failure in the exploratory wave")
		}
		return nil
	})
	res := createBlockBounded(t, node, node.GetMempool(), 10*time.Second)
	if res.err != nil {
		t.Fatalf("CreateBlock: %v", res.err)
	}
	if len(res.block.Transactions) != 2 || res.block.Transactions[0] != txs[0] || res.block.Transactions[1] != txs[1] {
		t.Fatalf("expected the first two transactions of the clean run before the failure, got %d transactions", len(res.block.Transactions))
	}
	if err := node.ValidateBlock(res.block); err != nil {
		t.Fatalf("the prefix block must validate: %v", err)
	}
	if rec, ok := livenessStrikes(t, node, txs[3]); !ok || rec.strikes != 1 {
		t.Fatalf("the failing transaction carries a strike, got %+v ok=%v", rec, ok)
	}
	if _, ok := livenessStrikes(t, node, txs[2]); ok {
		t.Fatalf("a transaction left out only by the prefix trim is not at fault: no strike")
	}
}

// The finish offsets that the prefix trim relies on are measured from the start
// of the wave, so work done before a transaction (state set-up, scheduling and
// the budget check) is counted, not only the apply time itself. A clock that
// advances on every read makes each transaction's own apply time exactly one
// step, so any offset larger than the running sum of those steps comes from the
// time before the transaction started.
func TestPerfWaveRecordsFinishOffsetsFromTheStartOfTheWave(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 3)
	const step = 10 * time.Millisecond
	var (
		mu    sync.Mutex
		clock = time.Unix(1_800_000_000, 0)
	)
	node.setLocalClock(func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(step)
		return clock
	})
	ctx := node.newBuildCtx(txs)
	res, err := node.executeWave(ctx, 1, txs)
	if err != nil {
		t.Fatalf("executeWave: %v", err)
	}
	if res.State != nil {
		defer res.State.EndBlock()
	}
	if len(res.Failures) != 0 || len(res.Kept) != len(txs) || len(res.KeptAt) != len(txs) {
		t.Fatalf("expected %d kept transactions with an offset each, got kept=%d offsets=%d failures=%d",
			len(txs), len(res.Kept), len(res.KeptAt), len(res.Failures))
	}
	for i, at := range res.KeptAt {
		own := time.Duration(i+1) * step
		if at <= own {
			t.Fatalf("offset %d is %s, which does not count anything beyond the apply time (%s so far)", i, at, own)
		}
		if i > 0 && at <= res.KeptAt[i-1] {
			t.Fatalf("offsets must increase: %s then %s", res.KeptAt[i-1], at)
		}
	}
}

func TestFitPrefixToBudget(t *testing.T) {
	txs := make([]*types.Transaction, 5)
	for i := range txs {
		txs[i] = &types.Transaction{Nonce: uint64(i)}
	}
	ms := func(vals ...int) []time.Duration {
		out := make([]time.Duration, len(vals))
		for i, v := range vals {
			out[i] = time.Duration(v) * time.Millisecond
		}
		return out
	}
	// The limit for a 300ms budget is 210ms.
	budget := 300 * time.Millisecond

	cases := []struct {
		name    string
		prefix  []*types.Transaction
		keptAt  []time.Duration
		budget  time.Duration
		wantLen int
	}{
		{"empty prefix", nil, nil, budget, 0},
		{"every transaction fits", txs, ms(10, 20, 30, 40, 50), budget, 5},
		{"cut where the cumulative cost passes the limit", txs, ms(100, 200, 300, 400, 500), budget, 2},
		{"a transaction finishing exactly at the limit is kept", txs, ms(70, 140, 210, 280, 350), budget, 3},
		{"one nanosecond over the limit is cut", txs, []time.Duration{70 * time.Millisecond, 140 * time.Millisecond, 210*time.Millisecond + 1, 280 * time.Millisecond, 350 * time.Millisecond}, budget, 2},
		// The limit is 210ms, but a transaction is only struck above the
		// per-transaction slow limit, so a first transaction between the two is
		// kept alone: the re-check of a single transaction is never cut short.
		{"a first transaction just over the limit is kept alone", txs, ms(211, 220, 230, 240, 250), budget, 1},
		{"a first transaction near the slow limit is kept alone", txs, ms(260, 270, 280, 290, 300), budget, 1},
		{"a first transaction finishing exactly at the budget is kept alone", txs, ms(300, 310, 320, 330, 340), budget, 1},
		{"a first transaction beyond the whole budget leaves nothing", txs, []time.Duration{300*time.Millisecond + 1, 310 * time.Millisecond, 320 * time.Millisecond, 330 * time.Millisecond, 340 * time.Millisecond}, budget, 0},
		{"timing missing leaves the prefix unchanged", txs, nil, budget, 5},
		{"timing shorter than the prefix leaves it unchanged", txs, ms(100, 200), budget, 5},
		{"extra timing beyond the prefix is ignored", txs[:2], ms(100, 200, 300, 400), budget, 2},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := fitPrefixToBudget(tc.prefix, tc.keptAt, tc.budget)
			if len(got) != tc.wantLen {
				t.Fatalf("kept %d transactions, want %d", len(got), tc.wantLen)
			}
			for i := range got {
				if got[i] != tc.prefix[i] {
					t.Fatalf("position %d is not the original leading run", i)
				}
			}
			if cap(got) != len(got) {
				t.Fatalf("the trimmed prefix must not expose the cut tail (len %d, cap %d)", len(got), cap(got))
			}
		})
	}
}

// A transaction that applies but takes longer than SlowTx is excluded like any
// other failure, because a slow apply also slows every validator.
func TestPerfSlowTransactionIsFlaggedEvenThoughItApplied(t *testing.T) {
	node := newTestNode(t)
	txs := livenessFundedTransfers(t, node, 4)
	slow := txs[2]
	clock := time.Unix(1_800_000_000, 0)
	node.setLocalClock(func() time.Time { return clock })
	node.setProposalApplyHook(func(wave, index int, tx *types.Transaction) error {
		if tx == slow {
			clock = clock.Add(defaultSlowTx + 50*time.Millisecond)
		}
		return nil
	})
	res := createBlockBounded(t, node, node.GetMempool(), 10*time.Second)
	if res.err != nil {
		t.Fatalf("CreateBlock: %v", res.err)
	}
	if livenessBlockContains(t, res.block, slow) || len(res.block.Transactions) != 3 {
		t.Fatalf("the slow transaction must be excluded, got %d transactions", len(res.block.Transactions))
	}
	if rec, ok := livenessStrikes(t, node, slow); !ok || rec.strikes != 1 {
		t.Fatalf("the slow transaction carries a strike, got %+v ok=%v", rec, ok)
	}
}

// With Blocks.MaxTxs raised to 2000, how long does a full block of valid
// transfers take? The canonical scheduler is quadratic in the number of
// transactions; this pins that it still returns a valid block within the bound
// (the build budget may trim the block, which is fine -- it must never fail).
func TestPerfLargeBlockStaysWithinTheBound(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a 2000-transaction block")
	}
	node := newTestNode(t)
	cfg := node.globalConfigSnapshot()
	cfg.Blocks.MaxTxs = 2000
	if err := node.SetGlobalConfig(cfg); err != nil {
		t.Fatalf("set config: %v", err)
	}
	livenessFundedTransfers(t, node, 2000)

	res := createBlockBounded(t, node, node.GetMempool(), 15*time.Second)
	if res.err != nil {
		t.Fatalf("CreateBlock: %v", res.err)
	}
	if res.took > 3*perfRoundBound {
		t.Fatalf("a 2000-transaction block took %s", res.took)
	}
	if err := node.ValidateBlock(res.block); err != nil {
		t.Fatalf("the block must validate: %v", err)
	}
	t.Logf("MaxTxs=2000: included %d of 2000 in %s (budget %s)", len(res.block.Transactions), res.took, node.buildConfigSnapshot().Budget)
}
