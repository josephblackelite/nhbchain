package modules

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestReplayMatchesTheFullWalk(t *testing.T) {
	for _, tc := range []struct {
		seed        int64
		blocks      int
		every       int
		extraBlocks int
		extraEvery  int
	}{
		{1, 300, 5, 200, 4},
		{2, 500, 40, 100, 30},
		{3, 50, 1, 20, 1},
		{4, 400, 0, 100, 0}, // a chain with no lending transaction at all
	} {
		tc := tc
		t.Run(fmt.Sprintf("seed%d", tc.seed), func(t *testing.T) {
			lc := newLendingChain(t, tc.seed)
			module := NewLendingModule(lc.node)
			compare := func(stage string) {
				for _, pool := range []string{"", "default", "poolB", "poolC", "nobody"} {
					wantMarket, wantUsers, wantErr := module.legacyReplayCommittedPoolState(pool)
					for pass := 0; pass < 2; pass++ {
						gotMarket, gotUsers, gotErr := module.replayCommittedPoolState(pool)
						if (wantErr == nil) != (gotErr == nil) {
							t.Fatalf("%s pool %q: error mismatch %v / %v", stage, pool, wantErr, gotErr)
						}
						if want, got := describeReplay(wantMarket, wantUsers), describeReplay(gotMarket, gotUsers); want != got {
							t.Fatalf("%s pool %q pass %d: replay differs\nwant %s\n got %s", stage, pool, pass, want, got)
						}
					}
				}
			}
			lc.extend(t, tc.blocks, tc.every)
			compare("first")
			// More blocks arrive; the index must pick them up and nothing else change.
			lc.extend(t, tc.extraBlocks, tc.extraEvery)
			compare("after more blocks")
			if tc.every > 0 {
				if got, want := module.replayHeights.through(lc.node, lc.node.GetHeight()), lc.lendingHeights(); fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("the index holds %v, want %v", got, want)
				}
			}
			// A shorter horizon (an earlier tip) answers with only the heights up to it.
			half := lc.node.GetHeight() / 2
			var wantHalf []uint64
			for _, h := range lc.lendingHeights() {
				if h <= half {
					wantHalf = append(wantHalf, h)
				}
			}
			if got := module.replayHeights.through(lc.node, half); fmt.Sprint(got) != fmt.Sprint(wantHalf) {
				t.Fatalf("through(%d) = %v, want %v", half, got, wantHalf)
			}
		})
	}
}

// TestReplayReadsOnlyBlocksWithLendingTransactions: the rebuild used to read
// every block of the chain on every call.
func TestReplayReadsOnlyBlocksWithLendingTransactions(t *testing.T) {
	lc := newLendingChain(t, 9)
	lc.extend(t, 3000, 300)
	module := NewLendingModule(lc.node)

	module.warmReplayIndex()
	before := lc.store.gets.Load()
	if _, _, err := module.replayCommittedPoolState("default"); err != nil {
		t.Fatalf("replay: %v", err)
	}
	reads := lc.store.gets.Load() - before
	if limit := int64(len(lc.lending)) + 4; reads > limit {
		t.Fatalf("a replay over a warm index read the store %d times; %d blocks hold a lending transaction", reads, len(lc.lending))
	}

	// A chain with no lending transaction at all costs no block reads.
	empty := newLendingChain(t, 10)
	empty.extend(t, 2000, 0)
	m2 := NewLendingModule(empty.node)
	m2.warmReplayIndex()
	before = empty.store.gets.Load()
	market, users, err := m2.replayCommittedPoolState("default")
	if err != nil || market != nil || users != nil {
		t.Fatalf("a chain with no lending transaction should replay to nothing, got %v %v %v", market, users, err)
	}
	if reads := empty.store.gets.Load() - before; reads > 2 {
		t.Fatalf("replaying a chain with no lending transaction read the store %d times", reads)
	}

	// A new block costs one read to look at, no matter how long the chain.
	empty.extend(t, 1, 0)
	before = empty.store.gets.Load()
	m2.replayCommittedPoolState("default")
	if reads := empty.store.gets.Load() - before; reads > 3 {
		t.Fatalf("one new block cost %d reads", reads)
	}
}

