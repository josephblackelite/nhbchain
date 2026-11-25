package core

import (
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
)

// Ledger PL-EXT-83: a lending cycle scheduled onto a day the settlement had already closed
// was never attempted again. From LendingClosedDayClampActivationHeight it goes on today.

const clampTestHeight = 100

func clampDay(day uint64, hours int) time.Time {
	return time.Unix(int64(day*86400), 0).UTC().Add(time.Duration(hours) * time.Hour)
}

func bucketHas(t *testing.T, ids [][32]byte, err error, want [32]byte) bool {
	t.Helper()
	if err != nil {
		t.Fatalf("read bucket: %v", err)
	}
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestLendingDepositPayoutOnAClosedDayIsStrandedWithoutTheClampAndAttemptedWithIt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activation uint64
		wantOnDay  uint64 // the day the entry is expected to be put on
		wantScan   bool   // whether a later pass scans (and so clears) it
	}{
		{"rule off", 0, 61, false},
		{"rule on", clampTestHeight, 62, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := newStakingStateProcessor(t)
			sp.SetValidatorRules(ValidatorRules{LendingClosedDayClampActivationHeight: tc.activation})
			manager := nhbstate.NewManager(sp.Trie)
			if err := manager.LendingDepositPayoutSetLastProcessedDay(61); err != nil {
				t.Fatal(err)
			}
			sp.BeginBlock(clampTestHeight, clampDay(62, 1))
			defer sp.EndBlock()

			id := [32]byte{0xD1}
			// The cycle is due on day 61, which the settlement closed; it is scheduled during day 62.
			if err := sp.scheduleNextLendingDepositPayout(manager, id, 61*86400+100); err != nil {
				t.Fatalf("schedule: %v", err)
			}
			ids, err := manager.LendingDepositPayoutDueOnDay(tc.wantOnDay)
			if !bucketHas(t, ids, err, id) {
				t.Fatalf("the entry is not on day %d", tc.wantOnDay)
			}
			other := uint64(61)
			if tc.wantOnDay == 61 {
				other = 62
			}
			if ids, err := manager.LendingDepositPayoutDueOnDay(other); bucketHas(t, ids, err, id) {
				t.Fatalf("the entry is also on day %d", other)
			}

			// A later block's pass.
			sp.BeginBlock(clampTestHeight+1, clampDay(63, 12))
			if err := sp.settleLendingDepositPayouts(clampDay(63, 12).Unix()); err != nil {
				t.Fatalf("settle: %v", err)
			}
			ids, err = manager.LendingDepositPayoutDueOnDay(tc.wantOnDay)
			stillThere := bucketHas(t, ids, err, id)
			if tc.wantScan && stillThere {
				t.Fatalf("the entry on day %d was not scanned by the next pass", tc.wantOnDay)
			}
			if !tc.wantScan && !stillThere {
				t.Fatalf("the stranded entry on day %d disappeared; the old behaviour changed", tc.wantOnDay)
			}
		})
	}
}

func TestLendingAutoDebitOnAClosedDayIsStrandedWithoutTheClampAndAttemptedWithIt(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activation uint64
		wantOnDay  uint64
		wantScan   bool
	}{
		{"rule off", 0, 30, false},
		{"rule on", clampTestHeight, 32, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := newStakingStateProcessor(t)
			sp.SetValidatorRules(ValidatorRules{LendingClosedDayClampActivationHeight: tc.activation})
			manager := nhbstate.NewManager(sp.Trie)
			if err := manager.LendingAutoDebitSetLastProcessedDay(31); err != nil {
				t.Fatal(err)
			}
			sp.BeginBlock(clampTestHeight, clampDay(32, 1))
			defer sp.EndBlock()

			id := [32]byte{0xA1}
			if err := sp.scheduleNextLendingAutoDebit(manager, id, 30*86400+100); err != nil {
				t.Fatalf("schedule: %v", err)
			}
			ids, err := manager.LendingAutoDebitDueOnDay(tc.wantOnDay)
			if !bucketHas(t, ids, err, id) {
				t.Fatalf("the entry is not on day %d", tc.wantOnDay)
			}
			sp.BeginBlock(clampTestHeight+1, clampDay(34, 12))
			if err := sp.settleLendingAutoDebits(clampDay(34, 12).Unix()); err != nil {
				t.Fatalf("settle: %v", err)
			}
			ids, err = manager.LendingAutoDebitDueOnDay(tc.wantOnDay)
			stillThere := bucketHas(t, ids, err, id)
			if tc.wantScan && stillThere {
				t.Fatalf("the entry on day %d was not scanned by the next pass", tc.wantOnDay)
			}
			if !tc.wantScan && !stillThere {
				t.Fatalf("the stranded entry disappeared; the old behaviour changed")
			}
		})
	}
}

func TestLendingDueDayKeepsAFutureDayAndTheClampOnlyMovesPastDaysForward(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetValidatorRules(ValidatorRules{LendingClosedDayClampActivationHeight: clampTestHeight})
	sp.BeginBlock(clampTestHeight, clampDay(62, 1))
	defer sp.EndBlock()
	if got := sp.lendingDueDay(70 * 86400); got != 70 {
		t.Fatalf("a future day moved to %d", got)
	}
	if got := sp.lendingDueDay(62*86400 + 5); got != 62 {
		t.Fatalf("today moved to %d", got)
	}
	if got := sp.lendingDueDay(3 * 86400); got != 62 {
		t.Fatalf("a long past day became %d, want today (62)", got)
	}
	sp.BeginBlock(clampTestHeight-1, clampDay(62, 1))
	if got := sp.lendingDueDay(3 * 86400); got != 3 {
		t.Fatalf("below the activation height the day became %d, want 3", got)
	}
}
