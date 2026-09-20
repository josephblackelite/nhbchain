package lending

import (
	"math/big"
	"testing"

	"nhbchain/crypto"
)

// accrualEngine is an engine with a 50% borrow rate at the 50% utilisation of
// the market it accrues (see TestAccrueInterestUpdatesIndexesAndFees) and no
// reserve or protocol cut, so the borrow index grows by exactly the rate.
func accrualEngine(t *testing.T) (*Engine, *mockEngineState) {
	t.Helper()
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x11)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x12)
	engine := NewEngine(moduleAddr, collateralAddr, RiskParameters{})
	engine.SetInterestModel(NewInterestModel(0, 1, 0, 1))
	state := newMockEngineState()
	engine.SetState(state)
	engine.SetPoolID("default")
	return engine, state
}

func newAccrualMarket(lastBlock, lastTimestamp uint64) *Market {
	return &Market{
		TotalNHBSupplied:    big.NewInt(1_000_000_000),
		TotalSupplyShares:   big.NewInt(1_000_000_000),
		TotalNHBBorrowed:    big.NewInt(500_000_000),
		SupplyIndex:         new(big.Int).Set(ray),
		BorrowIndex:         new(big.Int).Set(ray),
		LastUpdateBlock:     lastBlock,
		LastUpdateTimestamp: lastTimestamp,
	}
}

// accrueAt refreshes market at (height, timestamp) with a fresh engine, the
// way one block's transaction does.
func accrueAt(t *testing.T, engine *Engine, state *mockEngineState, market *Market, height uint64, timestamp int64) {
	t.Helper()
	engine.SetBlockHeight(height)
	engine.SetBlockTimestamp(timestamp)
	state.market = market
	if _, _, err := engine.accrueInterest(market); err != nil {
		t.Fatalf("accrue at height %d: %v", height, err)
	}
}

// Interest follows the block time that passed, not the number of blocks: the
// same elapsed time produces the same indexes whether one block or a thousand
// carried it.
func TestAccrueInterestFollowsBlockTimeNotBlockCount(t *testing.T) {
	const start, elapsed = int64(1_700_000_000), int64(3_600)

	oneBlock, stateA := accrualEngine(t)
	marketA := newAccrualMarket(100, uint64(start))
	accrueAt(t, oneBlock, stateA, marketA, 101, start+elapsed)

	manyBlocks, stateB := accrualEngine(t)
	marketB := newAccrualMarket(100, uint64(start))
	accrueAt(t, manyBlocks, stateB, marketB, 1_100, start+elapsed)

	if marketA.BorrowIndex.Cmp(marketB.BorrowIndex) != 0 || marketA.SupplyIndex.Cmp(marketB.SupplyIndex) != 0 || marketA.TotalNHBBorrowed.Cmp(marketB.TotalNHBBorrowed) != 0 {
		t.Fatalf("one block and a thousand blocks over the same time accrued differently: borrow index %s vs %s, borrowed %s vs %s",
			marketA.BorrowIndex, marketB.BorrowIndex, marketA.TotalNHBBorrowed, marketB.TotalNHBBorrowed)
	}
	if marketA.BorrowIndex.Cmp(ray) <= 0 {
		t.Fatalf("expected the borrow index to grow over an hour, got %s", marketA.BorrowIndex)
	}
	if marketA.LastUpdateTimestamp != uint64(start+elapsed) || marketA.LastUpdateBlock != 101 {
		t.Fatalf("market stamped at block %d time %d, want 101 and %d", marketA.LastUpdateBlock, marketA.LastUpdateTimestamp, start+elapsed)
	}
}

