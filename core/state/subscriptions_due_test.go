package state

import (
	"testing"

	"nhbchain/native/subscriptions"
)

// One settlement pass reads a day's due list, handles some of its entries and
// may append entries of its own to the same day (a retry). Only the handled
// entries leave the list; the appended ones stay, in order, and an emptied list
// is deleted.
func TestSubscriptionsRemoveDueKeepsEntriesAddedDuringThePass(t *testing.T) {
	m := newTestManagerForLendingAutoDebit(t)
	const day = 20_000
	for _, id := range []subscriptions.SubscriptionID{1, 2, 3, 2} {
		if err := m.SubscriptionsAppendDue(day, id); err != nil {
			t.Fatalf("append due: %v", err)
		}
	}
	// The pass read [1 2 3 2], handled 1, 2 and 3, and appended 9 meanwhile.
	if err := m.SubscriptionsAppendDue(day, 9); err != nil {
		t.Fatalf("append due: %v", err)
	}
	if err := m.SubscriptionsRemoveDue(day, []subscriptions.SubscriptionID{1, 2, 3}); err != nil {
		t.Fatalf("remove due: %v", err)
	}
	due, err := m.SubscriptionsDueOnDay(day)
	if err != nil {
		t.Fatalf("load due: %v", err)
	}
	if len(due) != 2 || due[0] != 2 || due[1] != 9 {
		t.Fatalf("expected [2 9] to remain, got %v", due)
	}

	// Nothing handled: nothing is written or removed.
	if err := m.SubscriptionsRemoveDue(day, nil); err != nil {
		t.Fatalf("remove nothing: %v", err)
	}
	if due, _ := m.SubscriptionsDueOnDay(day); len(due) != 2 {
		t.Fatalf("removing nothing changed the bucket: %v", due)
	}

	if err := m.SubscriptionsRemoveDue(day, []subscriptions.SubscriptionID{2, 9}); err != nil {
		t.Fatalf("remove due: %v", err)
	}
	if due, _ := m.SubscriptionsDueOnDay(day); len(due) != 0 {
		t.Fatalf("expected an empty bucket, got %v", due)
	}
	var raw []uint64
	if err := m.KVGetList(subscriptionsDueListKey(day), &raw); err != nil {
		t.Fatalf("read bucket key: %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("expected the emptied bucket to be gone, key still holds %v", raw)
	}
}
