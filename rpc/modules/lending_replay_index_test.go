package modules

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/native/lending"
)

// catchUpFully brings the index up to the chain tip, as the reads and the
// background warm-up do, one bounded step after another.
func catchUpFully(t testing.TB, m *LendingModule) {
	t.Helper()
	for i := 0; i < 1_000_000; i++ {
		if m.replayHeights.catchUp(m.node, m.node.GetHeight()) {
			return
		}
	}
	t.Fatalf("the index did not catch up with the chain")
}

// through brings the index up to height, however many steps that takes, and
// returns the ascending heights of the blocks it holds lending events for.
func (x *lendingHeightIndex) through(node *core.Node, height uint64) []uint64 {
	for !x.catchUp(node, height) {
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	seen := map[uint64]bool{}
	for _, entry := range x.pools {
		for _, ev := range entry.events {
			if ev.height <= height {
				seen[ev.height] = true
			}
		}
	}
	out := make([]uint64, 0, len(seen))
	for h := range seen {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// replayAll is the whole rebuild of a pool from the index, accounts and all, in
// the shape legacyReplayCommittedPoolState returns it.
func (m *LendingModule) replayAll(t testing.TB, poolID string) (*lending.Market, map[[20]byte]*lending.UserAccount) {
	t.Helper()
	id := strings.TrimSpace(poolID)
	if id == "" {
		id = defaultLendingPoolID
	}
	catchUpFully(t, m)
	x := &m.replayHeights
	x.mu.Lock()
	defer x.mu.Unlock()
	entry := x.pools[id]
	if entry == nil || len(entry.events) == 0 {
		return nil, nil
	}
	f := foldLocked(entry)
	replay := &lendingPoolReplay{
		supplyShares:    cloneBigInt(f.market.TotalSupplyShares),
		supplied:        cloneBigInt(f.market.TotalNHBSupplied),
		borrowed:        cloneBigInt(f.market.TotalNHBBorrowed),
		lastUpdateBlock: f.market.LastUpdateBlock,
		users:           make(map[[20]byte]*lending.UserAccount, len(f.users)),
	}
	for addr, user := range f.users {
		replay.users[addr] = cloneLendingUser(user)
	}
	return m.replayedMarket(id, replay), replay.users
}

func describeUser(addr [20]byte, u *lending.UserAccount) string {
	if u == nil {
		return "nil"
	}
	return fmt.Sprintf("%x:%v,%v,%v,%v,%d", addr, u.CollateralZNHB, u.SupplyShares, u.DebtNHB, u.ScaledDebt, u.LastSupplyBlock)
}

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
					id := strings.TrimSpace(pool)
					if id == "" {
						id = defaultLendingPoolID
					}
					wantMarket, wantUsers, wantErr := module.legacyReplayCommittedPoolState(pool)
					if wantErr != nil {
						t.Fatalf("%s pool %q: the full walk failed: %v", stage, pool, wantErr)
					}
					for pass := 0; pass < 2; pass++ {
						gotMarket, gotUsers := module.replayAll(t, pool)
						if want, got := describeReplay(wantMarket, wantUsers), describeReplay(gotMarket, gotUsers); want != got {
							t.Fatalf("%s pool %q pass %d: replay differs\nwant %s\n got %s", stage, pool, pass, want, got)
						}
					}
					// The reads take one account at a time: each must be the account of the
					// full walk, and an address the pool has never seen must have none.
					unseen := [20]byte{0xEE, 0x01}
					replay, moduleErr := module.replayPool(id, unseen)
					if moduleErr != nil {
						t.Fatalf("%s pool %q: replay: %v", stage, pool, moduleErr)
					}
					if (wantMarket == nil) != (replay == nil) {
						t.Fatalf("%s pool %q: the full walk found a market: %v, the index: %v", stage, pool, wantMarket != nil, replay != nil)
					}
					if replay == nil {
						continue
					}
					if len(replay.users) != 0 {
						t.Fatalf("%s pool %q: an address with no transaction has an account", stage, pool)
					}
					for addr, want := range wantUsers {
						replay, moduleErr := module.replayPool(id, addr)
						if moduleErr != nil {
							t.Fatalf("%s pool %q: replay: %v", stage, pool, moduleErr)
						}
						if got := replay.users[addr]; describeUser(addr, want) != describeUser(addr, got) {
							t.Fatalf("%s pool %q: account differs\nwant %s\n got %s", stage, pool, describeUser(addr, want), describeUser(addr, got))
						}
						if got, want := describeReplay(module.replayedMarket(id, replay), nil), describeReplay(wantMarket, nil); got != want {
							t.Fatalf("%s pool %q: market differs\nwant %s\n got %s", stage, pool, want, got)
						}
					}
				}
			}
			lc.extend(t, tc.blocks, tc.every)
			compare("first")
			// More blocks arrive in several steps; the sums are brought forward and
			// nothing else changes.
			for step := 0; step < 4; step++ {
				lc.extend(t, tc.extraBlocks/4, tc.extraEvery)
				compare(fmt.Sprintf("after more blocks, step %d", step))
			}
			if tc.every > 0 {
				if got, want := module.replayHeights.through(lc.node, lc.node.GetHeight()), lc.lendingHeights(); fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("the index holds %v, want %v", got, want)
				}
			}
		})
	}
}

