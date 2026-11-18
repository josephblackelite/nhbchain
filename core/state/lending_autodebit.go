package state

import (
	"encoding/binary"
	"fmt"
)

var (
	lendingAutoDebitDueListPrefix = []byte("lending/autodebit/due/")
	lendingAutoDebitWatermarkKey  = []byte("lending/autodebit/watermark")
)

func lendingAutoDebitDueListKey(day uint64) []byte {
	buf := make([]byte, len(lendingAutoDebitDueListPrefix)+8)
	copy(buf, lendingAutoDebitDueListPrefix)
	binary.BigEndian.PutUint64(buf[len(lendingAutoDebitDueListPrefix):], day)
	return buf
}

// LendingAutoDebitDueOnDay returns every fixed-term loan ID due for an
// auto-debit attempt on the given UTC day number (unix seconds / 86400), in
// insertion order. Mirrors SubscriptionsDueOnDay exactly, bucketed by
// calendar day -- fixed-term billing cadence (30-day cycles) has no natural
// relationship to validator-epoch length, so the settlement hook that reads
// this (core/lending_autodebit_settlement.go) attaches at the unconditional
// top of ProcessBlockLifecycle rather than inside finalizeEpoch. Returns an
// empty, non-nil slice if none exist.
func (m *Manager) LendingAutoDebitDueOnDay(day uint64) ([][32]byte, error) {
	var raw [][32]byte
	if err := m.KVGetList(lendingAutoDebitDueListKey(day), &raw); err != nil {
		return nil, fmt.Errorf("lending autodebit: load due list for day %d: %w", day, err)
	}
	return raw, nil
}

// LendingAutoDebitAppendDue schedules a fixed-term loan for an auto-debit
// attempt on the given UTC day number. A full read-modify-write via KVPut
// rather than a de-duping append, matching SubscriptionsAppendDue's
// rationale: re-bucketing the same loan ID into a day it was already
// scheduled for (a rare but possible race between retry scheduling and
// manual re-processing) must not be silently deduplicated away.
func (m *Manager) LendingAutoDebitAppendDue(day uint64, loanID [32]byte) error {
	existing, err := m.LendingAutoDebitDueOnDay(day)
	if err != nil {
		return err
	}
	next := make([][32]byte, 0, len(existing)+1)
	next = append(next, existing...)
	next = append(next, loanID)
	return m.KVPut(lendingAutoDebitDueListKey(day), next)
}

// LendingAutoDebitClearDue removes a fully-processed day's due-list bucket.
func (m *Manager) LendingAutoDebitClearDue(day uint64) error {
	return m.KVDelete(lendingAutoDebitDueListKey(day))
}

// LendingAutoDebitRemoveDue removes the entries a settlement pass has handled from a
// day's due list and keeps every other entry, in order. One occurrence is removed per
// processed id. An entry the pass itself appended to the day it is settling (a retry or
// a final cycle that lands on the same UTC day) sits behind the entries the pass read,
// so it is never the first occurrence of a processed id and survives; clearing the
// whole bucket destroyed it and the loan was never attempted again (ledger PL-EXT-42).
// When nothing is left the bucket is deleted, which is what LendingAutoDebitClearDue
// leaves behind.
func (m *Manager) LendingAutoDebitRemoveDue(day uint64, processed [][32]byte) error {
	return m.removeDueIDs(lendingAutoDebitDueListKey(day), processed, m.LendingAutoDebitDueOnDay, day)
}

// removeDueIDs is the shared body of the lending due-list removals: read the bucket,
// drop one occurrence of each processed id, write back what is left (or delete the
// bucket when nothing is).
func (m *Manager) removeDueIDs(key []byte, processed [][32]byte, load func(uint64) ([][32]byte, error), day uint64) error {
	if len(processed) == 0 {
		return nil
	}
	existing, err := load(day)
	if err != nil {
		return err
	}
	drop := make(map[[32]byte]int, len(processed))
	for _, id := range processed {
		drop[id]++
	}
	remaining := make([][32]byte, 0, len(existing))
	for _, id := range existing {
		if drop[id] > 0 {
			drop[id]--
			continue
		}
		remaining = append(remaining, id)
	}
	if len(remaining) == 0 {
		return m.KVDelete(key)
	}
	return m.KVPut(key, remaining)
}

// LendingAutoDebitLastProcessedDay returns the UTC day number the settlement
// hook last finished processing through (inclusive), and whether a
// watermark has ever been recorded. A brand-new chain has no watermark --
// callers must decide the correct starting point themselves (the current
// day, so nothing pre-genesis is ever scanned).
func (m *Manager) LendingAutoDebitLastProcessedDay() (uint64, bool, error) {
	var day uint64
	ok, err := m.KVGet(lendingAutoDebitWatermarkKey, &day)
	if err != nil {
		return 0, false, fmt.Errorf("lending autodebit: load watermark: %w", err)
	}
	return day, ok, nil
}

// LendingAutoDebitSetLastProcessedDay persists the watermark after the
// settlement hook finishes processing a day (or a catch-up range of days).
func (m *Manager) LendingAutoDebitSetLastProcessedDay(day uint64) error {
	return m.KVPut(lendingAutoDebitWatermarkKey, day)
}
