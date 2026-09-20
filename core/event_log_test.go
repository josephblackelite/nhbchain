package core

import (
	"reflect"
	"strconv"
	"sync"
	"testing"

	"nhbchain/core/events"
	"nhbchain/core/types"
)

func numberedEvent(n int) types.Event {
	return types.Event{Type: "test.event", Attributes: map[string]string{"n": strconv.Itoa(n)}}
}

func typedEvent(eventType string, n int) types.Event {
	return types.Event{Type: eventType, Attributes: map[string]string{"n": strconv.Itoa(n)}}
}

func eventNumbers(log []types.Event) []string {
	out := make([]string, 0, len(log))
	for _, evt := range log {
		out = append(out, evt.Attributes["n"])
	}
	return out
}

func numberedEvents(from, to int) []types.Event {
	var out []types.Event
	for n := from; n <= to; n++ {
		out = append(out, numberedEvent(n))
	}
	return out
}

// setMaxRetainedEvents shrinks the number of events a node keeps until the test
// ends. It must be called before the node is created.
func setMaxRetainedEvents(t *testing.T, limit int) {
	t.Helper()
	previous := maxRetainedEvents
	maxRetainedEvents = limit
	t.Cleanup(func() { maxRetainedEvents = previous })
}

func TestEventLogKeepsTheMostRecentEvents(t *testing.T) {
	log := newEventLog(3, 2)
	if got := log.snapshot(); len(got) != 0 {
		t.Fatalf("a new log holds %v", eventNumbers(got))
	}
	log.append(numberedEvents(1, 2))
	if got := eventNumbers(log.snapshot()); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("below the limit the log holds %v, want [1 2]", got)
	}
	log.append(numberedEvents(3, 5))
	if got := eventNumbers(log.snapshot()); !reflect.DeepEqual(got, []string{"3", "4", "5"}) {
		t.Fatalf("at the limit the log holds %v, want [3 4 5]", got)
	}
	log.append(nil)
	log.append(numberedEvents(6, 6))
	if got := eventNumbers(log.snapshot()); !reflect.DeepEqual(got, []string{"4", "5", "6"}) {
		t.Fatalf("after one more event the log holds %v, want [4 5 6]", got)
	}
	log.append(numberedEvents(7, 12))
	if got := eventNumbers(log.snapshot()); !reflect.DeepEqual(got, []string{"10", "11", "12"}) {
		t.Fatalf("after more events than the limit in one call the log holds %v, want [10 11 12]", got)
	}
	log.append(numberedEvents(13, 14))
	if got := eventNumbers(log.snapshot()); !reflect.DeepEqual(got, []string{"12", "13", "14"}) {
		t.Fatalf("the oldest event was not the one dropped: %v, want [12 13 14]", got)
	}
}

func TestEventLogSnapshotsAreCopies(t *testing.T) {
	log := newEventLog(4, 2)
	log.append(numberedEvents(1, 4))
	first := log.snapshot()
	first[0].Attributes["n"] = "changed"
	first[1] = types.Event{Type: "other"}
	if got := eventNumbers(log.snapshot()); !reflect.DeepEqual(got, []string{"1", "2", "3", "4"}) {
		t.Fatalf("changing a snapshot changed the log: %v", got)
	}
}

func TestEventLogLimitHasAFloorAndANilLogIsEmpty(t *testing.T) {
	log := newEventLog(0, 0)
	log.append(numberedEvents(1, 3))
	if got := eventNumbers(log.snapshot()); !reflect.DeepEqual(got, []string{"3"}) {
		t.Fatalf("a log with no limit holds %v, want the last event only", got)
	}
	var missing *eventLog
	missing.append(numberedEvents(1, 2))
	if got := missing.snapshot(); got != nil {
		t.Fatalf("a nil log holds %v", got)
	}
	var sp *StateProcessor
	if got := sp.takeEvents(); got != nil {
		t.Fatalf("a nil state processor handed over %v", got)
	}
}

