package rpc

import (
	"errors"
	"math/rand"
	"sync"
	"testing"

	"nhbchain/core/types"
)

func TestBlockSummaryPacking(t *testing.T) {
	cases := []blockSummary{
		{},
		{timestamp: 1, txCount: 0, flags: sumKnown},
		{timestamp: 1_789_900_100, txCount: 3, flags: sumKnown | sumLoaded | sumHasHeader | sumUserFacing},
		{timestamp: 1<<summaryTsBits - 1, txCount: 1<<summaryTxBits - 1, flags: sumKnown | sumLoaded},
	}
	for _, in := range cases {
		v, ok := packSummary(in)
		if !ok {
			t.Fatalf("%+v should fit", in)
		}
		if got := unpackSummary(v); got != in {
			t.Fatalf("round trip: %+v -> %#x -> %+v", in, v, got)
		}
	}
	// A summary that does not fit is not cached rather than cached wrong.
	for _, in := range []blockSummary{
		{timestamp: 1 << summaryTsBits, flags: sumKnown},
		{timestamp: -1, flags: sumKnown},
		{txCount: 1 << summaryTxBits, flags: sumKnown},
	} {
		if _, ok := packSummary(in); ok {
			t.Fatalf("%+v should not fit", in)
		}
	}
	var c blockSummaryCache
	c.store(5, blockSummary{timestamp: -1, flags: sumKnown})
	if _, ok := c.lookup(5); ok {
		t.Fatalf("an unrepresentable summary was cached")
	}
}

func testSum(h uint64) blockSummary {
	return blockSummary{timestamp: int64(1_000_000 + h), txCount: uint32(h % 7), flags: sumKnown | sumLoaded | sumHasHeader}
}

func TestBlockSummaryCacheGrowsInBothDirections(t *testing.T) {
	var c blockSummaryCache
	if _, ok := c.lookup(0); ok {
		t.Fatalf("an empty cache knows nothing")
	}
	// A scan walks down from the tip, one block at a time.
	for h := uint64(30000); h >= 10000; h-- {
		c.store(h, testSum(h))
	}
	// Then the tip moves up.
	for h := uint64(30001); h <= 30100; h++ {
		c.store(h, testSum(h))
	}
	for h := uint64(10000); h <= 30100; h++ {
		got, ok := c.lookup(h)
		if !ok || got != testSum(h) {
			t.Fatalf("height %d: got %+v ok=%v, want %+v", h, got, ok, testSum(h))
		}
	}
	for _, h := range []uint64{0, 9999, 40000} {
		if _, ok := c.lookup(h); ok && h != 9999 {
			t.Fatalf("height %d was never stored", h)
		}
	}
	// A block never examined inside the covered range is a miss, not a zero summary.
	c.store(50000, testSum(50000))
	if _, ok := c.lookup(45000); ok {
		t.Fatalf("an entry that was never stored reads as known")
	}
}

func TestBlockSummaryCacheKeepsOnlyTheMostRecentHeights(t *testing.T) {
	c := blockSummaryCache{max: 1000}
	for h := uint64(1); h <= 5000; h++ {
		c.store(h, testSum(h))
	}
	if got, ok := c.lookup(5000); !ok || got != testSum(5000) {
		t.Fatalf("the newest height must be kept")
	}
	if _, ok := c.lookup(100); ok {
		t.Fatalf("a height far below the bound was kept")
	}
	table := c.table.Load()
	if len(table.sums) > 1000 {
		t.Fatalf("the table holds %d heights, the bound is 1000", len(table.sums))
	}
	// Storing something older than the bound is a no-op, not a reallocation.
	c.store(50, testSum(50))
	if _, ok := c.lookup(50); ok {
		t.Fatalf("a height below the bound was stored")
	}
	// The recent window is intact.
	for h := uint64(4500); h <= 5000; h++ {
		if got, ok := c.lookup(h); !ok || got != testSum(h) {
			t.Fatalf("height %d lost", h)
		}
	}
}

// TestBlockSummaryCacheConcurrentUse hammers reads, writes and growth from many
// goroutines; every value read back must be the one written for that height.
func TestBlockSummaryCacheConcurrentUse(t *testing.T) {
	var c blockSummaryCache
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 20000; i++ {
				h := uint64(rng.Intn(60000))
				if rng.Intn(3) == 0 {
					c.store(h, testSum(h))
				} else if got, ok := c.lookup(h); ok && got != testSum(h) {
					t.Errorf("height %d: read %+v, want %+v", h, got, testSum(h))
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestSummarizeBlockMatchesTheScansDefinitions(t *testing.T) {
	for _, s := range []blockSummary{summarizeBlock(nil, nil), summarizeBlock(&types.Block{}, errors.New("unreadable"))} {
		if s.loaded() || s.userFacing() || s.readable() || s.flags&sumKnown == 0 {
			t.Fatalf("a block that could not be read: %+v", s)
		}
	}
	header := &types.BlockHeader{Height: 5, Timestamp: 1234}
	tx := func(typ types.TxType) *types.Transaction { return &types.Transaction{Type: typ} }

	empty := summarizeBlock(&types.Block{Header: header}, nil)
	if !empty.loaded() || !empty.readable() || empty.userFacing() || empty.txCount != 0 || empty.timestamp != 1234 {
		t.Fatalf("an empty block: %+v", empty)
	}
	noHeader := summarizeBlock(&types.Block{Transactions: []*types.Transaction{tx(types.TxTypeTransfer)}}, nil)
	if !noHeader.loaded() || noHeader.readable() || noHeader.userFacing() || noHeader.txCount != 1 {
		t.Fatalf("a block with no header: %+v", noHeader)
	}
	system := summarizeBlock(&types.Block{Header: header, Transactions: []*types.Transaction{tx(types.TxTypeHeartbeat), tx(types.TxTypeBuybackRefPrice), tx(types.TxTypeLendingRefPrice)}}, nil)
	if system.userFacing() || !system.readable() || system.txCount != 3 {
		t.Fatalf("a block of heartbeats and reference prices is not user-facing: %+v", system)
	}
	for _, typ := range []types.TxType{types.TxTypeTransfer, types.TxTypeTransferZNHB, types.TxTypeMint, types.TxTypeStake, types.TxTypeBuyZNHB} {
		mixed := summarizeBlock(&types.Block{Header: header, Transactions: []*types.Transaction{tx(types.TxTypeHeartbeat), nil, tx(typ)}}, nil)
		if !mixed.userFacing() || mixed.txCount != 3 {
			t.Fatalf("a block holding a %v transaction is user-facing: %+v", typ, mixed)
		}
	}
}
