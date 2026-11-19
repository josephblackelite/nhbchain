package core

// How long the public reads that walk a large set keep the node's state lock.
// Block validation and commit need that lock for writing, so the time a read
// holds it is time a block waits. The reads below take what they need from the
// state under the lock and do the rest of their work after letting go of it.
//
// The tests do not time the reads. They measure how often the lock is free while
// a read runs in a loop, which a read that holds the lock for the whole of its
// work cannot pass however fast the machine is.

import (
	"encoding/binary"
	"math/big"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/potso"
	"nhbchain/native/subscriptions"
)

const (
	// The window over which the share of free lock is measured, and the share a
	// read has to leave. A read that works under the lock leaves almost none; a
	// read that only copies under it leaves nearly all.
	stateScopeWindow    = 250 * time.Millisecond
	stateScopeMinShare  = 0.5
	stateScopeAttempts  = 3
	stateScopeMinRounds = 3
	stateScopeMaxWait   = 20 * time.Second
)

// stateLockFreeShare runs work in a loop on its own goroutine for the length of
// window and, meanwhile, keeps asking for the state lock for writing without
// waiting for it. It returns the share of those asks that found it free and how
// many times work finished.
func stateLockFreeShare(node *Node, window time.Duration, work func()) (float64, int64) {
	var (
		stop   atomic.Bool
		rounds atomic.Int64
		wg     sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for !stop.Load() {
			work()
			rounds.Add(1)
		}
	}()
	// On a loaded machine one run of work can take longer than the window, so the
	// measurement goes on until a few runs have finished (within a generous cap).
	var free, asked int
	start := time.Now()
	for deadline := start.Add(window); time.Now().Before(deadline) || (rounds.Load() < stateScopeMinRounds && time.Since(start) < stateScopeMaxWait); {
		if node.stateMu.TryLock() {
			node.stateMu.Unlock()
			free++
		}
		asked++
		runtime.Gosched()
	}
	stop.Store(true)
	wg.Wait()
	return float64(free) / float64(asked), rounds.Load()
}

// requireStateLockMostlyFree fails unless, in at least one of a few attempts, the
// state lock was free for at least half of the time work ran. Wall-clock effects
// of a loaded machine can make one attempt look bad; a read that works under the
// lock looks bad in every attempt.
func requireStateLockMostlyFree(t *testing.T, node *Node, name string, work func()) {
	t.Helper()
	var shares []float64
	for attempt := 0; attempt < stateScopeAttempts; attempt++ {
		share, rounds := stateLockFreeShare(node, stateScopeWindow, work)
		if rounds < stateScopeMinRounds {
			t.Fatalf("%s finished only %d times in %s; too slow to tell whether it holds the state lock", name, rounds, stateScopeWindow)
		}
		if share >= stateScopeMinShare {
			t.Logf("%s: the state lock was free %.0f%% of the time over %d runs", name, share*100, rounds)
			return
		}
		shares = append(shares, share)
	}
	t.Fatalf("%s held the state lock for most of its work in each of %d attempts (share of the time the lock was free: %v, want at least %.2f)", name, len(shares), shares, stateScopeMinShare)
}

// commitFixture stores what a test wrote to the node's state in the trie database,
// the way a committed block leaves it: nothing pending, so a copy of the state
// is small however much the state holds.
func commitFixture(t *testing.T, node *Node) {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	if _, err := node.state.Commit(1); err != nil {
		t.Fatalf("commit fixture: %v", err)
	}
}

func fixtureAddress(n uint64) [20]byte {
	var addr [20]byte
	binary.BigEndian.PutUint64(addr[12:], n)
	return addr
}

func fixtureEvent(eventType string, n int) types.Event {
	return types.Event{Type: eventType, Attributes: map[string]string{
		"n": strconv.Itoa(n), "from": "alpha", "to": "beta", "amount": "1000",
	}}
}

// fillEventLog commits events to the node's log: plain ones and, as the log
// keeps them apart, escrow events, so that both of its rings are in use.
func fillEventLog(node *Node, plain, pinned int) {
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	for i := 0; i < plain; i++ {
		evt := fixtureEvent("test.event", i)
		node.state.AppendEvent(&evt)
	}
	for i := 0; i < pinned; i++ {
		evt := fixtureEvent("escrow.created", plain+i)
		node.state.AppendEvent(&evt)
	}
	node.retainCommittedEventsLocked()
}

// Reading the node's events while blocks are validated and committed must not
// make them wait for a copy of every retained event.
func TestEventsDoNotHoldTheStateLockWhileCopying(t *testing.T) {
	node := newTestNode(t)
	fillEventLog(node, maxRetainedEvents, 20_000)
	if got := len(node.Events()); got < maxRetainedEvents+20_000 {
		t.Fatalf("the node holds %d events, want the %d that were committed", got, maxRetainedEvents+20_000)
	}
	requireStateLockMostlyFree(t, node, "Events", func() { node.Events() })
}

