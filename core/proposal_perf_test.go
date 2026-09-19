package core

import (
	"bytes"
	"errors"
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
		// over that prefix is quick.
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
		if got := len(res.block.Transactions); got != 10 {
			t.Fatalf("expected the 10 transactions applied before the budget ran out, got %d", got)
		}
		if err := node.ValidateBlock(res.block); err != nil {
			t.Fatalf("the prefix block must validate: %v", err)
		}
		// None of the 20 was at fault: all stay resident, and only the 10
		// included ones are leased.
		if node.MempoolSize() != len(txs) {
			t.Fatalf("no transaction may be lost to a budget cut, mempool has %d of %d", node.MempoolSize(), len(txs))
		}
		if got := livenessInFlight(node); got != 10 {
			t.Fatalf("expected 10 leases, got %d", got)
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
