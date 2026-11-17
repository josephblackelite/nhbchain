package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"

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

// A block that could not be read is not a fact about the block: the read may
// have failed once. It is remembered for a few seconds, and then read again.

func TestBlockSummaryCacheDoesNotKeepAFailureForGood(t *testing.T) {
	var c blockSummaryCache
	clock := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return clock }

	c.storeFailure(500)
	sum, ok := c.lookup(500)
	if !ok || sum.loaded() || sum.flags&sumKnown == 0 || sum.timestamp != 0 {
		t.Fatalf("a fresh failure should read as an unreadable block: %+v ok=%v", sum, ok)
	}
	clock = clock.Add(blockSummaryRetryAfter - time.Second)
	if _, ok := c.lookup(500); !ok {
		t.Fatalf("a failure was forgotten before the retry interval was over")
	}
	clock = clock.Add(time.Second)
	if _, ok := c.lookup(500); ok {
		t.Fatalf("a failure is still trusted after the retry interval")
	}
	// A clock that went backwards does not make a failure last longer.
	clock = time.Unix(1_600_000_000, 0)
	if _, ok := c.lookup(500); ok {
		t.Fatalf("a failure stamped in the future was trusted")
	}

	// What a later read finds replaces the failure, and stays.
	clock = time.Unix(1_700_000_100, 0)
	c.storeFailure(501)
	c.store(501, testSum(501))
	clock = clock.Add(time.Hour)
	if got, ok := c.lookup(501); !ok || got != testSum(501) {
		t.Fatalf("the summary of a block that was read did not replace its failure: %+v ok=%v", got, ok)
	}
	// A failure never replaces the summary of a block that was read.
	c.storeFailure(501)
	if got, ok := c.lookup(501); !ok || got != testSum(501) {
		t.Fatalf("a failure buried the summary of a block that was read: %+v ok=%v", got, ok)
	}
	// Growing the table keeps a failure that is still fresh.
	c.storeFailure(502)
	c.store(500+3*blockSummaryGrowStep, testSum(500))
	if _, ok := c.lookup(502); !ok {
		t.Fatalf("growing the table lost a fresh failure")
	}
}

func headerKey(t *testing.T, rc *randChain, height uint64) []byte {
	t.Helper()
	block, err := rc.Node.Chain().GetBlockByHeight(height)
	if err != nil {
		t.Fatalf("block %d: %v", height, err)
	}
	hash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block %d: %v", height, err)
	}
	return hash
}

func TestSummaryAtReadsABlockAgainAfterItsReadFailed(t *testing.T) {
	rc := buildRandomChain(t, 151, 120, false)
	srv := quietServer(t, rc.Node, costServerConfig())
	clock := time.Unix(1_700_000_000, 0)
	srv.blockSummaries.now = func() time.Time { return clock }
	chain := rc.Node.Chain()
	ctx := context.Background()
	victim := rc.Height - 10

	rc.Reads.failReads(headerKey(t, rc, victim), 1)
	sum, err := srv.summaryAt(ctx, chain, victim)
	if err != nil || sum.loaded() {
		t.Fatalf("the read that fails should give an unreadable block: %+v, %v", sum, err)
	}
	if rc.Reads.failed.Load() != 1 {
		t.Fatalf("the injected failure was not hit (%d)", rc.Reads.failed.Load())
	}

	// Asked again at once it is not read again.
	before := rc.Reads.gets.Load()
	for i := 0; i < 20; i++ {
		if sum, _ := srv.summaryAt(ctx, chain, victim); sum.loaded() {
			t.Fatalf("a failure inside the retry interval was answered with a block")
		}
	}
	if reads := rc.Reads.gets.Load() - before; reads != 0 {
		t.Fatalf("calls inside the retry interval read the store %d times", reads)
	}

	// After the interval it is read, and what is found is kept for good.
	clock = clock.Add(blockSummaryRetryAfter)
	sum, err = srv.summaryAt(ctx, chain, victim)
	if err != nil || !sum.readable() {
		t.Fatalf("the block was not read again once the interval had passed: %+v, %v", sum, err)
	}
	clock = clock.Add(time.Hour)
	before = rc.Reads.gets.Load()
	if again, _ := srv.summaryAt(ctx, chain, victim); again != sum {
		t.Fatalf("the summary changed: %+v then %+v", sum, again)
	}
	if reads := rc.Reads.gets.Load() - before; reads != 0 {
		t.Fatalf("a block that was read is read again (%d reads)", reads)
	}
}