// TestReplayOverACurrentIndexReadsNoBlocks: the rebuild used to read every block
// that holds a lending transaction, on every call.
func TestReplayOverACurrentIndexReadsNoBlocks(t *testing.T) {
	lc := newLendingChain(t, 9)
	lc.extend(t, 3000, 30)
	module := NewLendingModule(lc.node)

	catchUpFully(t, module)
	var addr [20]byte
	addr[0] = 0x31
	before := lc.store.gets.Load()
	for i := 0; i < 5; i++ {
		replay, moduleErr := module.replayPool("default", addr)
		if moduleErr != nil || replay == nil {
			t.Fatalf("replay: %v %v", replay, moduleErr)
		}
	}
	if reads := lc.store.gets.Load() - before; reads != 0 {
		t.Fatalf("five replays over a current index read the store %d times; %d blocks hold a lending transaction", reads, len(lc.lending))
	}

	// A chain with no lending transaction at all costs no block reads.
	empty := newLendingChain(t, 10)
	empty.extend(t, 2000, 0)
	m2 := NewLendingModule(empty.node)
	catchUpFully(t, m2)
	before = empty.store.gets.Load()
	replay, moduleErr := m2.replayPool("default")
	if moduleErr != nil || replay != nil {
		t.Fatalf("a chain with no lending transaction should replay to nothing, got %v %v", replay, moduleErr)
	}
	if reads := empty.store.gets.Load() - before; reads > 2 {
		t.Fatalf("replaying a chain with no lending transaction read the store %d times", reads)
	}

	// A new block costs one read to look at, no matter how long the chain.
	empty.extend(t, 1, 0)
	before = empty.store.gets.Load()
	if _, moduleErr := m2.replayPool("default"); moduleErr != nil {
		t.Fatalf("replay: %v", moduleErr)
	}
	if reads := empty.store.gets.Load() - before; reads > 3 {
		t.Fatalf("one new block cost %d reads", reads)
	}
}