// What Events returns belongs to the caller: changing it does not change what the
// node reports next, and the pending events of the live state come after the
// committed ones in order.
func TestEventsAreCopiesTheCallerMayChange(t *testing.T) {
	node := newTestNode(t)
	for i := 1; i <= 4; i++ {
		push := fixtureEvent("test.event", i)
		node.stateMu.Lock()
		node.state.AppendEvent(&push)
		if i == 3 {
			node.retainCommittedEventsLocked()
		}
		node.stateMu.Unlock()
	}
	// Events 1-3 are committed, event 4 is pending on the live state.
	first := node.Events()
	if got := eventNumbers(first); !reflect.DeepEqual(got, []string{"1", "2", "3", "4"}) {
		t.Fatalf("the node reports %v, want [1 2 3 4]", got)
	}
	for i := range first {
		first[i].Attributes["n"] = "changed"
		first[i].Attributes["extra"] = "x"
	}
	first[0] = types.Event{Type: "other"}
	second := node.Events()
	if got := eventNumbers(second); !reflect.DeepEqual(got, []string{"1", "2", "3", "4"}) {
		t.Fatalf("changing what Events returned changed what it returns next: %v", got)
	}
	for _, evt := range second {
		if _, ok := evt.Attributes["extra"]; ok {
			t.Fatalf("an attribute added to a returned event showed up in %v", evt)
		}
	}
}

// A node without an event log, or without any events, reports what it did before
// the copy moved out of the lock: nothing at all as a nil slice, and the pending
// events of a node that has no log.
func TestEventsOfANodeWithoutALog(t *testing.T) {
	sp := &StateProcessor{}
	node := &Node{state: sp}
	if got := node.Events(); got != nil {
		t.Fatalf("a node with no log and no events reports %v, want nil", got)
	}
	pending := fixtureEvent("test.event", 7)
	sp.AppendEvent(&pending)
	if got := eventNumbers(node.Events()); !reflect.DeepEqual(got, []string{"7"}) {
		t.Fatalf("a node with no log reports %v, want its pending event [7]", got)
	}
	node.eventLog = newEventLog(10, 10)
	if got := node.Events(); got == nil || len(got) != 1 {
		t.Fatalf("a node with a log reports %v, want its one pending event", got)
	}
	var missing *Node
	if got := missing.Events(); got != nil {
		t.Fatalf("a missing node reports %v", got)
	}
	if got := (&Node{}).Events(); got != nil {
		t.Fatalf("a node with no state reports %v", got)
	}
}

// The view of the pending events is taken under the lock and read after it is
// released, and a transaction that is rolled back cuts the live slice back and
// reuses its space. The view must not change when that happens.
func TestPendingEventsViewIsNotChangedByALaterRollback(t *testing.T) {
	sp := &StateProcessor{}
	for i := 1; i <= 3; i++ {
		evt := fixtureEvent("test.event", i)
		sp.AppendEvent(&evt)
	}
	view := sp.pendingEventsView()

	sp.events = sp.events[:1]
	for i := 10; i <= 11; i++ {
		evt := fixtureEvent("test.event", i)
		sp.AppendEvent(&evt)
	}
	if got := eventNumbers(view); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("the view changed after a rollback: %v, want [1 2 3]", got)
	}
	if got := eventNumbers(sp.pendingEventsView()); !reflect.DeepEqual(got, []string{"1", "10", "11"}) {
		t.Fatalf("the live events are %v, want [1 10 11]", got)
	}
	var missing *StateProcessor
	if got := missing.pendingEventsView(); got != nil {
		t.Fatalf("a missing processor has events %v", got)
	}
}

// The view of the log shares the events' attribute maps; the copy does not.
func TestEventLogViewIsReadableAfterTheLogMovesOn(t *testing.T) {
	log := newEventLog(3, 2)
	log.append(numberedEvents(1, 3))
	view := log.view()
	copied := copyEvents(view)
	log.append(numberedEvents(4, 9)) // replaces every event the view was taken of
	if got := eventNumbers(view); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("the view changed when the log moved on: %v", got)
	}
	if got := eventNumbers(copied); !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("the copy of the view is %v, want [1 2 3]", got)
	}
	copied[0].Attributes["n"] = "changed"
	if got := eventNumbers(view)[0]; got != "1" {
		t.Fatalf("changing a copy changed the view: %q", got)
	}
	if got := copyEvents(nil); got != nil {
		t.Fatalf("a copy of no slice is %v, want nil", got)
	}
	if got := copyEvents([]types.Event{}); got == nil || len(got) != 0 {
		t.Fatalf("a copy of an empty slice is %v, want an empty slice", got)
	}
}

