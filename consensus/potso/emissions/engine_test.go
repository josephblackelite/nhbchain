package emissions

import (
	"math/big"
	"testing"
)

func TestEnginePoolForEpoch(t *testing.T) {
	schedule := &Schedule{entries: []scheduleEntry{{startEpoch: 1, amount: big.NewInt(100)}, {startEpoch: 5, amount: big.NewInt(50)}}}
	engine, err := NewEngine(schedule, Caps{Global: big.NewInt(180), Epoch: big.NewInt(60)})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	pool, remaining, err := engine.PoolForEpoch(1, big.NewInt(0))
	if err != nil {
		t.Fatalf("pool epoch1: %v", err)
	}
	if pool.Cmp(big.NewInt(60)) != 0 {
		t.Fatalf("expected epoch cap 60, got %s", pool)
	}
	if remaining.Cmp(big.NewInt(120)) != 0 {
		t.Fatalf("remaining mismatch: %s", remaining)
	}
	pool, remaining, err = engine.PoolForEpoch(5, big.NewInt(60))
	if err != nil {
		t.Fatalf("pool epoch5: %v", err)
	}
	if pool.Cmp(big.NewInt(50)) != 0 {
		t.Fatalf("expected schedule amount 50, got %s", pool)
	}
	if remaining.Cmp(big.NewInt(70)) != 0 {
		t.Fatalf("remaining mismatch: %s", remaining)
	}
}
