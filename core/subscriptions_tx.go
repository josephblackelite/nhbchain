package core

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/subscriptions"
)

// applySubscriptionCreatePlanTransaction handles TxTypeSubscriptionCreatePlan.
// The plan's Merchant is always the transaction's own recovered signer,
// never a client-supplied field -- the same discipline
// TxTypeLendingCreatePool's DeveloperOwner fix established for exactly the
// same reason (see core/lending_native.go's applyLendingCreatePoolTransaction
// doc comment).
func (sp *StateProcessor) applySubscriptionCreatePlanTransaction(tx *types.Transaction, sender []byte) error {
	var payload struct {
		Name               string
		PriceWei           *big.Int
		Asset              string
		IntervalSeconds    uint64
		TrialPeriodSeconds uint64
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("subscriptionCreatePlan: decode payload: %w", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	id, err := manager.SubscriptionsNextPlanID()
	if err != nil {
		return fmt.Errorf("subscriptionCreatePlan: assign plan id: %w", err)
	}

	var merchant [20]byte
	copy(merchant[:], sender)
	plan := &subscriptions.Plan{
		ID:                 id,
		Merchant:           merchant,
		Name:               payload.Name,
		PriceWei:           payload.PriceWei,
		Asset:              subscriptions.Asset(payload.Asset),
		IntervalSeconds:    payload.IntervalSeconds,
		TrialPeriodSeconds: payload.TrialPeriodSeconds,
		Active:             true,
		CreatedAt:          uint64(sp.blockTimestamp().Unix()),
	}

	registry := subscriptions.NewRegistry(manager)
	registry.SetPauses(sp.pauses)
	if err := registry.CreatePlan(merchant, plan); err != nil {
		return fmt.Errorf("subscriptionCreatePlan: %w", err)
	}

	if evt := (events.SubscriptionPlanCreated{
		PlanID:          uint64(plan.ID),
		Merchant:        merchant,
		PriceWei:        plan.PriceWei,
		Asset:           string(plan.Asset),
		IntervalSeconds: plan.IntervalSeconds,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}

	return sp.incrementNativeAccountNonce(sender)
}

// applySubscriptionUpdatePlanTransaction handles TxTypeSubscriptionUpdatePlan.
// Only Name and Active are mutable -- see subscriptions.Plan's doc comment
// for why pricing terms are permanently fixed once a plan exists.
func (sp *StateProcessor) applySubscriptionUpdatePlanTransaction(tx *types.Transaction, sender []byte) error {
	var payload struct {
		PlanID uint64
		Name   string
		Active bool
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("subscriptionUpdatePlan: decode payload: %w", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	registry := subscriptions.NewRegistry(manager)
	registry.SetPauses(sp.pauses)

	var caller [20]byte
	copy(caller[:], sender)
	plan, err := registry.UpdatePlan(caller, subscriptions.PlanID(payload.PlanID), payload.Name, payload.Active)
	if err != nil {
		return fmt.Errorf("subscriptionUpdatePlan: %w", err)
	}

	if evt := (events.SubscriptionPlanUpdated{
		PlanID: uint64(plan.ID),
		Name:   plan.Name,
		Active: plan.Active,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}

	return sp.incrementNativeAccountNonce(sender)
}

// applySubscriptionSubscribeTransaction handles TxTypeSubscriptionSubscribe
// -- the single most important transaction in this module. The payer's
// envelope signature on THIS transaction is their entire standing
// authorization: no further signature is ever required for the chain to
// debit their account PriceWei every IntervalSeconds (see
// core/subscriptions_settlement.go's doc comment for the full safety
// argument). This transaction deliberately never moves any balance itself
// -- Subscribe only ever schedules a subscription into the due-index;
// settleSubscriptionCharges (core/subscriptions_settlement.go) is the sole
// code path that debits a payer, including for the very first charge. That
// invariant (exactly one money-moving path) is what keeps the mandate
// model simple to reason about and test.
func (sp *StateProcessor) applySubscriptionSubscribeTransaction(tx *types.Transaction, sender []byte) error {
	if !sp.hasSubscriptionsConfig {
		return fmt.Errorf("subscriptionSubscribe: subscriptions engine is not configured for this network")
	}
	var payload struct {
		PlanID uint64
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("subscriptionSubscribe: decode payload: %w", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	registry := subscriptions.NewRegistry(manager)
	registry.SetPauses(sp.pauses)

	plan, ok := registry.GetPlan(subscriptions.PlanID(payload.PlanID))
	if !ok {
		return fmt.Errorf("subscriptionSubscribe: plan %d not found", payload.PlanID)
	}
	if !plan.Active {
		return fmt.Errorf("subscriptionSubscribe: %w", subscriptions.ErrPlanInactive)
	}

	id, err := manager.SubscriptionsNextSubscriptionID()
	if err != nil {
		return fmt.Errorf("subscriptionSubscribe: assign subscription id: %w", err)
	}

	var payer [20]byte
	copy(payer[:], sender)

	now := uint64(sp.blockTimestamp().Unix())
	// The first charge is due exactly TrialPeriodSeconds from now (zero
	// for a plan with no trial, meaning it is picked up the moment
	// today's due-index bucket is next processed) -- never charged
	// synchronously inside this transaction, preserving the "Subscribe
	// never moves money" invariant above.
	nextChargeAt := now + plan.TrialPeriodSeconds

	sub := &subscriptions.Subscription{
		ID:              id,
		PlanID:          plan.ID,
		Payer:           payer,
		Merchant:        plan.Merchant,
		PriceWei:        plan.PriceWei,
		Asset:           plan.Asset,
		IntervalSeconds: plan.IntervalSeconds,
		Status:          subscriptions.SubscriptionStatusActive,
		StartAt:         now,
		NextChargeAt:    nextChargeAt,
		CreatedAt:       now,
	}

	if err := registry.CreateSubscription(sub); err != nil {
		return fmt.Errorf("subscriptionSubscribe: %w", err)
	}

	dueDay := nextChargeAt / secondsPerDay
	if err := manager.SubscriptionsAppendDue(dueDay, sub.ID); err != nil {
		return fmt.Errorf("subscriptionSubscribe: schedule first charge: %w", err)
	}

	if evt := (events.SubscriptionCreated{
		SubscriptionID: uint64(sub.ID),
		PlanID:         uint64(plan.ID),
		Payer:          payer,
		Merchant:       plan.Merchant,
		PriceWei:       sub.PriceWei,
		Asset:          string(sub.Asset),
		NextChargeAt:   nextChargeAt,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}

	return sp.incrementNativeAccountNonce(sender)
}

// applySubscriptionCancelTransaction handles TxTypeSubscriptionCancel. The
// cancelled subscription is left in whatever due-index bucket it currently
// sits in -- settleSubscriptionCharges checks live Status on every entry it
// processes and silently drops any that are already terminal, rather than
// this transaction needing to find and rewrite that bucket (which would
// require either a reverse index or a linear scan neither of which this
// module otherwise needs).
func (sp *StateProcessor) applySubscriptionCancelTransaction(tx *types.Transaction, sender []byte) error {
	var payload struct {
		SubscriptionID uint64
	}
	if err := rlp.DecodeBytes(tx.Data, &payload); err != nil {
		return fmt.Errorf("subscriptionCancel: decode payload: %w", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	registry := subscriptions.NewRegistry(manager)
	registry.SetPauses(sp.pauses)

	var caller [20]byte
	copy(caller[:], sender)
	sub, err := registry.CancelSubscription(caller, subscriptions.SubscriptionID(payload.SubscriptionID), uint64(sp.blockTimestamp().Unix()))
	if err != nil {
		return fmt.Errorf("subscriptionCancel: %w", err)
	}

	if evt := (events.SubscriptionCancelled{
		SubscriptionID: uint64(sub.ID),
		Payer:          sub.Payer,
		Merchant:       sub.Merchant,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}

	return sp.incrementNativeAccountNonce(sender)
}
