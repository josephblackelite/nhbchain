package state

import (
	"encoding/binary"
	"fmt"
)

var (
	lendingDepositPayoutDueListPrefix = []byte("lending/depositpayout/due/")
	lendingDepositPayoutWatermarkKey  = []byte("lending/depositpayout/watermark")
)

func lendingDepositPayoutDueListKey(day uint64) []byte {
	buf := make([]byte, len(lendingDepositPayoutDueListPrefix)+8)
	copy(buf, lendingDepositPayoutDueListPrefix)
	binary.BigEndian.PutUint64(buf[len(lendingDepositPayoutDueListPrefix):], day)
	return buf
}

// LendingDepositPayoutDueOnDay returns every fixed-term deposit ID due for a
// payout attempt on the given UTC day number (unix seconds / 86400), in
// insertion order. Mirrors LendingAutoDebitDueOnDay exactly (Milestone 3's
// mirror-image due-index, in the opposite direction of money flow). Returns
// an empty, non-nil slice if none exist.
func (m *Manager) LendingDepositPayoutDueOnDay(day uint64) ([][32]byte, error) {
	var raw [][32]byte
	if err := m.KVGetList(lendingDepositPayoutDueListKey(day), &raw); err != nil {
		return nil, fmt.Errorf("lending deposit payout: load due list for day %d: %w", day, err)
	}
	return raw, nil
}

// LendingDepositPayoutAppendDue schedules a fixed-term deposit for a payout
// attempt on the given UTC day number. A full read-modify-write via KVPut
// rather than a de-duping append, matching LendingAutoDebitAppendDue's own
// rationale.
func (m *Manager) LendingDepositPayoutAppendDue(day uint64, depositID [32]byte) error {
	existing, err := m.LendingDepositPayoutDueOnDay(day)
	if err != nil {
		return err
	}
	next := make([][32]byte, 0, len(existing)+1)
	next = append(next, existing...)
	next = append(next, depositID)
	return m.KVPut(lendingDepositPayoutDueListKey(day), next)
}

// LendingDepositPayoutClearDue removes a fully-processed day's due-list
// bucket.
func (m *Manager) LendingDepositPayoutClearDue(day uint64) error {
	return m.KVDelete(lendingDepositPayoutDueListKey(day))
}

// LendingDepositPayoutLastProcessedDay returns the UTC day number the
// settlement hook last finished processing through (inclusive), and
// whether a watermark has ever been recorded.
func (m *Manager) LendingDepositPayoutLastProcessedDay() (uint64, bool, error) {
	var day uint64
	ok, err := m.KVGet(lendingDepositPayoutWatermarkKey, &day)
	if err != nil {
		return 0, false, fmt.Errorf("lending deposit payout: load watermark: %w", err)
	}
	return day, ok, nil
}

// LendingDepositPayoutSetLastProcessedDay persists the watermark after the
// settlement hook finishes processing a day (or a catch-up range of days).
func (m *Manager) LendingDepositPayoutSetLastProcessedDay(day uint64) error {
	return m.KVPut(lendingDepositPayoutWatermarkKey, day)
}