// fixtureWeightSnapshot builds a weight snapshot of the given number of entries:
// the entry at index i is for fixtureAddress(i+1) and has the weight entries-i.
func fixtureWeightSnapshot(epoch uint64, entries int) *potso.StoredWeightSnapshot {
	snapshot := &potso.StoredWeightSnapshot{
		Epoch:           epoch,
		TotalStake:      big.NewInt(int64(entries) * 1000),
		TotalEngagement: uint64(entries),
		Entries:         make([]potso.StoredWeightEntry, entries),
	}
	for i := range snapshot.Entries {
		snapshot.Entries[i] = potso.StoredWeightEntry{
			Address:            fixtureAddress(uint64(i + 1)),
			Stake:              big.NewInt(int64(1000 + i)),
			Engagement:         uint64(i),
			StakeShareBps:      uint64(i % 100),
			EngagementShareBps: uint64(i % 50),
			WeightBps:          uint64(entries - i),
		}
	}
	return snapshot
}

func fillPotsoLeaderboard(t *testing.T, node *Node, epoch uint64, entries int) {
	t.Helper()
	snapshot := fixtureWeightSnapshot(epoch, entries)
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.PotsoMetricsSetSnapshot(epoch, snapshot); err != nil {
		t.Fatalf("store leaderboard snapshot: %v", err)
	}
	if err := manager.PotsoRewardsSetLastProcessedEpoch(epoch); err != nil {
		t.Fatalf("store last processed epoch: %v", err)
	}
}

func fillPotsoDay(t *testing.T, node *Node, day string, participants int) {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	for i := 0; i < participants; i++ {
		meter := &potso.Meter{Day: day, UptimeSeconds: uint64(100 + i%977), TxCount: uint64(i % 31), EscrowEvents: uint64(i % 7)}
		if err := manager.PotsoPutMeter(fixtureAddress(uint64(i+1)), meter); err != nil {
			t.Fatalf("store meter %d: %v", i, err)
		}
	}
}

// The leaderboard reads a whole stored snapshot however small the page it
// returns; it may not do that under the state lock.
func TestPotsoLeaderboardDoesNotHoldTheStateLock(t *testing.T) {
	node := newTestNode(t)
	fillPotsoLeaderboard(t, node, 5, 40_000)
	commitFixture(t, node)

	requireStateLockMostlyFree(t, node, "PotsoLeaderboard", func() {
		if _, total, _, err := node.PotsoLeaderboard(0, 0, 10); err != nil || total != 40_000 {
			t.Errorf("PotsoLeaderboard: total %d, error %v", total, err)
		}
	})
}

// The daily top reads and ranks every participant of the day before it cuts the
// page; it may not do that under the state lock.
func TestPotsoTopDoesNotHoldTheStateLock(t *testing.T) {
	node := newTestNode(t)
	const day = "20260102"
	fillPotsoDay(t, node, day, 3_000)
	commitFixture(t, node)

	requireStateLockMostlyFree(t, node, "PotsoTop", func() {
		if top, err := node.PotsoTop(day, 10); err != nil || len(top) != 10 {
			t.Errorf("PotsoTop: %d entries, error %v", len(top), err)
		}
	})
}

// Reading from a copy of the state must give the pages and the ranking the
// stored data gives, including what has been applied since the last commit.
func TestPotsoReadsReturnWhatTheStateHolds(t *testing.T) {
	node := newTestNode(t)
	const day = "20260102"
	fillPotsoLeaderboard(t, node, 3, 25)
	commitFixture(t, node)
	fillPotsoLeaderboard(t, node, 4, 12) // pending: not committed yet
	fillPotsoDay(t, node, day, 6)

	epoch, total, page, err := node.PotsoLeaderboard(0, 2, 3)
	if err != nil {
		t.Fatalf("PotsoLeaderboard: %v", err)
	}
	if epoch != 4 || total != 12 || len(page) != 3 {
		t.Fatalf("the latest epoch page is epoch %d, total %d, %d rows; want epoch 4, total 12, 3 rows", epoch, total, len(page))
	}
	if page[0].Address != fixtureAddress(3) || page[2].Address != fixtureAddress(5) {
		t.Fatalf("the page starts at %x and ends at %x, want the third to fifth entries", page[0].Address, page[2].Address)
	}
	page[0].Stake.SetInt64(1)
	if _, _, again, err := node.PotsoLeaderboard(0, 2, 3); err != nil || again[0].Stake.Cmp(big.NewInt(1002)) != 0 {
		t.Fatalf("changing a returned row changed the stored one: %v, %v", again, err)
	}
	if epoch, total, rows, err := node.PotsoLeaderboard(3, 0, 0); err != nil || epoch != 3 || total != 25 || len(rows) != 25 {
		t.Fatalf("the committed epoch is epoch %d, total %d, %d rows, error %v; want epoch 3, total 25, 25 rows", epoch, total, len(rows), err)
	}
	if epoch, total, rows, err := node.PotsoLeaderboard(9, 0, 5); err != nil || epoch != 9 || total != 0 || len(rows) != 0 {
		t.Fatalf("an epoch with no snapshot is epoch %d, total %d, %d rows, error %v", epoch, total, len(rows), err)
	}

	top, err := node.PotsoTop(day, 4)
	if err != nil {
		t.Fatalf("PotsoTop: %v", err)
	}
	if len(top) != 4 {
		t.Fatalf("PotsoTop returned %d entries, want 4", len(top))
	}
	for i := 1; i < len(top); i++ {
		if top[i-1].Meter.Score < top[i].Meter.Score {
			t.Fatalf("PotsoTop is not in descending score order at %d: %d then %d", i, top[i-1].Meter.Score, top[i].Meter.Score)
		}
	}
	if all, err := node.PotsoTop(day, 100); err != nil || len(all) != 6 {
		t.Fatalf("PotsoTop with a large limit returned %d entries, error %v; want the 6 participants", len(all), err)
	}
}

