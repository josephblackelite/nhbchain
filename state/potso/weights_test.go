package potso

import (
	"math/big"
	"testing"
)

func mustBig(v string) *big.Int {
	n, ok := new(big.Int).SetString(v, 10)
	if !ok {
		panic("invalid big int")
	}
	return n
}

func TestLedgerBoundsAndClamp(t *testing.T) {
	floor := mustBig("100")
	ceil := mustBig("1000")
	ledger, err := NewLedger(floor, ceil)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	addr := [20]byte{1}
	if _, err := ledger.Set(addr, mustBig("500"), mustBig("1500")); err != nil {
		t.Fatalf("set weight: %v", err)
	}
	entry := ledger.Entry(addr)
	if entry.Value.Cmp(ceil) != 0 {
		t.Fatalf("expected clamp to ceil: got %s", entry.Value)
	}
	update, err := ledger.ApplyDecay(addr, mustBig("950"))
	if err != nil {
		t.Fatalf("apply decay: %v", err)
	}
	if update.Applied.Cmp(mustBig("900")) != 0 {
		t.Fatalf("expected applied 900 got %s", update.Applied)
	}
	if update.Current.Cmp(mustBig("100")) != 0 {
		t.Fatalf("expected floor 100 got %s", update.Current)
	}
	if !update.Clamped {
		t.Fatalf("expected clamp flag")
	}
}

func TestLedgerPenaltyMarkers(t *testing.T) {
	ledger, err := NewLedger(nil, nil)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	addr := [20]byte{2}
	var hash [32]byte
	hash[0] = 0xAA
	if ledger.WasPenaltyApplied(hash, addr) {
		t.Fatalf("unexpected marker")
	}
	ledger.MarkPenaltyApplied(hash, addr)
	if !ledger.WasPenaltyApplied(hash, addr) {
		t.Fatalf("expected marker")
	}
}

func TestEnsureBaselineSeedsFirstTimeOffenderFromRealStake(t *testing.T) {
	ledger, err := NewLedger(nil, nil)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	addr := [20]byte{9}
	entry, err := ledger.EnsureBaseline(addr, mustBig("5000"))
	if err != nil {
		t.Fatalf("ensure baseline: %v", err)
	}
	if entry.Base.Cmp(mustBig("5000")) != 0 {
		t.Fatalf("expected base 5000, got %s", entry.Base)
	}
	if entry.Value.Cmp(mustBig("5000")) != 0 {
		t.Fatalf("expected value 5000, got %s", entry.Value)
	}
}

func TestEnsureBaselinePreservesValueButRefreshesBaseOnSubsequentCalls(t *testing.T) {
	ledger, err := NewLedger(nil, nil)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	addr := [20]byte{10}
	if _, err := ledger.EnsureBaseline(addr, mustBig("1000")); err != nil {
		t.Fatalf("ensure baseline: %v", err)
	}
	// A slash/decay drops Value below Base, as real penalty application does.
	if _, err := ledger.ApplyDecay(addr, mustBig("400")); err != nil {
		t.Fatalf("apply decay: %v", err)
	}
	// Stake grows before the next offense -- Base should track it, but the
	// already-applied penalty must not silently heal.
	entry, err := ledger.EnsureBaseline(addr, mustBig("2000"))
	if err != nil {
		t.Fatalf("ensure baseline: %v", err)
	}
	if entry.Base.Cmp(mustBig("2000")) != 0 {
		t.Fatalf("expected refreshed base 2000, got %s", entry.Base)
	}
	if entry.Value.Cmp(mustBig("600")) != 0 {
		t.Fatalf("expected value to stay at post-decay 600, got %s", entry.Value)
	}
}

func TestLedgerSetBounds(t *testing.T) {
	ledger, err := NewLedger(nil, nil)
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	addr := [20]byte{3}
	if _, err := ledger.Set(addr, mustBig("200"), mustBig("200")); err != nil {
		t.Fatalf("set weight: %v", err)
	}
	if err := ledger.SetBounds(mustBig("400"), mustBig("500")); err != nil {
		t.Fatalf("set bounds: %v", err)
	}
	entry := ledger.Entry(addr)
	if entry.Value.Cmp(mustBig("400")) != 0 {
		t.Fatalf("expected weight clamped to new floor, got %s", entry.Value)
	}
}
