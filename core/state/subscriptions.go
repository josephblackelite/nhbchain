package state

import (
	"encoding/binary"
	"fmt"

	"nhbchain/native/subscriptions"
)

var (
	subscriptionsDueListPrefix   = []byte("subscriptions/due/")
	subscriptionsWatermarkKey    = []byte("subscriptions/watermark")
	subscriptionsPlanSequenceKey = []byte("subscriptions/seq/plan")
	subscriptionsSubSequenceKey  = []byte("subscriptions/seq/sub")
)

func subscriptionsDueListKey(day uint64) []byte {
	buf := make([]byte, len(subscriptionsDueListPrefix)+8)
	copy(buf, subscriptionsDueListPrefix)
	binary.BigEndian.PutUint64(buf[len(subscriptionsDueListPrefix):], day)
	return buf
}

// SubscriptionsDueOnDay returns every subscription ID due for a charge
// attempt on the given UTC day number (unix seconds / 86400), in insertion
// order. Mirrors BuybackAsksForEpoch exactly, bucketed by calendar day
// instead of validator epoch -- subscription billing cadence
// (IntervalSeconds, typically monthly) has no natural relationship to
// epoch length, so the settlement hook that reads this
// (core/subscriptions_settlement.go) attaches at the unconditional top of
// ProcessBlockLifecycle rather than inside finalizeEpoch. Returns an
// empty, non-nil slice if none exist.
func (m *Manager) SubscriptionsDueOnDay(day uint64) ([]subscriptions.SubscriptionID, error) {
	var raw []uint64
	if err := m.KVGetList(subscriptionsDueListKey(day), &raw); err != nil {
		return nil, fmt.Errorf("subscriptions: load due list for day %d: %w", day, err)
	}
	ids := make([]subscriptions.SubscriptionID, len(raw))
	for i := range raw {
		ids[i] = subscriptions.SubscriptionID(raw[i])
	}
	return ids, nil
}

// SubscriptionsAppendDue schedules a subscription for a charge attempt on
// the given UTC day number. A full read-modify-write via KVPut rather than
// a de-duping append, matching BuybackAppendAsk's rationale: re-bucketing
// the same subscription ID into a day it was already scheduled for (a
// rare but possible race between retry scheduling and manual
// re-processing) must not be silently deduplicated away.
func (m *Manager) SubscriptionsAppendDue(day uint64, id subscriptions.SubscriptionID) error {
	existing, err := m.SubscriptionsDueOnDay(day)
	if err != nil {
		return err
	}
	raw := make([]uint64, 0, len(existing)+1)
	for _, e := range existing {
		raw = append(raw, uint64(e))
	}
	raw = append(raw, uint64(id))
	return m.KVPut(subscriptionsDueListKey(day), raw)
}

// SubscriptionsClearDue removes a fully-processed day's due-list bucket.
func (m *Manager) SubscriptionsClearDue(day uint64) error {
	return m.KVDelete(subscriptionsDueListKey(day))
}

// SubscriptionsLastProcessedDay returns the UTC day number the settlement
// hook last finished processing through (inclusive), and whether a
// watermark has ever been recorded. A brand-new chain has no watermark --
// callers must decide the correct starting point themselves (the current
// day, so nothing pre-genesis is ever scanned).
func (m *Manager) SubscriptionsLastProcessedDay() (uint64, bool, error) {
	var day uint64
	ok, err := m.KVGet(subscriptionsWatermarkKey, &day)
	if err != nil {
		return 0, false, fmt.Errorf("subscriptions: load watermark: %w", err)
	}
	return day, ok, nil
}

// SubscriptionsSetLastProcessedDay persists the watermark after the
// settlement hook finishes processing a day (or a catch-up range of days).
func (m *Manager) SubscriptionsSetLastProcessedDay(day uint64) error {
	return m.KVPut(subscriptionsWatermarkKey, day)
}

// SubscriptionsNextPlanID returns the next unused PlanID and durably
// advances the counter, mirroring GovernanceNextProposalID exactly.
func (m *Manager) SubscriptionsNextPlanID() (subscriptions.PlanID, error) {
	next, err := m.nextSequence(subscriptionsPlanSequenceKey)
	if err != nil {
		return 0, fmt.Errorf("subscriptions: plan sequence: %w", err)
	}
	return subscriptions.PlanID(next), nil
}

// SubscriptionsNextSubscriptionID returns the next unused SubscriptionID
// and durably advances the counter.
func (m *Manager) SubscriptionsNextSubscriptionID() (subscriptions.SubscriptionID, error) {
	next, err := m.nextSequence(subscriptionsSubSequenceKey)
	if err != nil {
		return 0, fmt.Errorf("subscriptions: subscription sequence: %w", err)
	}
	return subscriptions.SubscriptionID(next), nil
}

func (m *Manager) nextSequence(key []byte) (uint64, error) {
	var current uint64
	ok, err := m.KVGet(key, &current)
	if err != nil {
		return 0, err
	}
	if ok && current == ^uint64(0) {
		return 0, fmt.Errorf("sequence overflow")
	}
	next := current + 1
	if err := m.KVPut(key, next); err != nil {
		return 0, err
	}
	return next, nil
}