// fillPotsoWeights stores the weight snapshot that governance and the weight
// query read for the epoch, and marks the epoch as the last one processed.
func fillPotsoWeights(t *testing.T, node *Node, epoch uint64, entries int) {
	t.Helper()
	snapshot := fixtureWeightSnapshot(epoch, entries)
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	if err := manager.SetSnapshotPotsoWeights(epoch, snapshot); err != nil {
		t.Fatalf("store weight snapshot: %v", err)
	}
	if err := manager.PotsoRewardsSetLastProcessedEpoch(epoch); err != nil {
		t.Fatalf("store last processed epoch: %v", err)
	}
}

// fillPotsoRewardEpoch stores the winners of a reward epoch the way the epoch
// close leaves them: the ordered list, a payout of 100+i for the winner at index
// i (the address fixtureAddress(i+1)), and a settled claim for every third one.
func fillPotsoRewardEpoch(t *testing.T, node *Node, epoch uint64, winners int) {
	t.Helper()
	list := make([][20]byte, winners)
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	for i := range list {
		list[i] = fixtureAddress(uint64(i + 1))
		amount := big.NewInt(int64(100 + i))
		if err := manager.PotsoRewardsSetPayout(epoch, list[i], amount); err != nil {
			t.Fatalf("store payout %d: %v", i, err)
		}
		if i%3 == 0 {
			claim := &potso.RewardClaim{Amount: amount, Claimed: true, ClaimedAt: uint64(50 + i), Mode: potso.RewardPayoutModeClaim}
			if err := manager.PotsoRewardsSetClaim(epoch, list[i], claim); err != nil {
				t.Fatalf("store claim %d: %v", i, err)
			}
		}
	}
	if err := manager.PotsoRewardsSetWinners(epoch, list); err != nil {
		t.Fatalf("store winners: %v", err)
	}
}

// appendPotsoRewardHistory adds count payouts to the history of addr, for the
// epochs from first on, the amount of the one for epoch e being 10+e.
func appendPotsoRewardHistory(t *testing.T, node *Node, addr [20]byte, first uint64, count int) {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	for i := 0; i < count; i++ {
		epoch := first + uint64(i)
		entry := potso.RewardHistoryEntry{Epoch: epoch, Amount: big.NewInt(int64(10 + epoch)), Mode: potso.RewardPayoutModeAuto}
		if err := manager.PotsoRewardsAppendHistory(addr, entry); err != nil {
			t.Fatalf("append history entry %d: %v", epoch, err)
		}
	}
}

// The weight query decodes a whole stored snapshot and searches it entry by
// entry however few addresses it is asked about; it may not do that under the
// state lock.
func TestPotsoWeightDoesNotHoldTheStateLock(t *testing.T) {
	node := newTestNode(t)
	const entries = 40_000
	fillPotsoWeights(t, node, 5, entries)
	commitFixture(t, node)

	// The last entry is the one that takes the longest to find.
	last := fixtureAddress(entries)
	requireStateLockMostlyFree(t, node, "PotsoWeight", func() {
		if epoch, weight, err := node.PotsoWeight(0, last); err != nil || epoch != 5 || weight != 1 {
			t.Errorf("PotsoWeight: epoch %d, weight %d, error %v; want epoch 5, weight 1", epoch, weight, err)
		}
	})
}

