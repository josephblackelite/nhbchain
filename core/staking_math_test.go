package core

import (
	"math"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func TestStakingIndexMonthly_1250bps(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(db.Close)

	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}

	mgr := nhbstate.NewManager(tr)
	engine := nhbstate.NewRewardEngine(mgr)

	aprBps := uint64(1_250)
	payoutDays := uint64(30)
	start := time.Unix(1_700_000_000, 0).UTC()

	if err := engine.UpdateGlobalIndex(aprBps, payoutDays, start); err != nil {
		t.Fatalf("update global index at start: %v", err)
	}

	end := start.Add(30 * 24 * time.Hour)
	if err := engine.UpdateGlobalIndex(aprBps, payoutDays, end); err != nil {
		t.Fatalf("update global index at end: %v", err)
	}

	globalIndex, err := mgr.GetGlobalIndex()
	if err != nil {
		t.Fatalf("get global index: %v", err)
	}

	if got, want := globalIndex.LastUpdateUnix, end.Unix(); got != want {
		t.Fatalf("unexpected last update: got %d want %d", got, want)
	}

	raw := new(big.Int).SetBytes(globalIndex.UQ128x128)
	if raw.Sign() == 0 {
		t.Fatalf("global index not initialised")
	}

	unit := new(big.Int).Lsh(big.NewInt(1), 128)
	ratio := new(big.Float).SetInt(raw)
	ratio.Quo(ratio, new(big.Float).SetInt(unit))

	monthlyRate := new(big.Float).Sub(ratio, big.NewFloat(1))
	monthlyRateFloat, _ := monthlyRate.Float64()

	expected := float64(aprBps) / 10_000 / 12
	if diff := math.Abs(monthlyRateFloat - expected); diff > 2e-4 {
		t.Fatalf("unexpected monthly rate diff: got %.9f expected %.9f diff %.9f", monthlyRateFloat, expected, diff)
	}
}
