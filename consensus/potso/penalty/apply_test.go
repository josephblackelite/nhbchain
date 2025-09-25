package penalty

import (
	"math/big"
	"testing"

	"nhbchain/consensus/potso/evidence"
	"nhbchain/core/events"
	statepotso "nhbchain/state/potso"
)

func newLedgerForTest(t *testing.T) *statepotso.Ledger {
	ledger, err := statepotso.NewLedger(big.NewInt(100), big.NewInt(1000))
	if err != nil {
		t.Fatalf("new ledger: %v", err)
	}
	return ledger
}

func TestEngineApplyEquivocation(t *testing.T) {
	ledger := newLedgerForTest(t)
	offender := [20]byte{1, 2, 3}
	if _, err := ledger.Set(offender, big.NewInt(800), big.NewInt(800)); err != nil {
		t.Fatalf("set weight: %v", err)
	}
	cfg := DefaultConfig()
	cfg.EquivocationThetaBps = 5000
	cfg.EquivocationMinDecay = big.NewInt(50)
	catalog, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	engine := NewEngine(catalog, ledger, nil)
	record := &evidence.Record{Hash: [32]byte{0xAA}, Evidence: evidence.Evidence{Type: evidence.TypeEquivocation, Offender: offender}}
	result, err := engine.Apply(record, Context{BlockHeight: 99})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if result.Idempotent {
		t.Fatalf("expected non-idempotent")
	}
	if result.WeightUpdate.Current.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected new weight 400 got %s", result.WeightUpdate.Current)
	}
	if result.Event == nil || result.Event.Type != events.TypePotsoPenaltyApplied {
		t.Fatalf("expected penalty event")
	}
	if !ledger.WasPenaltyApplied(record.Hash, offender) {
		t.Fatalf("expected penalty marker")
	}
	again, err := engine.Apply(record, Context{BlockHeight: 100})
	if err != nil {
		t.Fatalf("apply repeat: %v", err)
	}
	if !again.Idempotent {
		t.Fatalf("expected idempotent on repeat")
	}
}
