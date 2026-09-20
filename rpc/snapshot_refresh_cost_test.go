package rpc

// TestSnapshotRefreshCost measures what one refresh of the explorer snapshot
// costs -- the work the background loop does after every new block -- and what
// the loop's other per-block step, advancing the all-time payment index, costs.
// It is a measurement, so it only runs when asked:
//
//	NHB_RPC_COST_TABLE=1 go test ./rpc -run TestSnapshotRefreshCost -v -count=1 -timeout 30m
//
// The chain is shaped like the live one (NHB_RPC_COST_BLOCKS blocks, default the
// live 196,693) on an on-disk store, and the numbers are wall time on one core
// and reads that reached the store.

import (
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

type refreshSample struct {
	wall  time.Duration
	reads int64
}

func summarizeRefresh(samples []refreshSample) (medWall, maxWall time.Duration, medReads, maxReads int64) {
	walls := make([]time.Duration, len(samples))
	reads := make([]int64, len(samples))
	for i, s := range samples {
		walls[i], reads[i] = s.wall, s.reads
	}
	sort.Slice(walls, func(i, j int) bool { return walls[i] < walls[j] })
	sort.Slice(reads, func(i, j int) bool { return reads[i] < reads[j] })
	return walls[len(walls)/2], walls[len(walls)-1], reads[len(reads)/2], reads[len(reads)-1]
}

func TestSnapshotRefreshCost(t *testing.T) {
	if os.Getenv("NHB_RPC_COST_TABLE") == "" {
		t.Skip("set NHB_RPC_COST_TABLE=1 to run the snapshot refresh cost measurement")
	}
	blocks := envInt("NHB_RPC_COST_BLOCKS", liveChainBlocks)
	fx := buildCostChain(t, costChainOpts{Blocks: blocks, LevelDB: os.Getenv("NHB_RPC_COST_MEM") == "", Seed: 42})
	srv := quietServer(t, fx.Node, costServerConfig())
	prevProcs := runtime.GOMAXPROCS(1)
	t.Cleanup(func() { runtime.GOMAXPROCS(prevProcs) })
	runtime.GC()

	refresh := func(window int) refreshSample {
		before := fx.Reads.gets.Load()
		start := time.Now()
		if err := refreshSnapshotForCost(srv, window); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		return refreshSample{time.Since(start), fx.Reads.gets.Load() - before}
	}

	first := refresh(explorerDefaultRecentBlocks)
	t.Logf("snapshot refresh, first after start (window %d):        %9.2f ms, %6d store reads", explorerDefaultRecentBlocks, float64(first.wall.Microseconds())/1000, first.reads)
	same := refresh(explorerDefaultRecentBlocks)
	t.Logf("snapshot refresh, same height again:                    %9.2f ms, %6d store reads", float64(same.wall.Microseconds())/1000, same.reads)

	var perBlock []refreshSample
	for i := 0; i < 30; i++ {
		fx.appendBlock(t)
		perBlock = append(perBlock, refresh(explorerDefaultRecentBlocks))
	}
	medWall, maxWall, medReads, maxReads := summarizeRefresh(perBlock)
	t.Logf("snapshot refresh after each new block (30 blocks):      median %6.2f ms max %6.2f ms; store reads median %d max %d",
		float64(medWall.Microseconds())/1000, float64(maxWall.Microseconds())/1000, medReads, maxReads)

	var wide []refreshSample
	for i := 0; i < 5; i++ {
		fx.appendBlock(t)
		wide = append(wide, refresh(explorerMaxRecentBlocks))
	}
	medWall, maxWall, medReads, maxReads = summarizeRefresh(wide)
	t.Logf("snapshot refresh, window %d, after each new block (5):  median %6.2f ms max %6.2f ms; store reads median %d max %d",
		explorerMaxRecentBlocks, float64(medWall.Microseconds())/1000, float64(maxWall.Microseconds())/1000, medReads, maxReads)

	// The loop's other per-block step: advance the all-time payment index.
	advance := func() refreshSample {
		before := fx.Reads.gets.Load()
		start := time.Now()
		srv.advanceExplorerActivityIndex()
		return refreshSample{time.Since(start), fx.Reads.gets.Load() - before}
	}
	prevBatch := explorerActivityBatchSize
	t.Cleanup(func() { explorerActivityBatchSize = prevBatch })
	batch := advance() // from an empty index: one batch
	t.Logf("activity index, one batch from empty (batch %d blocks): %9.2f ms, %6d store reads", prevBatch, float64(batch.wall.Microseconds())/1000, batch.reads)
	explorerActivityBatchSize = 1 << 30
	advance() // catch up to the tip
	var steady []refreshSample
	for i := 0; i < 30; i++ {
		fx.appendBlock(t)
		steady = append(steady, advance())
	}
	medWall, maxWall, medReads, maxReads = summarizeRefresh(steady)
	t.Logf("activity index, one new block per call (30 calls):      median %6.2f ms max %6.2f ms; store reads median %d max %d",
		float64(medWall.Microseconds())/1000, float64(maxWall.Microseconds())/1000, medReads, maxReads)
	idle := advance()
	t.Logf("activity index, nothing new:                            %9.2f ms, %6d store reads", float64(idle.wall.Microseconds())/1000, idle.reads)
}
