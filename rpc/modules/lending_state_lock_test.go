package modules

// What the public lending reads do to the rest of the node. These use only the
// module's public methods and the test chain's helpers, so they can be pointed
// at an older tree to see what it does there.

import (
	"sync"
	"sync/atomic"
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

const (
	// A state read that waits this long for one lending read is not explained by
	// a scheduler or garbage-collector pause, however loaded the machine is.
	stateLockFloor = 75 * time.Millisecond
	// While a lending read that is not holding the state lock runs, a state read
	// in a loop completes thousands of times; while one that is holding it runs,
	// it completes about once.
	stateLockMinProgress = 25
	stateLockAttempts    = 5
)

type lockProbe struct {
	scan     time.Duration // how long the first lending read took
	worst    time.Duration // the longest any state read waited
	progress int64         // state reads that finished while the lending read ran
}

// probeFirstLendingRead runs the first lending read of a fresh module, which
// has the whole chain to look through, while another goroutine reads state as
// fast as it can.
func probeFirstLendingRead(t *testing.T, lc *lendingChain) lockProbe {
	t.Helper()
	module := NewLendingModule(lc.node)
	var addr [20]byte
	addr[0] = 0x42

	var (
		wg       sync.WaitGroup
		stop     atomic.Bool
		scanning atomic.Bool
		progress atomic.Int64
		worst    time.Duration
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			start := time.Now()
			if _, err := lc.node.GetAccount(addr[:]); err != nil {
				t.Errorf("get account: %v", err)
				return
			}
			if d := time.Since(start); d > worst {
				worst = d
			}
			if scanning.Load() {
				progress.Add(1)
			}
		}
	}()
	// Let the reader get going, then run the lending read.
	time.Sleep(20 * time.Millisecond)
	scanning.Store(true)
	scanStart := time.Now()
	_, _, moduleErr := module.GetMarket(defaultLendingPoolID)
	scan := time.Since(scanStart)
	scanning.Store(false)
	stop.Store(true)
	wg.Wait()
	if moduleErr != nil {
		t.Fatalf("GetMarket: %v", moduleErr)
	}
	return lockProbe{scan: scan, worst: worst, progress: progress.Load()}
}

// TestLendingReadsDoNotHoldTheStateLockWhileScanning: the scan used to run
// inside the exclusive state lock, so every state read waited for it.
//
// Wall-clock measurements on a busy machine include pauses that have nothing to
// do with the lock, so the test does not trust a single one: a probe shows a
// held lock when a state read waited longer than a quarter of the scan and
// longer than any pause could explain, or when hardly any state read got
// through while the scan ran (a count, which a pause does not change much); and
// the test fails only if every one of several independent probes shows one. A
// lock held for the whole scan does that every time.
func TestLendingReadsDoNotHoldTheStateLockWhileScanning(t *testing.T) {
	lc := newLendingChain(t, 11)
	lc.extend(t, 60000, 0)

	var probes []lockProbe
	for attempt := 0; attempt < stateLockAttempts; attempt++ {
		p := probeFirstLendingRead(t, lc)
		if p.scan < 30*time.Millisecond {
			t.Skipf("the first lending read took only %s on this machine; too fast to tell whether it held the lock", p.scan)
		}
		limit := p.scan / 4
		if limit < stateLockFloor {
			limit = stateLockFloor
		}
		if p.worst <= limit && p.progress >= stateLockMinProgress {
			t.Logf("attempt %d: lending read %s, longest state read wait %s, %d state reads finished during it", attempt+1, p.scan, p.worst, p.progress)
			return // the lock was not held for the scan
		}
		t.Logf("attempt %d: lending read %s, longest state read wait %s (limit %s), %d state reads finished during it (want at least %d)",
			attempt+1, p.scan, p.worst, limit, p.progress, stateLockMinProgress)
		probes = append(probes, p)
	}
	t.Fatalf("in each of %d attempts a state read waited for the first lending read to finish its scan of the chain (%+v)", len(probes), probes)
}
