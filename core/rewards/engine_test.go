package rewards

import (
	"math/big"
	"testing"
	"time"
)

func Test_UpdateGlobalIndex_AdvancesWithBlockTime(t *testing.T) {
	engine := NewEngine()
	start := time.Unix(1_700_000_000, 0)
	engine.SetLastUpdateTs(uint64(start.Unix()))

	updated, changed := engine.UpdateGlobalIndex(start.Add(365*24*time.Hour), 10_000)
	if !changed {
		t.Fatalf("expected index update to report change")
	}
	expected := new(big.Int).Add(IndexUnit(), IndexUnit())
	if updated.Cmp(expected) != 0 {
		t.Fatalf("unexpected index: got %s want %s", updated, expected)
	}
	if engine.LastUpdateTs() != uint64(start.Add(365*24*time.Hour).Unix()) {
		t.Fatalf("last update mismatch: %d", engine.LastUpdateTs())
	}

	// Reapplying at the same timestamp should not change the index.
	next, changed := engine.UpdateGlobalIndex(start.Add(365*24*time.Hour), 10_000)
	if changed {
		t.Fatalf("expected no change when timestamp does not advance")
	}
	if next.Cmp(updated) != 0 {
		t.Fatalf("index mutated unexpectedly")
	}
}