// Events some readers add up or count over the whole run (fees applied, POTSO
// penalties, escrow events) are not pushed out by a flood of other events; they
// have a log of their own, and the two are read back in the order the events
// happened.
func TestEventLogKeepsPinnedEventsApartFromTheRest(t *testing.T) {
	for _, pinned := range []string{events.TypeFeeApplied, events.TypePotsoPenaltyApplied, "escrow.created", "ESCROW.Released"} {
		if !isPinnedEvent(pinned) {
			t.Fatalf("%q is not pinned", pinned)
		}
	}
	for _, plain := range []string{"test.event", "transfer.native", "escrowed", "loyalty.program.skipped", ""} {
		if isPinnedEvent(plain) {
			t.Fatalf("%q is pinned", plain)
		}
	}

	log := newEventLog(2, 3)
	log.append([]types.Event{
		typedEvent(events.TypeFeeApplied, 1),
		typedEvent("test.event", 2),
		typedEvent(events.TypePotsoPenaltyApplied, 3),
		typedEvent("test.event", 4),
		typedEvent("test.event", 5),
		typedEvent("escrow.created", 6),
		typedEvent("test.event", 7),
	})
	if got := eventNumbers(log.snapshot()); !reflect.DeepEqual(got, []string{"1", "3", "5", "6", "7"}) {
		t.Fatalf("the log holds %v, want the three pinned events and the last two others in order [1 3 5 6 7]", got)
	}
	log.append([]types.Event{typedEvent("test.event", 8), typedEvent(events.TypeFeeApplied, 9), typedEvent("test.event", 10)})
	if got := eventNumbers(log.snapshot()); !reflect.DeepEqual(got, []string{"3", "6", "8", "9", "10"}) {
		t.Fatalf("the log holds %v, want [3 6 8 9 10]: the oldest pinned event goes first once its log is full", got)
	}
}

// A node keeps a pinned event while enough other events scroll past to push out
// everything else it had.
func TestNodeKeepsPinnedEventsWhileOtherEventsScrollPast(t *testing.T) {
	setMaxRetainedEvents(t, 5)
	node := newTestNode(t)
	push := func(evt types.Event) {
		node.stateMu.Lock()
		defer node.stateMu.Unlock()
		node.state.AppendEvent(&evt)
		node.retainCommittedEventsLocked()
	}
	push(typedEvent(events.TypeFeeApplied, 0))
	for n := 1; n <= 50; n++ {
		push(numberedEvent(n))
	}
	push(typedEvent(events.TypePotsoPenaltyApplied, 51))

	var got []string
	for _, evt := range node.Events() {
		if evt.Type == "test.event" || isPinnedEvent(evt.Type) {
			got = append(got, evt.Attributes["n"])
		}
	}
	want := []string{"0", "46", "47", "48", "49", "50", "51"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("the node reports %v, want the fee event, the last five others and the penalty in order %v", got, want)
	}
}

// The state a node copies for every block it builds, validates or commits must
// not carry the events of the blocks before it: they belong to the node's
// bounded log. Before, the state held every event since the node started and
// copied all of them each time, so a block cost more the longer the node ran.
func TestCommittedBlocksLeaveNoEventsOnTheState(t *testing.T) {
	run := runDevnet(t, 120)
	node := run.node

	node.stateMu.RLock()
	held := len(node.state.events)
	stateCopy, err := node.state.Copy()
	node.stateMu.RUnlock()
	if err != nil {
		t.Fatalf("copy state: %v", err)
	}
	if held != 0 {
		t.Fatalf("the state holds %d events after 120 committed blocks, want none", held)
	}
	if n := len(stateCopy.events); n != 0 {
		t.Fatalf("a copy of the state carries %d events, want none", n)
	}
	if logged := node.Events(); len(logged) == 0 {
		t.Fatalf("no event is readable after 120 committed blocks")
	}
}

