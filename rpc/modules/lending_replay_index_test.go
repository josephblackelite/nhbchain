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
	case <-time.After(20 * time.Second):
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
	case <-time.After(5 * time.Second):
		t.Fatalf("a cancelled warm-up kept running")
	}
	m2.replayHeights.mu.Lock()
	scanned = m2.replayHeights.scanned
	m2.replayHeights.mu.Unlock()
	if scanned >= lc2.node.GetHeight() {
		t.Fatalf("a cancelled warm-up still scanned the whole chain")
	}
}