// A page of an epoch's payouts is cut from a winner list that is decoded whole;
// it may not be decoded under the state lock.
func TestPotsoEpochPayoutsDoNotHoldTheStateLock(t *testing.T) {
	node := newTestNode(t)
	const winners = 20_000
	fillPotsoRewardEpoch(t, node, 5, winners)
	commitFixture(t, node)

	// A cursor near the end of the list is the page that takes the longest to find.
	cursor := fixtureAddress(winners - 10)
	requireStateLockMostlyFree(t, node, "PotsoRewardEpochPayouts", func() {
		if page, err := node.PotsoRewardEpochPayouts(5, &cursor, 10); err != nil || len(page) != 10 {
			t.Errorf("PotsoRewardEpochPayouts: %d payouts, error %v; want 10", len(page), err)
		}
	})
}

// The export of an epoch reads the payout and the claim of every winner; it may
// not do that under the state lock.
func TestPotsoExportEpochDoesNotHoldTheStateLock(t *testing.T) {
	node := newTestNode(t)
	const winners = 3_000
	fillPotsoRewardEpoch(t, node, 5, winners)
	commitFixture(t, node)

	requireStateLockMostlyFree(t, node, "PotsoExportEpoch", func() {
		if _, _, count, err := node.PotsoExportEpoch(5); err != nil || count != winners {
			t.Errorf("PotsoExportEpoch: %d winners, error %v; want %d", count, err, winners)
		}
	})
}

// A page of an address's payout history is cut from a history that is decoded
// whole; it may not be decoded under the state lock.
func TestPotsoRewardsHistoryDoesNotHoldTheStateLock(t *testing.T) {
	node := newTestNode(t)
	addr := fixtureAddress(0x71)
	const entries = 3_000
	appendPotsoRewardHistory(t, node, addr, 1, entries)
	commitFixture(t, node)

	requireStateLockMostlyFree(t, node, "PotsoRewardsHistory", func() {
		if page, _, err := node.PotsoRewardsHistory(addr, "", 10); err != nil || len(page) != 10 {
			t.Errorf("PotsoRewardsHistory: %d entries, error %v; want 10", len(page), err)
		}
	})
}

