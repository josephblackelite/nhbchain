package rpc

// What the background loop's rebuild of the explorer snapshot costs, counted in
// reads that reach the block store. It uses only the shim in
// query_cost_shim_test.go, so it can be pointed at an older tree.

import (
	"testing"
)

// TestSnapshotRefreshReadsOnlyWhatIsNew: the loop rebuilds the snapshot after
// every block. Each rebuild used to read every block of its window again and, on
// a chain with little user-facing activity, up to 50,000 blocks of look-back;
// after the first, a rebuild now reads the block that is new and a couple of
// others, however long the chain and however wide the window.
func TestSnapshotRefreshReadsOnlyWhatIsNew(t *testing.T) {
	rc := buildRandomChain(t, 131, 3000, false)
	srv := quietServer(t, rc.Node, costServerConfig())
	settleActivityIndex(srv)

	refresh := func(window int) int64 {
		before := rc.Reads.gets.Load()
		if err := refreshSnapshotForCost(srv, window); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		return rc.Reads.gets.Load() - before
	}

	first := refresh(explorerDefaultRecentBlocks)
	if first < explorerDefaultRecentBlocks {
		t.Fatalf("the first rebuild read only %d blocks; something is answering without looking", first)
	}
	// The same height again: nothing new to read (the tip block is the one thing
	// that is looked at again, since it is never kept).
	if again := refresh(explorerDefaultRecentBlocks); again > 8 {
		t.Fatalf("a second rebuild at the same height read the store %d times", again)
	}
	worst := int64(0)
	for i := 0; i < 25; i++ {
		rc.Grow(1)
		settleActivityIndex(srv)
		if reads := refresh(explorerDefaultRecentBlocks); reads > worst {
			worst = reads
		}
	}
	if worst > 12 {
		t.Fatalf("a rebuild after one new block read the store %d times; it should read the new block and a few others, not the window (%d) or the look-back", worst, explorerDefaultRecentBlocks)
	}
	// A wider window costs its extra blocks once, then the same again.
	refresh(explorerMaxRecentBlocks)
	rc.Grow(1)
	if reads := refresh(explorerMaxRecentBlocks); reads > 12 {
		t.Fatalf("a rebuild of a %d-block window after one new block read the store %d times", explorerMaxRecentBlocks, reads)
	}
}