// A block every two seconds accrues the stated rate, not half of it: over a
// day the borrow index grows by the 50% annual rate times the fraction of the
// year that day is, at either cadence.
func TestAccrueInterestStatesTheSameRateAtOneAndTwoSecondBlocks(t *testing.T) {
	const day = int64(86_400)
	start := int64(1_700_000_000)

	grow := func(step int64) *big.Int {
		engine, state := accrualEngine(t)
		market := newAccrualMarket(1, uint64(start))
		height := uint64(1)
		for elapsed := step; elapsed <= day; elapsed += step {
			height++
			accrueAt(t, engine, state, market, height, start+elapsed)
		}
		return market.BorrowIndex
	}

	// 50% a year over a day: 0.5 * 86400 / 31,536,000 = 0.00136986... of the index.
	expected := new(big.Int).Mul(ray, big.NewInt(day))
	expected.Mul(expected, big.NewInt(5))
	expected.Quo(expected, new(big.Int).Mul(big.NewInt(10), big.NewInt(secondsPerYear)))
	expected.Add(expected, ray)

	// Compounding every step, and the utilisation the interest itself raises, put
	// the result a few millionths above the simple figure; a cadence that accrued
	// half the rate would be 500 millionths off.
	tolerance := new(big.Int).Quo(ray, big.NewInt(100_000))
	for _, step := range []int64{1, 2} {
		got := grow(step)
		diff := new(big.Int).Sub(got, expected)
		diff.Abs(diff)
		if diff.Cmp(tolerance) > 0 {
			t.Fatalf("with a block every %d s the borrow index after a day is %s, want %s within %s", step, got, expected, tolerance)
		}
	}
}

// A block that shares the second of the one before, or reports an earlier one,
// accrues nothing and never moves the recorded time back.
func TestAccrueInterestIgnoresAZeroOrNegativeTimeDelta(t *testing.T) {
	const start = int64(1_700_000_000)
	engine, state := accrualEngine(t)
	market := newAccrualMarket(50, uint64(start))

	accrueAt(t, engine, state, market, 51, start) // same second
	if market.BorrowIndex.Cmp(ray) != 0 || market.TotalNHBBorrowed.Cmp(big.NewInt(500_000_000)) != 0 {
		t.Fatalf("a block in the same second accrued interest: index %s", market.BorrowIndex)
	}
	if market.LastUpdateBlock != 51 || market.LastUpdateTimestamp != uint64(start) {
		t.Fatalf("stamped block %d time %d, want 51 and %d", market.LastUpdateBlock, market.LastUpdateTimestamp, start)
	}

	accrueAt(t, engine, state, market, 52, start-500) // earlier timestamp
	if market.BorrowIndex.Cmp(ray) != 0 {
		t.Fatalf("a block reporting an earlier time accrued interest: index %s", market.BorrowIndex)
	}
	if market.LastUpdateTimestamp != uint64(start) {
		t.Fatalf("the recorded time moved back to %d", market.LastUpdateTimestamp)
	}

	accrueAt(t, engine, state, market, 53, start+10) // time moves on again
	if market.BorrowIndex.Cmp(ray) <= 0 || market.LastUpdateTimestamp != uint64(start+10) {
		t.Fatalf("expected interest for the 10 s that passed and the time recorded, got index %s time %d", market.BorrowIndex, market.LastUpdateTimestamp)
	}
}

// A market that was never stamped with a block time accrues its first step by
// the block count, one second a block as before, and is stamped by it; after
// that it follows block time.
func TestAccrueInterestUnstampedMarketAccruesByBlockCountOnce(t *testing.T) {
	const start = int64(1_700_000_000)
	engine, state := accrualEngine(t)
	legacy := newAccrualMarket(100, 0)
	accrueAt(t, engine, state, legacy, 200, start)

	stamped := newAccrualMarket(100, uint64(start-100))
	stampedEngine, stampedState := accrualEngine(t)
	accrueAt(t, stampedEngine, stampedState, stamped, 200, start)

	if legacy.BorrowIndex.Cmp(stamped.BorrowIndex) != 0 {
		t.Fatalf("100 blocks of an unstamped market accrued %s, the same 100 s of a stamped one %s", legacy.BorrowIndex, stamped.BorrowIndex)
	}
	if legacy.LastUpdateTimestamp != uint64(start) {
		t.Fatalf("the accrual did not stamp the market: %d", legacy.LastUpdateTimestamp)
	}

	// From here on block time rules: 500 blocks that took 10 s accrue 10 s.
	accrueAt(t, engine, state, legacy, 700, start+10)
	fromStart := newAccrualMarket(100, uint64(start-100))
	fromStartEngine, fromStartState := accrualEngine(t)
	accrueAt(t, fromStartEngine, fromStartState, fromStart, 200, start)
	accrueAt(t, fromStartEngine, fromStartState, fromStart, 700, start+10)
	if legacy.BorrowIndex.Cmp(fromStart.BorrowIndex) != 0 {
		t.Fatalf("after being stamped the market accrued %s, want %s", legacy.BorrowIndex, fromStart.BorrowIndex)
	}
}