// The sum of a pool is brought forward over the events that arrived since it was
// last made, not made again, and is made again only when a block found late puts
// events in the middle.
func TestAPoolSumIsBroughtForwardNotMadeAgain(t *testing.T) {
	lc := newLendingChain(t, 51)
	lc.extend(t, 300, 2)
	module, clock := indexUnderTest(lc)
	catchUpFully(t, module)
	if _, moduleErr := module.replayPool("poolB"); moduleErr != nil {
		t.Fatalf("replay: %v", moduleErr)
	}
	entry := func() *lendingPoolLog {
		module.replayHeights.mu.Lock()
		defer module.replayHeights.mu.Unlock()
		return module.replayHeights.pools["poolB"]
	}
	first := entry().fold
	if first == nil || first.n != len(entry().events) {
		t.Fatalf("no sum was kept over the events of the pool")
	}

	lc.extend(t, 100, 2)
	catchUpFully(t, module)
	if _, moduleErr := module.replayPool("poolB"); moduleErr != nil {
		t.Fatalf("replay: %v", moduleErr)
	}
	if entry().fold != first {
		t.Fatalf("the sum was made again from the start for events that came after it")
	}
	if first.n != len(entry().events) {
		t.Fatalf("the sum covers %d of %d events", first.n, len(entry().events))
	}

	// A block that could not be read and is found later puts events in the middle.
	lc.extend(t, 200, 2)
	// The victim is a block above the ones the index has examined that holds a
	// transaction for the pool.
	var victim uint64
	for _, h := range lc.lendingHeights() {
		if h <= 400 || victim != 0 {
			continue
		}
		block, err := lc.node.GetBlockByHeight(h)
		if err != nil {
			t.Fatalf("block %d: %v", h, err)
		}
		for _, tx := range block.Transactions {
			if id, err := lendingPoolIDFromTxData(tx.Data); err == nil && isCommittedLendingTxType(tx.Type) && id == "poolB" {
				victim = h
			}
		}
	}
	if victim == 0 {
		t.Fatalf("the test chain has no block above 400 with a transaction for the pool")
	}
	lc.store.failReads(lc.blockKey(t, victim), 1)
	catchUpFully(t, module)
	lc.store.failReads(lc.blockKey(t, victim), 0)
	if _, moduleErr := module.replayPool("poolB"); moduleErr != nil {
		t.Fatalf("replay: %v", moduleErr)
	}
	before := entry().fold
	*clock = clock.Add(lendingUnreadRetryAfter)
	catchUpFully(t, module)
	if entry().fold != nil {
		t.Fatalf("the sum was kept although an event was put in the middle of the list")
	}
	wantMarket, wantUsers, _ := module.legacyReplayCommittedPoolState("poolB")
	gotMarket, gotUsers := module.replayAll(t, "poolB")
	if want, got := describeReplay(wantMarket, wantUsers), describeReplay(gotMarket, gotUsers); want != got {
		t.Fatalf("after the late block the replay differs\nwant %s\n got %s", want, got)
	}
	if entry().fold == before {
		t.Fatalf("the sum was not made again")
	}
}

// A read for a pool no committed lending transaction names answers at once and
// keeps nothing, whatever the name.
func TestUnknownPoolNamesAddNothingToTheIndex(t *testing.T) {
	lc := newLendingChain(t, 52)
	lc.extend(t, 400, 3)
	module := NewLendingModule(lc.node)
	catchUpFully(t, module)
	pools := func() int {
		module.replayHeights.mu.Lock()
		defer module.replayHeights.mu.Unlock()
		return len(module.replayHeights.pools)
	}
	known := pools()
	if known == 0 {
		t.Fatalf("the test chain has no lending transactions")
	}
	var addr [20]byte
	addr[0] = 0x77
	for i := 0; i < 500; i++ {
		name := fmt.Sprintf("no-such-pool-%d", i)
		if _, _, moduleErr := module.GetMarket(name); moduleErr != nil {
			t.Fatalf("GetMarket(%q): %v", name, moduleErr)
		}
		if account, moduleErr := module.GetUserAccount(name, addr); moduleErr != nil || account != nil {
			t.Fatalf("GetUserAccount(%q) = %v, %v", name, account, moduleErr)
		}
	}
	if got := pools(); got != known {
		t.Fatalf("the index holds %d pools after reads for unknown pools, it held %d", got, known)
	}
}

// A step of catching up ends after the block that spends its budget. The index
// reads the time from a clock the test moves a millisecond with every look, so
// a budget of three milliseconds is three blocks.
func TestACatchUpStepEndsWhenItsBudgetIsSpent(t *testing.T) {
	lc := newLendingChain(t, 53)
	lc.extend(t, 60, 2)
	module := NewLendingModule(lc.node)
	prev := lendingScanBudget
	lendingScanBudget = 3 * time.Millisecond
	t.Cleanup(func() { lendingScanBudget = prev })
	tick := time.Unix(1_700_000_000, 0)
	module.replayHeights.now = func() time.Time {
		tick = tick.Add(time.Millisecond)
		return tick
	}

	tip := lc.node.GetHeight()
	for step := uint64(1); ; step++ {
		current := module.replayHeights.catchUp(module.node, tip)
		module.replayHeights.mu.Lock()
		scanned := module.replayHeights.scanned
		module.replayHeights.mu.Unlock()
		want := 3 * step
		if want > tip {
			want = tip
		}
		if scanned != want {
			t.Fatalf("step %d examined up to height %d, want %d", step, scanned, want)
		}
		if current != (want == tip) {
			t.Fatalf("step %d: current = %v", step, current)
		}
		if current {
			return
		}
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
		if wantErr != nil {
			t.Fatalf("pool %q: the full walk failed: %v", pool, wantErr)
		}
		gotMarket, gotUsers := module.replayAll(t, pool)
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
