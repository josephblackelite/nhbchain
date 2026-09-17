package core

import (
	"fmt"
	"math/big"
	"sync"
	"testing"
)

func TestNodeGetValidatorSetReturnsCopy(t *testing.T) {
	original := map[string]*big.Int{
		"alpha": big.NewInt(1),
		"beta":  nil,
	}
	node := &Node{state: &StateProcessor{ValidatorSet: original}}

	snapshot := node.GetValidatorSet()
	if snapshot == nil {
		t.Fatalf("expected snapshot, got nil")
	}
	if len(snapshot) != len(original) {
		t.Fatalf("expected %d validators, got %d", len(original), len(snapshot))
	}
	if _, ok := snapshot["beta"]; !ok {
		t.Fatalf("snapshot missing beta entry")
	}

	// Mutating the snapshot must not affect the original map entries.
	snapshot["gamma"] = big.NewInt(3)
	if _, exists := original["gamma"]; exists {
		t.Fatalf("unexpected mutation of original map when adding new entry")
	}

	// Mutating a big.Int in the snapshot should not affect the original value.
	if snapshot["alpha"] == nil {
		t.Fatalf("expected snapshot to contain alpha entry")
	}
	snapshot["alpha"].Add(snapshot["alpha"], big.NewInt(4))
	if original["alpha"].Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("original validator power mutated: %v", original["alpha"])
	}
}

func TestNodeGetValidatorSetConcurrentAccess(t *testing.T) {
	node := &Node{state: &StateProcessor{ValidatorSet: map[string]*big.Int{"validator-0": big.NewInt(1)}}}

	const iterations = 1000
	var wg sync.WaitGroup
	start := make(chan struct{})
	panicCh := make(chan interface{}, 1)

	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < iterations; i++ {
			node.stateMu.Lock()
			if node.state.ValidatorSet == nil {
				node.state.ValidatorSet = make(map[string]*big.Int)
			}
			key := fmt.Sprintf("validator-%d", i%5)
			node.state.ValidatorSet[key] = big.NewInt(int64(i))
			if i%3 == 0 {
				delete(node.state.ValidatorSet, key)
			}
			node.stateMu.Unlock()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				panicCh <- r
			}
		}()
		<-start
		for i := 0; i < iterations; i++ {
			snapshot := node.GetValidatorSet()
			for range snapshot {
				// Iterate to mimic consumers walking the map.
			}
		}
	}()

	close(start)
	wg.Wait()

	select {
	case r := <-panicCh:
		t.Fatalf("GetValidatorSet triggered panic under concurrent access: %v", r)
	default:
	}
}
