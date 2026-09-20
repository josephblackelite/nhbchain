package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/lending"
)

func seedLegacyLendingPosition(t *testing.T, sp *StateProcessor, addr [20]byte, debt int64) {
	t.Helper()
	if err := nhbstate.NewManager(sp.Trie).PutAccount(addr[:], &types.Account{
		BalanceNHB:        big.NewInt(0),
		BalanceZNHB:       big.NewInt(0),
		CollateralBalance: big.NewInt(5_000),
		SupplyShares:      big.NewInt(1_000),
		DebtPrincipal:     big.NewInt(debt),
	}); err != nil {
		t.Fatalf("seed legacy position: %v", err)
	}
}

func lendingUserExists(t *testing.T, sp *StateProcessor, poolID string, addr [20]byte) bool {
	t.Helper()
	_, ok, err := nhbstate.NewManager(sp.Trie).LendingGetUserAccount(poolID, addr)
	if err != nil {
		t.Fatalf("load lending user account: %v", err)
	}
	return ok
}

// The sweep that lists every account to move legacy lending positions into a
// pool runs until it has completed once for that pool and is then marked done:
// every lending transaction used to run it again. A position that appears
// after the mark is left for the per-account migration and is not swept, and a
// pool that has not been swept yet still gets its own pass.
func TestLendingLegacySweepRunsOncePerPool(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetLendingRiskParameters(lending.RiskParameters{MaxLTV: 7_500, LiquidationThreshold: 8_000})
	manager := nhbstate.NewManager(sp.Trie)
	first, second := [20]byte{0xE1}, [20]byte{0xE2}

	seedLegacyLendingPosition(t, sp, first, 250)
	if done, err := manager.LendingLegacyReconciled("default"); err != nil || done {
		t.Fatalf("the pool is marked before any sweep: done=%v err=%v", done, err)
	}
	if _, _, err := sp.lendingEngine("default"); err != nil {
		t.Fatalf("first engine: %v", err)
	}
	if !lendingUserExists(t, sp, "default", first) {
		t.Fatalf("the first sweep did not migrate the legacy position")
	}
	if done, err := manager.LendingLegacyReconciled("default"); err != nil || !done {
		t.Fatalf("the pool is not marked after a completed sweep: done=%v err=%v", done, err)
	}

	seedLegacyLendingPosition(t, sp, second, 300)
	if _, _, err := sp.lendingEngine("default"); err != nil {
		t.Fatalf("second engine: %v", err)
	}
	if lendingUserExists(t, sp, "default", second) {
		t.Fatalf("a completed pool was swept again")
	}

	// Another pool has had no sweep yet, so it gets its own.
	if _, _, err := sp.lendingEngine("other"); err != nil {
		t.Fatalf("engine for another pool: %v", err)
	}
	if !lendingUserExists(t, sp, "other", second) {
		t.Fatalf("the pool that had not been swept was not swept")
	}
}

// Nothing the chain has executed writes the mark: it is set only by a lending
// engine. A block lifecycle and a lending reference price leave it unset.
func TestLendingLegacySweepMarkIsNotWrittenWithoutALendingEngine(t *testing.T) {
	sp, key := newLendingRefPriceTestState(t)
	ts := int64(1_800_000_000)
	sp.BeginBlock(5, time.Unix(ts, 0).UTC())
	if err := sp.ProcessBlockLifecycle(5, ts); err != nil {
		sp.EndBlock()
		t.Fatalf("lifecycle: %v", err)
	}
	sp.EndBlock()
	if err := submitLendingRefPriceTx(t, sp, big.NewInt(5), big.NewInt(100), time.Unix(ts, 0).UTC(), key); err != nil {
		t.Fatalf("lending reference price: %v", err)
	}
	if done, err := nhbstate.NewManager(sp.Trie).LendingLegacyReconciled("default"); err != nil || done {
		t.Fatalf("the sweep mark was written without a lending engine: done=%v err=%v", done, err)
	}
}
