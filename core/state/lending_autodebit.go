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