func TestWarmReplayIndexCatchesUpAndStops(t *testing.T) {
	lc := newLendingChain(t, 12)
	lc.extend(t, 4000, 400)
	module := NewLendingModule(lc.node)
	done := make(chan struct{})
	go func() {
		module.WarmReplayIndex(context.Background())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(60 * time.Second): // only a failure waits this long
		t.Fatalf("the warm-up did not finish")
	}
	module.replayHeights.mu.Lock()
	scanned := module.replayHeights.scanned
	module.replayHeights.mu.Unlock()
	if scanned != lc.node.GetHeight() {
		t.Fatalf("the warm-up scanned to %d, the tip is %d", scanned, lc.node.GetHeight())
	}

	// A context that ends stops it before it is done.
	lc2 := newLendingChain(t, 13)
	lc2.extend(t, 20000, 0)
	m2 := NewLendingModule(lc2.node)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	finished := make(chan struct{})
	go func() {
		m2.WarmReplayIndex(ctx)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(30 * time.Second): // only a failure waits this long
		t.Fatalf("a cancelled warm-up kept running")
	}
	m2.replayHeights.mu.Lock()
	scanned = m2.replayHeights.scanned
	m2.replayHeights.mu.Unlock()
	if scanned >= lc2.node.GetHeight() {
		t.Fatalf("a cancelled warm-up still scanned the whole chain")
	}
}

// A store that fails to read a block once must not make the index forget that
// block for good: the walk this replaced read every block on every call, so a
// one-off failure cost one call, and it must not cost more than that now.

func heightsInclude(list []uint64, h uint64) bool {
	for _, v := range list {
		if v == h {
			return true
		}
	}
	return false
}

// indexUnderTest is a module whose index reads a clock the test moves.
func indexUnderTest(lc *lendingChain) (*LendingModule, *time.Time) {
	module := NewLendingModule(lc.node)
	clock := time.Unix(1_700_000_000, 0)
	module.replayHeights.now = func() time.Time { return clock }
	return module, &clock
}

func TestReplayFindsALendingBlockWhoseFirstReadFailed(t *testing.T) {
	lc := newLendingChain(t, 31)
	lc.extend(t, 400, 7)
	heights := lc.lendingHeights()
	if len(heights) < 6 {
		t.Fatalf("the test chain has only %d lending blocks", len(heights))
	}
	victim := heights[len(heights)/2]
	module, clock := indexUnderTest(lc)
	tip := lc.node.GetHeight()

	lc.store.failReads(lc.blockKey(t, victim), 1)
	first := module.replayHeights.through(lc.node, tip)
	if lc.store.failed.Load() != 1 {
		t.Fatalf("the injected failure was not hit (%d)", lc.store.failed.Load())
	}
	if heightsInclude(first, victim) {
		t.Fatalf("height %d was read although its read failed", victim)
	}

	// Asked again at once, the index does not go back to the failed block.
	before := lc.store.gets.Load()
	for i := 0; i < 25; i++ {
		module.replayHeights.through(lc.node, tip)
	}
	if reads := lc.store.gets.Load() - before; reads != 0 {
		t.Fatalf("calls inside the retry interval read the store %d times", reads)
	}

	// Once the interval has passed the block is read again, and this time it reads.
	*clock = clock.Add(lendingUnreadRetryAfter)
	if got, want := module.replayHeights.through(lc.node, tip), heights; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after the retry the index holds %v, want %v", got, want)
	}
	// It is settled: nothing is retried any more.
	before = lc.store.gets.Load()
	*clock = clock.Add(time.Hour)
	module.replayHeights.through(lc.node, tip)
	if reads := lc.store.gets.Load() - before; reads != 0 {
		t.Fatalf("a settled index read the store %d times", reads)
	}

	// And the replay the reads are answered from is the one of a store that never failed.
	for _, pool := range []string{"", "default", "poolB", "poolC"} {
		wantMarket, wantUsers, wantErr := module.legacyReplayCommittedPoolState(pool)
		gotMarket, gotUsers, gotErr := module.replayCommittedPoolState(pool)
		if (wantErr == nil) != (gotErr == nil) {
			t.Fatalf("pool %q: error mismatch %v / %v", pool, wantErr, gotErr)
		}
		if want, got := describeReplay(wantMarket, wantUsers), describeReplay(gotMarket, gotUsers); want != got {
			t.Fatalf("pool %q: replay differs after the retry\nwant %s\n got %s", pool, want, got)
		}
	}
}

