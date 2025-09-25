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