// The events a node reports are the most recent ones its committed blocks
// produced, in order, and the bound changes nothing else about the chain.
func TestNodeEventsKeepOnlyTheMostRecentEvents(t *testing.T) {
	full := runDevnet(t, 200)
	fullEvents := devnetDescribeEvents(full.node.Events())

	const keep = 150
	if len(fullEvents) <= keep {
		t.Fatalf("the devnet produced %d events, too few to reach a bound of %d", len(fullEvents), keep)
	}
	setMaxRetainedEvents(t, keep)
	bounded := runDevnet(t, 200)
	got := devnetDescribeEvents(bounded.node.Events())
	if len(got) != keep {
		t.Fatalf("a node with a bound of %d reports %d events", keep, len(got))
	}
	if !reflect.DeepEqual(got, fullEvents[len(fullEvents)-keep:]) {
		t.Fatalf("the bounded log is not the most recent %d events of the full one", keep)
	}
	if !reflect.DeepEqual(bounded.blockLines, full.blockLines) {
		t.Fatalf("the bound on the event log changed a block")
	}
}

// Replays the whole devnet with a small bound: every block hash and state root
// is the recorded one, and at every checkpoint the node reports exactly the most
// recent events of the log a node without a bound reports.
func TestDevnetReplayWithABoundedLog(t *testing.T) {
	unbounded := runDevnet(t, devnetBlocks)
	if devnetDigest(unbounded.blockLines) != devnetBlocksDigest {
		t.Fatalf("the devnet without a bound no longer matches the recorded blocks")
	}
	const keep = 1000
	setMaxRetainedEvents(t, keep)
	bounded := runDevnet(t, devnetBlocks)
	if got := devnetDigest(bounded.blockLines); got != devnetBlocksDigest {
		t.Fatalf("with a bound of %d events the block hashes and state roots changed: digest %s, recorded %s", keep, got, devnetBlocksDigest)
	}
	windowed := 0
	for i, height := range bounded.checkpointHeight {
		want := unbounded.checkpointEvents[i]
		if len(want) > keep {
			want = want[len(want)-keep:]
			windowed++
		}
		if !reflect.DeepEqual(bounded.checkpointEvents[i], want) {
			t.Fatalf("at height %d the bounded node reports %d events that are not the most recent %d of the full log", height, len(bounded.checkpointEvents[i]), len(want))
		}
	}
	if windowed == 0 {
		t.Fatalf("the bound of %d was never reached, so the test proved nothing", keep)
	}
}

// Readers may call Events while blocks are committed. Whatever they see must be
// a prefix of the final log: no event missing, repeated or out of order, even
// while a commit hands its events to the log.
func TestEventsCanBeReadWhileBlocksCommit(t *testing.T) {
	var (
		mu     sync.Mutex
		digest = map[int]string{}
		clash  string
		reads  int
	)
	observe := func(node *Node) func() {
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				lines := devnetDescribeEvents(node.Events())
				sum := devnetDigest(lines)
				mu.Lock()
				reads++
				if previous, ok := digest[len(lines)]; ok && previous != sum {
					clash = "two reads with " + strconv.Itoa(len(lines)) + " events saw different events"
				}
				digest[len(lines)] = sum
				mu.Unlock()
			}
		}()
		return func() {
			close(stop)
			wg.Wait()
		}
	}
	run := runDevnetObserved(t, 150, observe)
	final := run.checkpointEvents[len(run.checkpointEvents)-1]

	mu.Lock()
	defer mu.Unlock()
	if clash != "" {
		t.Fatal(clash)
	}
	if reads == 0 {
		t.Fatalf("no read completed while the blocks were committed")
	}
	for n, sum := range digest {
		if n > len(final) {
			t.Fatalf("a read saw %d events, more than the %d in the final log", n, len(final))
		}
		if want := devnetDigest(final[:n]); sum != want {
			t.Fatalf("a read that saw %d events did not see the first %d events of the final log", n, n)
		}
	}
}
