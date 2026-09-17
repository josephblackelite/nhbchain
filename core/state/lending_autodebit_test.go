package state

import (
	"testing"

	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func newTestManagerForLendingAutoDebit(t *testing.T) *Manager {
	t.Helper()
	db := storage.NewMemDB()
	tr, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	return NewManager(tr)
}

func loanIDFromSeed(seed byte) [32]byte {
	var id [32]byte
	id[31] = seed
	return id
}

// TestLendingAutoDebitDueIndexRoundTrips proves the due-index round-trips
// [32]byte loan IDs through RLP correctly (append/read/clear), and that
// distinct days stay isolated from each other -- the exact foundation
// core/lending_autodebit_settlement.go's day-loop depends on.
func TestLendingAutoDebitDueIndexRoundTrips(t *testing.T) {
	m := newTestManagerForLendingAutoDebit(t)

	empty, err := m.LendingAutoDebitDueOnDay(100)
	if err != nil {
		t.Fatalf("read empty day: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("expected no entries for an untouched day, got %d", len(empty))
	}

	loanA := loanIDFromSeed(1)
	loanB := loanIDFromSeed(2)
	if err := m.LendingAutoDebitAppendDue(100, loanA); err != nil {
		t.Fatalf("append loanA: %v", err)
	}
	if err := m.LendingAutoDebitAppendDue(100, loanB); err != nil {
		t.Fatalf("append loanB: %v", err)
	}
	// A distinct day's own bucket must not see the same entries.
	if err := m.LendingAutoDebitAppendDue(101, loanIDFromSeed(3)); err != nil {
		t.Fatalf("append day 101: %v", err)
	}

	day100, err := m.LendingAutoDebitDueOnDay(100)
	if err != nil {
		t.Fatalf("read day 100: %v", err)
	}
	if len(day100) != 2 || day100[0] != loanA || day100[1] != loanB {
		t.Fatalf("expected [loanA, loanB] in insertion order, got %v", day100)
	}

	day101, err := m.LendingAutoDebitDueOnDay(101)
	if err != nil {
		t.Fatalf("read day 101: %v", err)
	}
	if len(day101) != 1 || day101[0] != loanIDFromSeed(3) {
		t.Fatalf("expected day 101 isolated from day 100, got %v", day101)
	}

	if err := m.LendingAutoDebitClearDue(100); err != nil {
		t.Fatalf("clear day 100: %v", err)
	}
	cleared, err := m.LendingAutoDebitDueOnDay(100)
	if err != nil {
		t.Fatalf("read cleared day: %v", err)
	}
	if len(cleared) != 0 {
		t.Fatalf("expected day 100 empty after clear, got %d entries", len(cleared))
	}
	// Clearing one day must not disturb another.
	day101Again, err := m.LendingAutoDebitDueOnDay(101)
	if err != nil {
		t.Fatalf("read day 101 after clearing day 100: %v", err)
	}
	if len(day101Again) != 1 {
		t.Fatalf("expected day 101 unaffected by clearing day 100, got %d entries", len(day101Again))
	}
}

// TestLendingAutoDebitWatermarkRoundTrips proves the watermark correctly
// reports "no watermark yet" on a fresh chain, then persists and returns
// whatever was last set.
func TestLendingAutoDebitWatermarkRoundTrips(t *testing.T) {
	m := newTestManagerForLendingAutoDebit(t)

	_, ok, err := m.LendingAutoDebitLastProcessedDay()
	if err != nil {
		t.Fatalf("read fresh watermark: %v", err)
	}
	if ok {
		t.Fatal("expected no watermark on a fresh chain")
	}

	if err := m.LendingAutoDebitSetLastProcessedDay(42); err != nil {
		t.Fatalf("set watermark: %v", err)
	}
	day, ok, err := m.LendingAutoDebitLastProcessedDay()
	if err != nil {
		t.Fatalf("read watermark: %v", err)
	}
	if !ok || day != 42 {
		t.Fatalf("expected watermark 42, got day=%d ok=%v", day, ok)
	}
}

// TestLendingAutoDebitAppendDueDoesNotDeduplicate mirrors
// SubscriptionsAppendDue's own documented rationale: re-bucketing the same
// loan ID into a day it's already scheduled for (a rare but possible race
// between retry scheduling and manual re-processing) must not be silently
// deduplicated away.
func TestLendingAutoDebitAppendDueDoesNotDeduplicate(t *testing.T) {
	m := newTestManagerForLendingAutoDebit(t)
	loanA := loanIDFromSeed(9)

	if err := m.LendingAutoDebitAppendDue(5, loanA); err != nil {
		t.Fatalf("first append: %v", err)
	}
	if err := m.LendingAutoDebitAppendDue(5, loanA); err != nil {
		t.Fatalf("second append: %v", err)
	}

	entries, err := m.LendingAutoDebitDueOnDay(5)
	if err != nil {
		t.Fatalf("read day: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected the duplicate append to be preserved (2 entries), got %d", len(entries))
	}
}