// The failure is found while the index is being brought up in the background
// too, and a block that reads fine and holds nothing is never looked at again.
func TestWarmUpRecordsAFailedReadForARetry(t *testing.T) {
	lc := newLendingChain(t, 32)
	lc.extend(t, 3000, 300)
	heights := lc.lendingHeights()
	if len(heights) == 0 {
		t.Fatalf("the test chain has no lending block")
	}
	victim := heights[len(heights)-1]
	module, clock := indexUnderTest(lc)
	lc.store.failReads(lc.blockKey(t, victim), 1)

	module.WarmReplayIndex(context.Background())
	tip := lc.node.GetHeight()
	if lc.store.failed.Load() != 1 {
		t.Fatalf("the injected failure was not hit (%d)", lc.store.failed.Load())
	}
	if heightsInclude(module.replayHeights.through(lc.node, tip), victim) {
		t.Fatalf("height %d is in the index although its read failed", victim)
	}
	*clock = clock.Add(lendingUnreadRetryAfter)
	before := lc.store.gets.Load()
	got := module.replayHeights.through(lc.node, tip)
	if fmt.Sprint(got) != fmt.Sprint(heights) {
		t.Fatalf("after the retry the index holds %v, want %v", got, heights)
	}
	if reads := lc.store.gets.Load() - before; reads != 1 {
		t.Fatalf("the retry read the store %d times; one block had failed and every other block had been read", reads)
	}
}

// A block that keeps failing is tried once per interval, however many calls
// arrive, and is found as soon as it can be read.
func TestABlockThatStaysUnreadableIsRetriedOncePerInterval(t *testing.T) {
	lc := newLendingChain(t, 33)
	lc.extend(t, 300, 6)
	heights := lc.lendingHeights()
	victim := heights[1]
	module, clock := indexUnderTest(lc)
	tip := lc.node.GetHeight()
	key := lc.blockKey(t, victim)

	lc.store.failReads(key, -1)
	module.replayHeights.through(lc.node, tip)
	for round := 0; round < 4; round++ {
		before := lc.store.gets.Load()
		for i := 0; i < 50; i++ {
			if heightsInclude(module.replayHeights.through(lc.node, tip), victim) {
				t.Fatalf("round %d: an unreadable block is in the index", round)
			}
		}
		if reads := lc.store.gets.Load() - before; reads != 0 {
			t.Fatalf("round %d: 50 calls inside the interval read the store %d times", round, reads)
		}
		*clock = clock.Add(lendingUnreadRetryAfter)
		before = lc.store.gets.Load()
		module.replayHeights.through(lc.node, tip)
		if reads := lc.store.gets.Load() - before; reads != 1 {
			t.Fatalf("round %d: the first call after the interval read the store %d times, want the one failed block", round, reads)
		}
	}
	lc.store.failReads(key, 0)
	*clock = clock.Add(lendingUnreadRetryAfter)
	if got := module.replayHeights.through(lc.node, tip); fmt.Sprint(got) != fmt.Sprint(heights) {
		t.Fatalf("once the block can be read the index holds %v, want %v", got, heights)
	}
}