// Reading the reward and weight records from a copy of the state must give the
// answers the stored records give, including what has been applied since the
// last commit, and what the caller gets back must be its own.
func TestPotsoRewardReadsReturnWhatTheStateHolds(t *testing.T) {
	node := newTestNode(t)
	if epoch, weight, err := node.PotsoWeight(0, fixtureAddress(1)); err != nil || epoch != 0 || weight != 0 {
		t.Fatalf("PotsoWeight before any epoch: epoch %d, weight %d, error %v; want zeros", epoch, weight, err)
	}

	fillPotsoWeights(t, node, 3, 25)
	fillPotsoRewardEpoch(t, node, 3, 25)
	appendPotsoRewardHistory(t, node, fixtureAddress(1), 1, 4)
	commitFixture(t, node)
	fillPotsoWeights(t, node, 4, 12) // pending: not committed yet
	fillPotsoRewardEpoch(t, node, 4, 8)
	appendPotsoRewardHistory(t, node, fixtureAddress(1), 5, 3)

	t.Run("weight", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			epoch     uint64
			addr      [20]byte
			wantEpoch uint64
			want      uint64
		}{
			{"latest epoch", 0, fixtureAddress(2), 4, 11},
			{"committed epoch", 3, fixtureAddress(2), 3, 24},
			{"address with no entry", 4, fixtureAddress(20), 4, 0},
			{"epoch with no snapshot", 9, fixtureAddress(2), 9, 0},
		} {
			epoch, weight, err := node.PotsoWeight(tc.epoch, tc.addr)
			if err != nil || epoch != tc.wantEpoch || weight != tc.want {
				t.Fatalf("%s: epoch %d, weight %d, error %v; want epoch %d, weight %d", tc.name, epoch, weight, err, tc.wantEpoch, tc.want)
			}
		}
	})

	t.Run("payouts", func(t *testing.T) {
		page, err := node.PotsoRewardEpochPayouts(3, nil, 2)
		if err != nil || len(page) != 2 || page[0].Address != fixtureAddress(1) || page[1].Address != fixtureAddress(2) {
			t.Fatalf("first page: %v, error %v; want the first two winners", page, err)
		}
		if page[0].Amount.Cmp(big.NewInt(100)) != 0 || page[1].Amount.Cmp(big.NewInt(101)) != 0 {
			t.Fatalf("first page amounts are %v and %v, want 100 and 101", page[0].Amount, page[1].Amount)
		}
		page[0].Amount.SetInt64(1)
		if again, err := node.PotsoRewardEpochPayouts(3, nil, 1); err != nil || again[0].Amount.Cmp(big.NewInt(100)) != 0 {
			t.Fatalf("changing a returned amount changed the stored one: %v, %v", again, err)
		}
		cursor := fixtureAddress(2)
		next, err := node.PotsoRewardEpochPayouts(3, &cursor, 3)
		if err != nil || len(next) != 3 || next[0].Address != fixtureAddress(3) || next[2].Address != fixtureAddress(5) {
			t.Fatalf("page after a cursor: %v, error %v; want the third to fifth winners", next, err)
		}
		if all, err := node.PotsoRewardEpochPayouts(3, nil, 0); err != nil || len(all) != 25 {
			t.Fatalf("default page: %d payouts, error %v; want the 25 winners", len(all), err)
		}
		last := fixtureAddress(25)
		if rest, err := node.PotsoRewardEpochPayouts(3, &last, 5); err != nil || rest == nil || len(rest) != 0 {
			t.Fatalf("page after the last winner: %v, error %v; want an empty list", rest, err)
		}
		if pending, err := node.PotsoRewardEpochPayouts(4, nil, 100); err != nil || len(pending) != 8 {
			t.Fatalf("pending epoch: %d payouts, error %v; want 8", len(pending), err)
		}
		if none, err := node.PotsoRewardEpochPayouts(9, nil, 10); err != nil || len(none) != 0 {
			t.Fatalf("epoch with no winners: %d payouts, error %v; want none", len(none), err)
		}
	})

	t.Run("export", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			epoch   uint64
			winners int
			total   int64
			claimed int
		}{
			{"committed epoch", 3, 25, 2800, 9},
			{"pending epoch", 4, 8, 828, 3},
			{"epoch with no winners", 9, 0, 0, 0},
		} {
			data, total, winners, err := node.PotsoExportEpoch(tc.epoch)
			if err != nil || winners != tc.winners || total.Cmp(big.NewInt(tc.total)) != 0 {
				t.Fatalf("%s: %d winners, total %v, error %v; want %d winners, total %d", tc.name, winners, total, err, tc.winners, tc.total)
			}
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			if len(lines) != tc.winners+1 || lines[0] != "address,amount,claimed,claimedAt,mode" {
				t.Fatalf("%s: %d lines starting %q; want a header and %d rows", tc.name, len(lines), lines[0], tc.winners)
			}
			claimed := 0
			for _, line := range lines[1:] {
				if strings.Contains(line, ",true,") {
					claimed++
				}
			}
			if claimed != tc.claimed {
				t.Fatalf("%s: %d claimed rows, want %d", tc.name, claimed, tc.claimed)
			}
		}
	})

	t.Run("history", func(t *testing.T) {
		addr := fixtureAddress(1)
		page, next, err := node.PotsoRewardsHistory(addr, "", 3)
		if err != nil || len(page) != 3 || next != "3" || page[0].Epoch != 7 || page[2].Epoch != 5 {
			t.Fatalf("first page: %v, next %q, error %v; want epochs 7 to 5 and next 3", page, next, err)
		}
		if page[0].Amount.Cmp(big.NewInt(17)) != 0 {
			t.Fatalf("the newest amount is %v, want 17", page[0].Amount)
		}
		page[0].Amount.SetInt64(1)
		if again, _, err := node.PotsoRewardsHistory(addr, "", 1); err != nil || again[0].Amount.Cmp(big.NewInt(17)) != 0 {
			t.Fatalf("changing a returned entry changed the stored one: %v, %v", again, err)
		}
		page, next, err = node.PotsoRewardsHistory(addr, "3", 3)
		if err != nil || len(page) != 3 || next != "6" || page[0].Epoch != 4 || page[2].Epoch != 2 {
			t.Fatalf("second page: %v, next %q, error %v; want epochs 4 to 2 and next 6", page, next, err)
		}
		page, next, err = node.PotsoRewardsHistory(addr, "6", 3)
		if err != nil || len(page) != 1 || next != "" || page[0].Epoch != 1 {
			t.Fatalf("last page: %v, next %q, error %v; want epoch 1 and no next", page, next, err)
		}
		if page, next, err := node.PotsoRewardsHistory(addr, "7", 3); err != nil || page == nil || len(page) != 0 || next != "" {
			t.Fatalf("page past the end: %v, next %q, error %v; want an empty list", page, next, err)
		}
		if page, _, err := node.PotsoRewardsHistory(fixtureAddress(2), "", 3); err != nil || len(page) != 0 {
			t.Fatalf("an address with no history has %v, error %v", page, err)
		}
		for _, cursor := range []string{"x", "-1"} {
			if _, _, err := node.PotsoRewardsHistory(addr, cursor, 3); err == nil {
				t.Fatalf("cursor %q was accepted", cursor)
			}
		}
	})

	t.Run("a node with no state", func(t *testing.T) {
		for name, node := range map[string]*Node{"empty node": {}, "missing node": nil} {
			if _, _, err := node.PotsoWeight(0, fixtureAddress(1)); err == nil {
				t.Errorf("PotsoWeight on a %s reported no error", name)
			}
			if _, err := node.PotsoRewardEpochPayouts(1, nil, 10); err == nil {
				t.Errorf("PotsoRewardEpochPayouts on a %s reported no error", name)
			}
			if _, _, _, err := node.PotsoExportEpoch(1); err == nil {
				t.Errorf("PotsoExportEpoch on a %s reported no error", name)
			}
			if _, _, err := node.PotsoRewardsHistory(fixtureAddress(1), "", 10); err == nil {
				t.Errorf("PotsoRewardsHistory on a %s reported no error", name)
			}
		}
	})
}

