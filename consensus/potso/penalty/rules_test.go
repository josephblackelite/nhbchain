package penalty

import (
	"math/big"
	"testing"

	"nhbchain/consensus/potso/evidence"
)

func TestEquivocationPenalty(t *testing.T) {
	cfg := DefaultConfig()
	cfg.EquivocationThetaBps = 4000
	cfg.EquivocationMinDecay = big.NewInt(50)
	catalog, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	rule, ok := catalog.Rule(evidence.TypeEquivocation)
	if !ok {
		t.Fatalf("missing rule")
	}
	meta := Metadata{BaseWeight: big.NewInt(200), CurrentWeight: big.NewInt(150)}
	penalty, err := rule.Compute(meta)
	if err != nil {
		t.Fatalf("compute penalty: %v", err)
	}
	expected := big.NewInt(80)
	if penalty.DecayAmount.Cmp(expected) != 0 {
		t.Fatalf("expected decay %s got %s", expected, penalty.DecayAmount)
	}
}

func TestDowntimeLadder(t *testing.T) {
	cfg := DefaultConfig()
	catalog, err := BuildCatalog(cfg)
	if err != nil {
		t.Fatalf("build catalog: %v", err)
	}
	rule, ok := catalog.Rule(evidence.TypeDowntime)
	if !ok {
		t.Fatalf("missing downtime rule")
	}
	meta := Metadata{CurrentWeight: big.NewInt(1000), MissedEpochs: 3}
	penalty, err := rule.Compute(meta)
	if err != nil {
		t.Fatalf("compute penalty: %v", err)
	}
	expected := big.NewInt(100)
	if penalty.DecayAmount.Cmp(expected) != 0 {
		t.Fatalf("expected decay %s got %s", expected, penalty.DecayAmount)
	}
}