// A block that keeps failing costs one read per interval, not one per call.
func TestABlockThatStaysUnreadableIsReadOncePerInterval(t *testing.T) {
	rc := buildRandomChain(t, 153, 60, false)
	srv := quietServer(t, rc.Node, costServerConfig())
	clock := time.Unix(1_700_000_000, 0)
	srv.blockSummaries.now = func() time.Time { return clock }
	chain := rc.Node.Chain()
	ctx := context.Background()
	victim := rc.Height - 3
	rc.Reads.failReads(headerKey(t, rc, victim), -1)

	for round := 0; round < 4; round++ {
		before := rc.Reads.gets.Load()
		for i := 0; i < 50; i++ {
			if sum, _ := srv.summaryAt(ctx, chain, victim); sum.loaded() {
				t.Fatalf("round %d: an unreadable block was reported as read", round)
			}
		}
		if reads := rc.Reads.gets.Load() - before; reads != 1 {
			t.Fatalf("round %d: 50 calls of a block that cannot be read made %d reads, want 1", round, reads)
		}
		clock = clock.Add(blockSummaryRetryAfter)
	}
}

// nhb_getLatestTransactions used to stop at the first block it could not read
// and, with the failure remembered for good, stopped there on every call until
// the node was restarted.
func TestLatestTransactionsRecoverAfterAOneOffReadFailure(t *testing.T) {
	rc := buildRandomChain(t, 152, 200, false)
	seen := map[uint64]bool{}
	var withTxs []uint64
	for _, h := range rc.HashAt {
		if !seen[h] {
			seen[h] = true
			withTxs = append(withTxs, h)
		}
	}
	sort.Slice(withTxs, func(i, j int) bool { return withTxs[i] > withTxs[j] })
	if len(withTxs) < 10 {
		t.Fatalf("the test chain has only %d blocks with transactions", len(withTxs))
	}
	victim := withTxs[1]

	latest := func(srv *Server) []json.RawMessage {
		t.Helper()
		call := doRPC(t, srv, nil, "203.0.113.50:1", "nhb_getLatestTransactions", 50)
		if call.Code != 200 || call.Resp.Error != nil {
			t.Fatalf("nhb_getLatestTransactions: HTTP %d %s", call.Code, call.Body)
		}
		var out struct {
			Result []json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(call.Body, &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out.Result
	}
	healthy := quietServer(t, rc.Node, costServerConfig())
	want := latest(healthy)
	if len(want) != 50 {
		t.Fatalf("a healthy node returned %d transactions, want 50", len(want))
	}

	srv := quietServer(t, rc.Node, costServerConfig())
	clock := time.Unix(1_700_000_000, 0)
	srv.blockSummaries.now = func() time.Time { return clock }
	rc.Reads.failReads(headerKey(t, rc, victim), 1)
	if got := latest(srv); len(got) >= len(want) {
		t.Fatalf("with block %d unreadable the list should end there, it has %d of %d transactions", victim, len(got), len(want))
	}
	if rc.Reads.failed.Load() != 1 {
		t.Fatalf("the injected failure was not hit (%d)", rc.Reads.failed.Load())
	}

	clock = clock.Add(blockSummaryRetryAfter)
	got := latest(srv)
	if len(got) != len(want) {
		t.Fatalf("after the retry the list has %d transactions, want %d", len(got), len(want))
	}
	for i := range want {
		if string(got[i]) != string(want[i]) {
			t.Fatalf("transaction %d differs after the retry", i)
		}
	}
}

// TestBlockSummaryFailuresNeverBuryASummaryUnderConcurrency: readers that lost a
// race and record a failure for a block another one read must not undo it.
func TestBlockSummaryFailuresNeverBuryASummaryUnderConcurrency(t *testing.T) {
	var c blockSummaryCache
	clock := time.Unix(1_700_000_000, 0)
	c.now = func() time.Time { return clock }
	const heights = 4000
	// Cover the whole range first, so that the entries below are written in
	// place and none is lost to the table being replaced.
	c.storeFailure(0)
	c.storeFailure(heights)

	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		w := w
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 20000; i++ {
				h := uint64(1 + rng.Intn(heights-1))
				switch {
				case w%2 == 0:
					c.storeFailure(h)
				case rng.Intn(4) == 0:
					c.store(h, testSum(h))
				default:
					if got, ok := c.lookup(h); ok && got.loaded() && got != testSum(h) {
						t.Errorf("height %d: read %+v", h, got)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	// Every height whose summary was stored must read as that summary now, however
	// many failures were recorded after it.
	for h := uint64(1); h < heights; h++ {
		c.store(h, testSum(h))
	}
	var ranAgain sync.WaitGroup
	for w := 0; w < 8; w++ {
		ranAgain.Add(1)
		go func() {
			defer ranAgain.Done()
			for h := uint64(1); h < heights; h++ {
				c.storeFailure(h)
			}
		}()
	}
	ranAgain.Wait()
	for h := uint64(1); h < heights; h++ {
		if got, ok := c.lookup(h); !ok || got != testSum(h) {
			t.Fatalf("height %d: a failure buried its summary: %+v ok=%v", h, got, ok)
		}
	}
}
