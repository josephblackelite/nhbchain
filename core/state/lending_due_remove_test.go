package state

import "testing"

// Ledger PL-EXT-42: one settlement pass reads a day's due list, handles some of its entries and may schedule entries of its own onto the
// same day. Only the handled entries leave the list; the scheduled ones stay, in order; an emptied list is deleted (the same shape as
// SubscriptionsRemoveDue, tested next to it).

func lendingID(b byte) [32]byte {
	var id [32]byte
	id[0] = b
	return id
}

func TestLendingAutoDebitRemoveDueKeepsEntriesScheduledDuringThePass(t *testing.T) {
	m := newTestManagerForLendingAutoDebit(t)
	const day = 20_000
	for _, id := range [][32]byte{lendingID(1), lendingID(2), lendingID(3), lendingID(2)} {
		if err := m.LendingAutoDebitAppendDue(day, id); err != nil {
			t.Fatalf("append due: %v", err)
		}
	}
	// A different day must never be touched.
	if err := m.LendingAutoDebitAppendDue(day+1, lendingID(1)); err != nil {
		t.Fatalf("append due: %v", err)
	}
	// The pass read [1 2 3 2], handled 1, 2 and 3, and scheduled 9 meanwhile.
	if err := m.LendingAutoDebitAppendDue(day, lendingID(9)); err != nil {
		t.Fatalf("append due: %v", err)
	}
	if err := m.LendingAutoDebitRemoveDue(day, [][32]byte{lendingID(1), lendingID(2), lendingID(3)}); err != nil {
		t.Fatalf("remove due: %v", err)
	}
	due, err := m.LendingAutoDebitDueOnDay(day)
	if err != nil {
		t.Fatalf("load due: %v", err)
	}
	if len(due) != 2 || due[0] != lendingID(2) || due[1] != lendingID(9) {
		t.Fatalf("expected [2 9] to remain (one occurrence removed per handled id), got %v", due)
	}
	if other, _ := m.LendingAutoDebitDueOnDay(day + 1); len(other) != 1 {
		t.Fatalf("another day's bucket changed: %v", other)
	}

	// Nothing handled: nothing is written or removed.
	if err := m.LendingAutoDebitRemoveDue(day, nil); err != nil {
		t.Fatalf("remove nothing: %v", err)
	}
	if due, _ := m.LendingAutoDebitDueOnDay(day); len(due) != 2 {
		t.Fatalf("removing nothing changed the bucket: %v", due)
	}

	if err := m.LendingAutoDebitRemoveDue(day, [][32]byte{lendingID(2), lendingID(9)}); err != nil {
		t.Fatalf("remove due: %v", err)
	}
	if due, _ := m.LendingAutoDebitDueOnDay(day); len(due) != 0 {
		t.Fatalf("expected an empty bucket, got %v", due)
	}
	var raw [][32]byte
	if err := m.KVGetList(lendingAutoDebitDueListKey(day), &raw); err != nil {
		t.Fatalf("read bucket key: %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("expected the emptied bucket to be gone, key still holds %v", raw)
	}
}

func TestEmptyingABucketWithRemoveDueLeavesTheStateClearDueLeaves(t *testing.T) {
	// A bucket whose every entry was handled is deleted, not rewritten as an empty list: the state root is the one ClearDue gives.
	const day = 20_000
	build := func() *Manager {
		m := newTestManagerForLendingAutoDebit(t)
		for _, id := range [][32]byte{lendingID(1), lendingID(2)} {
			if err := m.LendingAutoDebitAppendDue(day, id); err != nil {
				t.Fatalf("append due: %v", err)
			}
			if err := m.LendingDepositPayoutAppendDue(day, id); err != nil {
				t.Fatalf("append due: %v", err)
			}
		}
		if err := m.LendingAutoDebitAppendDue(day+1, lendingID(7)); err != nil {
			t.Fatalf("append due: %v", err)
		}
		return m
	}
	removed, cleared := build(), build()
	if err := removed.LendingAutoDebitRemoveDue(day, [][32]byte{lendingID(1), lendingID(2)}); err != nil {
		t.Fatalf("remove due: %v", err)
	}
	if err := removed.LendingDepositPayoutRemoveDue(day, [][32]byte{lendingID(1), lendingID(2)}); err != nil {
		t.Fatalf("remove due: %v", err)
	}
	if err := cleared.LendingAutoDebitClearDue(day); err != nil {
		t.Fatalf("clear due: %v", err)
	}
	if err := cleared.LendingDepositPayoutClearDue(day); err != nil {
		t.Fatalf("clear due: %v", err)
	}
	if removed.trie.Hash() != cleared.trie.Hash() {
		t.Fatalf("emptying a bucket with RemoveDue leaves a different state than ClearDue: %x vs %x", removed.trie.Hash(), cleared.trie.Hash())
	}
}

func TestLendingDepositPayoutRemoveDueKeepsEntriesScheduledDuringThePass(t *testing.T) {
	m := newTestManagerForLendingAutoDebit(t)
	const day = 20_000
	for _, id := range [][32]byte{lendingID(1), lendingID(2), lendingID(3), lendingID(2)} {
		if err := m.LendingDepositPayoutAppendDue(day, id); err != nil {
			t.Fatalf("append due: %v", err)
		}
	}
	// The same ids on the loan side must be unaffected: the two lists are separate.
	if err := m.LendingAutoDebitAppendDue(day, lendingID(1)); err != nil {
		t.Fatalf("append due: %v", err)
	}
	if err := m.LendingDepositPayoutAppendDue(day, lendingID(9)); err != nil {
		t.Fatalf("append due: %v", err)
	}
	if err := m.LendingDepositPayoutRemoveDue(day, [][32]byte{lendingID(1), lendingID(2), lendingID(3)}); err != nil {
		t.Fatalf("remove due: %v", err)
	}
	due, err := m.LendingDepositPayoutDueOnDay(day)
	if err != nil {
		t.Fatalf("load due: %v", err)
	}
	if len(due) != 2 || due[0] != lendingID(2) || due[1] != lendingID(9) {
		t.Fatalf("expected [2 9] to remain, got %v", due)
	}
	if loans, _ := m.LendingAutoDebitDueOnDay(day); len(loans) != 1 {
		t.Fatalf("the loan list changed: %v", loans)
	}
	if err := m.LendingDepositPayoutRemoveDue(day, [][32]byte{lendingID(2), lendingID(9)}); err != nil {
		t.Fatalf("remove due: %v", err)
	}
	var raw [][32]byte
	if err := m.KVGetList(lendingDepositPayoutDueListKey(day), &raw); err != nil {
		t.Fatalf("read bucket key: %v", err)
	}
	if len(raw) != 0 {
		t.Fatalf("expected the emptied bucket to be gone, key still holds %v", raw)
	}
}
