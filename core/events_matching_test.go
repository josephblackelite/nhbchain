package core

import (
	"runtime"
	"strings"
	"testing"
)

// Ledger PL-EXT-78: escrow_listEvents built a deep copy of every retained event for each
// call. EventsMatching filters first and copies only what it returns.
func TestEventsMatchingReturnsTheFirstMatchesOldestFirstAsCopies(t *testing.T) {
	node := newTestNode(t)
	fillEventLog(node, 50, 40) // 50 "test.event", then 40 "escrow.created"

	got := node.EventsMatching("ESCROW.", 5)
	if len(got) != 5 {
		t.Fatalf("got %d events, want 5", len(got))
	}
	for i, evt := range got {
		if !strings.HasPrefix(evt.Type, "escrow.") {
			t.Fatalf("event %d has type %q", i, evt.Type)
		}
	}
	all := node.Events()
	var firstEscrow int
	for i, evt := range all {
		if strings.HasPrefix(evt.Type, "escrow.") {
			firstEscrow = i
			break
		}
	}
	for i := range got {
		if got[i].Attributes["n"] != all[firstEscrow+i].Attributes["n"] {
			t.Fatalf("event %d is not the %d-th escrow event in order", i, i)
		}
	}
	// The events are copies: changing one changes nothing in the log.
	for k := range got[0].Attributes {
		got[0].Attributes[k] = "changed"
	}
	if again := node.EventsMatching("escrow.", 1); len(again) != 1 || again[0].Attributes["n"] == "changed" {
		t.Fatalf("a returned event shares its attributes with the log")
	}
	if n := len(node.EventsMatching("escrow.", 1000)); n != 40 {
		t.Fatalf("with a limit above the matches: %d events, want all 40", n)
	}
	if node.EventsMatching("escrow.", 0) != nil || node.EventsMatching("escrow.", -3) != nil {
		t.Fatalf("a limit below one must return nothing")
	}
	if len(node.EventsMatching("nothing.", 10)) != 0 {
		t.Fatalf("a prefix nothing has must return nothing")
	}
	var nilNode *Node
	if nilNode.EventsMatching("escrow.", 5) != nil {
		t.Fatalf("a nil node must return nothing")
	}
}

func allocatedBy(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}

// A call with a small limit must allocate a small fraction of what copying the whole log costs.
func TestEventsMatchingAllocatesInProportionToWhatItReturns(t *testing.T) {
	node := newTestNode(t)
	fillEventLog(node, maxRetainedEvents, maxPinnedEvents)

	whole := allocatedBy(func() { _ = node.Events() })
	small := allocatedBy(func() { _ = node.EventsMatching("escrow.", 100) })
	t.Logf("Events: %d MB, EventsMatching(100): %d MB", whole>>20, small>>20)
	if small*5 > whole {
		t.Fatalf("EventsMatching(…, 100) allocated %d bytes against %d for the whole log: it must be far smaller", small, whole)
	}
}