// The list reads of the subscriptions module decode a stored index or walk a
// charge history; they must not do that under the state lock either.
func TestSubscriptionListReadsDoNotHoldTheStateLock(t *testing.T) {
	node := newTestNode(t)
	merchant := fixtureAddress(0x51)
	payer := fixtureAddress(0x52)
	const entries = 150_000
	ids := make([]uint64, entries)
	for i := range ids {
		ids[i] = uint64(i + 1)
	}
	// The indexes are stored under the registry's own keys; writing them whole
	// is how a test gets a merchant with a very large list without creating each
	// subscription.
	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	for _, key := range []string{"subscriptions/merchantsubs/" + string(merchant[:]), "subscriptions/payersubs/" + string(payer[:]), "subscriptions/merchantplans/" + string(merchant[:])} {
		if err := manager.KVPut([]byte(key), ids); err != nil {
			node.stateMu.Unlock()
			t.Fatalf("store index: %v", err)
		}
	}
	node.stateMu.Unlock()
	commitFixture(t, node)

	if got, err := node.SubscriptionsByMerchant(merchant); err != nil || len(got) != entries || got[0] != 1 || got[entries-1] != entries {
		t.Fatalf("SubscriptionsByMerchant returned %d ids, error %v; want the %d stored", len(got), err, entries)
	}
	if got, err := node.SubscriptionsByPayer(payer); err != nil || len(got) != entries {
		t.Fatalf("SubscriptionsByPayer returned %d ids, error %v; want the %d stored", len(got), err, entries)
	}
	if got, err := node.SubscriptionPlansByMerchant(merchant); err != nil || len(got) != entries {
		t.Fatalf("SubscriptionPlansByMerchant returned %d ids, error %v; want the %d stored", len(got), err, entries)
	}
	t.Run("by merchant", func(t *testing.T) {
		requireStateLockMostlyFree(t, node, "SubscriptionsByMerchant", func() {
			if _, err := node.SubscriptionsByMerchant(merchant); err != nil {
				t.Errorf("SubscriptionsByMerchant: %v", err)
			}
		})
	})
	t.Run("by payer", func(t *testing.T) {
		requireStateLockMostlyFree(t, node, "SubscriptionsByPayer", func() {
			if _, err := node.SubscriptionsByPayer(payer); err != nil {
				t.Errorf("SubscriptionsByPayer: %v", err)
			}
		})
	})
	t.Run("plans by merchant", func(t *testing.T) {
		requireStateLockMostlyFree(t, node, "SubscriptionPlansByMerchant", func() {
			if _, err := node.SubscriptionPlansByMerchant(merchant); err != nil {
				t.Errorf("SubscriptionPlansByMerchant: %v", err)
			}
		})
	})
}

// The charge history of a subscription is read the same way, and an unknown
// subscription has none.
func TestSubscriptionChargesAreReadFromACopyOfTheState(t *testing.T) {
	node := newTestNode(t)
	node.stateMu.Lock()
	registry := subscriptions.NewRegistry(nhbstate.NewManager(node.state.Trie))
	for i := 1; i <= 3; i++ {
		charge := subscriptions.Charge{
			SubscriptionID: 7,
			Asset:          subscriptions.AssetNHB,
			AmountWei:      big.NewInt(int64(i)),
			FeeWei:         big.NewInt(0),
			Status:         subscriptions.ChargeStatusPaid,
			AttemptNumber:  uint32(i),
		}
		if err := registry.AppendCharge(7, charge); err != nil {
			node.stateMu.Unlock()
			t.Fatalf("append charge %d: %v", i, err)
		}
	}
	node.stateMu.Unlock()
	if got, err := node.SubscriptionCharges(7); err != nil || len(got) != 3 {
		t.Fatalf("SubscriptionCharges returned %d charges, error %v; want 3", len(got), err)
	}
	if got, err := node.SubscriptionCharges(8); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("an unknown subscription has charges %v, error %v; want an empty list", got, err)
	}
}

