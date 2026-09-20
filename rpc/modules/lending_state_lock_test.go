package modules

// What the public lending reads do to the rest of the node. These use only the
// module's public methods and the test chain's helpers, so they can be pointed
// at an older tree to see what it does there.

import (
	"sync"
	"testing"
	"time"
)

// TestRepeatedLendingReadsDoNotRescanTheChain: every read used to walk every
// block of the chain (and did so inside the state lock).
func TestRepeatedLendingReadsDoNotRescanTheChain(t *testing.T) {
	lc := newLendingChain(t, 21)
	lc.extend(t, 4000, 0)
	module := NewLendingModule(lc.node)
	if _, _, moduleErr := module.GetMarket(defaultLendingPoolID); moduleErr != nil {
		t.Fatalf("first read: %v", moduleErr)
	}
	before := lc.store.gets.Load()
	for i := 0; i < 5; i++ {
		if _, _, moduleErr := module.GetMarket(defaultLendingPoolID); moduleErr != nil {
			t.Fatalf("read %d: %v", i, moduleErr)
		}
	}
	if reads := lc.store.gets.Load() - before; reads > 200 {
		t.Fatalf("five repeated reads of a %d-block chain read the store %d times", lc.node.GetHeight(), reads)
	}
}

// TestLendingReadsDoNotHoldTheStateLockWhileScanning: the scan used to run
// inside the exclusive state lock, so every state read waited for it.
func TestLendingReadsDoNotHoldTheStateLockWhileScanning(t *testing.T) {
	lc := newLendingChain(t, 11)
	lc.extend(t, 60000, 0)
	module := NewLendingModule(lc.node)
	var addr [20]byte
	addr[0] = 0x42

	var wg sync.WaitGroup
	done := make(chan struct{})
	var worst time.Duration
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			start := time.Now()
			if _, err := lc.node.GetAccount(addr[:]); err != nil {
				t.Errorf("get account: %v", err)
				return
			}
			if d := time.Since(start); d > worst {
				worst = d
			}
		}
	}()
	// Let the reader get going, then run the lending read, whose first call has
	// the whole chain to look through.
	time.Sleep(20 * time.Millisecond)
	scanStart := time.Now()
	if _, _, moduleErr := module.GetMarket(defaultLendingPoolID); moduleErr != nil {
		t.Fatalf("GetMarket: %v", moduleErr)
	}
	scan := time.Since(scanStart)
	close(done)
	wg.Wait()
	if scan < 30*time.Millisecond {
		t.Skipf("the first lending read took only %s on this machine; too fast to tell whether it held the lock", scan)
	}
	if worst > scan/4 {
		t.Fatalf("a state read waited %s while the first lending read (%s) was scanning the chain", worst, scan)
	}
}