// A state view is a copy: the callback runs without the lock, sees the state as
// of the copy, and what it writes is thrown away.
func TestStateViewCallbackRunsWithoutTheStateLock(t *testing.T) {
	node := newTestNode(t)
	setEpoch := func(epoch uint64) {
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		if err := nhbstate.NewManager(node.state.Trie).PotsoRewardsSetLastProcessedEpoch(epoch); err != nil {
			t.Fatalf("set epoch: %v", err)
		}
	}
	liveEpoch := func() uint64 {
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		epoch, _, err := nhbstate.NewManager(node.state.Trie).PotsoRewardsLastProcessedEpoch()
		if err != nil {
			t.Fatalf("read epoch: %v", err)
		}
		return epoch
	}
	setEpoch(7)

	ran := false
	err := node.WithStateView(func(view *nhbstate.Manager) error {
		ran = true
		if !node.stateMu.TryLock() {
			t.Fatalf("the callback ran while the state lock was held")
		}
		node.stateMu.Unlock()

		if epoch, _, err := view.PotsoRewardsLastProcessedEpoch(); err != nil || epoch != 7 {
			t.Fatalf("the view shows epoch %d, error %v; want 7", epoch, err)
		}
		setEpoch(8) // the live state moves on after the copy
		if epoch, _, _ := view.PotsoRewardsLastProcessedEpoch(); epoch != 7 {
			t.Fatalf("the view shows epoch %d after the live state moved on, want 7", epoch)
		}
		return view.PotsoRewardsSetLastProcessedEpoch(9)
	})
	if err != nil || !ran {
		t.Fatalf("WithStateView: ran %v, error %v", ran, err)
	}
	if got := liveEpoch(); got != 8 {
		t.Fatalf("the live state shows epoch %d, want 8: the view's write must be discarded and the live write kept", got)
	}

	if err := node.WithStateView(nil); err == nil {
		t.Fatalf("WithStateView accepted a nil callback")
	}
	if err := (&Node{}).WithStateView(func(*nhbstate.Manager) error { return nil }); err == nil {
		t.Fatalf("WithStateView on a node with no state reported no error")
	}
	var missing *Node
	if err := missing.WithStateView(func(*nhbstate.Manager) error { return nil }); err == nil {
		t.Fatalf("WithStateView on a missing node reported no error")
	}
}

// Every read above runs while blocks are being committed, and none of them may
// change a block: the blocks a node commits are the same with the readers as
// without them, and the readers never see an error.
func TestPublicReadsCanRunWhileBlocksCommit(t *testing.T) {
	const blocks = 400
	var (
		mu       sync.Mutex
		failures []string
		reads    int
	)
	record := func(name string, err error) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		if err != nil && len(failures) < 5 {
			failures = append(failures, name+": "+err.Error())
		}
	}
	observe := func(node *Node) func() {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			var addr [20]byte
			addr[19] = 1
			for {
				select {
				case <-stop:
					return
				default:
				}
				node.Events()
				_, _, _, err := node.PotsoLeaderboard(0, 0, 10)
				record("PotsoLeaderboard", err)
				_, err = node.PotsoTop("", 10)
				record("PotsoTop", err)
				err = node.WithStateView(func(m *nhbstate.Manager) error {
					_, err := m.TokenSupply("NHB")
					return err
				})
				record("WithStateView", err)
				_, err = node.SubscriptionsByMerchant(addr)
				record("SubscriptionsByMerchant", err)
				_, err = node.SubscriptionCharges(1)
				record("SubscriptionCharges", err)
				_, _, err = node.PotsoWeight(0, addr)
				record("PotsoWeight", err)
				_, err = node.PotsoRewardEpochPayouts(1, nil, 10)
				record("PotsoRewardEpochPayouts", err)
				_, _, _, err = node.PotsoExportEpoch(1)
				record("PotsoExportEpoch", err)
				_, _, err = node.PotsoRewardsHistory(addr, "", 10)
				record("PotsoRewardsHistory", err)
				time.Sleep(200 * time.Microsecond)
			}
		}()
		return func() {
			close(stop)
			wg.Wait()
		}
	}
	plain := runDevnet(t, blocks)
	observed := runDevnetObserved(t, blocks, observe)

	mu.Lock()
	defer mu.Unlock()
	if len(failures) > 0 {
		t.Fatalf("reads failed while blocks were committed: %v", failures)
	}
	if reads == 0 {
		t.Fatalf("no read completed while the blocks were committed")
	}
	if !reflect.DeepEqual(observed.blockLines, plain.blockLines) {
		t.Fatalf("reading while blocks were committed changed a block")
	}
	if got, want := devnetDigest(devnetDescribeEvents(observed.node.Events())), devnetDigest(devnetDescribeEvents(plain.node.Events())); got != want {
		t.Fatalf("reading while blocks were committed changed the events the node reports")
	}
}
