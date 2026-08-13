package core

import (
	"fmt"
	"math/big"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/subscriptions"
)

// settleSubscriptionCharges is called unconditionally at the top of
// ProcessBlockLifecycle (core/epochs.go), every block -- NOT gated by
// epoch length like settleEpochRewards/settleBuybackEpoch, since
// subscription billing cadence (Plan.IntervalSeconds, typically monthly)
// has no natural relationship to validator-epoch length. Internally
// day-gated instead: compares the current block's UTC day number against
// a persisted watermark (state.Manager's SubscriptionsLastProcessedDay)
// and processes every day from watermark+1 through today, catch-up-safe
// if a chain restart or long block gap ever skipped a day entirely --
// mirrors the persist-a-watermark/catch-up-safe shape POTSO reward
// processing already established.
//
// THE CORE SAFETY ARGUMENT for why this function may debit a payer's
// account with ZERO fresh signature at charge time: the debited amount
// (Subscription.PriceWei) is fixed, bounded, and was explicitly disclosed
// and authorized by the payer's own envelope signature on the original
// TxTypeSubscriptionSubscribe transaction (core/subscriptions_tx.go) --
// never an open-ended or system-chosen amount. This is the same "bounded
// standing authorization" discipline every other system-initiated debit
// on this chain already follows: settleEpochRewards (core/rewards_logic.go)
// debits the admin wallet by a schedule-computed amount every epoch with
// zero fresh signature, and settleBuybackEpoch (core/buyback_settlement.go)
// pulls from an escrow account funded by the seller's own earlier signed
// ask. A subscription charge never goes through applyTransactionFee at
// all (native/fees) -- it is not a TxTypeTransfer/TxTypeTransferZNHB, so
// the ordinary 1.5% MDR transfer fee never applies to it; ManagementFeeBps
// computed below is a wholly separate platform fee, charged alongside (not
// instead of) that transfer fee, exactly as directed.
//
// A failure to charge one subscription (insufficient balance) NEVER
// returns an error from this function -- that would abort block
// production over an ordinary, expected business outcome. Only genuine
// internal/storage errors propagate.
func (sp *StateProcessor) settleSubscriptionCharges(timestamp int64) error {
	if !sp.hasSubscriptionsConfig || timestamp < 0 {
		return nil
	}
	manager := nhbstate.NewManager(sp.Trie)
	today := uint64(timestamp) / secondsPerDay

	// The watermark only ever advances to today-1, NEVER to today itself:
	// today's due-index bucket can still receive brand-new entries later
	// in the same calendar day (a Subscribe transaction in a later block,
	// or a same-day retry re-bucketing after a failed charge), so it must
	// be re-scanned every block for as long as it remains "today" --
	// cheap, since settleSubscriptionsDueOnDay clears whatever it
	// processes, leaving nothing but genuinely new entries to find on the
	// next pass. Only a day that has fully elapsed (today has moved past
	// it) can never receive another entry and is safe to mark closed
	// forever.
	lastClosed, hasWatermark, err := manager.SubscriptionsLastProcessedDay()
	if err != nil {
		return err
	}
	startDay := uint64(0)
	if hasWatermark {
		startDay = lastClosed + 1
	}
	if startDay > today {
		startDay = today
	}

	registry := subscriptions.NewRegistry(manager)
	registry.SetPauses(sp.pauses)
	cfg := sp.subscriptionsConfig

	for day := startDay; day <= today; day++ {
		if err := sp.settleSubscriptionsDueOnDay(manager, registry, cfg, day, uint64(timestamp)); err != nil {
			return err
		}
	}

	if today == 0 {
		// No day has fully elapsed yet -- nothing can be safely closed.
		return nil
	}
	newWatermark := today - 1
	if hasWatermark && newWatermark <= lastClosed {
		return nil
	}
	return manager.SubscriptionsSetLastProcessedDay(newWatermark)
}

func (sp *StateProcessor) settleSubscriptionsDueOnDay(manager *nhbstate.Manager, registry *subscriptions.Registry, cfg subscriptions.Config, day uint64, now uint64) error {
	due, err := manager.SubscriptionsDueOnDay(day)
	if err != nil {
		return err
	}
	if len(due) == 0 {
		return nil
	}
	for _, subID := range due {
		if err := sp.settleOneSubscriptionCharge(manager, registry, cfg, subID, now); err != nil {
			return err
		}
	}
	return manager.SubscriptionsClearDue(day)
}

func (sp *StateProcessor) settleOneSubscriptionCharge(manager *nhbstate.Manager, registry *subscriptions.Registry, cfg subscriptions.Config, subID subscriptions.SubscriptionID, now uint64) error {
	sub, ok := registry.GetSubscription(subID)
	if !ok {
		// Nothing to do -- not an error.
		return nil
	}
	// A subscription can reach a terminal status (payer/merchant/admin
	// cancellation) while still sitting in a due-index bucket -- see
	// applySubscriptionCancelTransaction's doc comment for why
	// cancellation never rewrites the bucket it happens to be in. Skip
	// silently: this is expected, not a bug.
	if sub.Status != subscriptions.SubscriptionStatusActive && sub.Status != subscriptions.SubscriptionStatusPastDue {
		return nil
	}

	existingCharges, err := registry.ListCharges(subID)
	if err != nil {
		return fmt.Errorf("subscriptions: load charge history for %d: %w", subID, err)
	}
	attemptNumber := uint32(len(existingCharges) + 1)

	payerAcc, err := sp.getAccount(sub.Payer[:])
	if err != nil {
		return fmt.Errorf("subscriptions: load payer %x: %w", sub.Payer, err)
	}
	balance := assetBalance(payerAcc, sub.Asset)

	decision := subscriptions.DecideCharge(sub, cfg, balance, now)

	if decision.Success {
		return sp.applySuccessfulSubscriptionCharge(manager, registry, sub, decision, payerAcc, balance, cfg, attemptNumber, now)
	}
	return sp.applyFailedSubscriptionCharge(manager, registry, sub, decision, attemptNumber, now)
}

func (sp *StateProcessor) applySuccessfulSubscriptionCharge(manager *nhbstate.Manager, registry *subscriptions.Registry, sub *subscriptions.Subscription, decision subscriptions.ChargeDecision, payerAcc *types.Account, payerBalance *big.Int, cfg subscriptions.Config, attemptNumber uint32, now uint64) error {
	merchantAcc, err := sp.getAccount(sub.Merchant[:])
	if err != nil {
		return fmt.Errorf("subscriptions: load merchant %x: %w", sub.Merchant, err)
	}

	var treasuryAcc *types.Account
	if decision.FeeWei.Sign() > 0 {
		treasuryAcc, err = sp.getAccount(cfg.Treasury[:])
		if err != nil {
			return fmt.Errorf("subscriptions: load treasury: %w", err)
		}
	}

	setAssetBalance(payerAcc, sub.Asset, new(big.Int).Sub(payerBalance, sub.PriceWei))
	addAssetBalance(merchantAcc, sub.Asset, decision.MerchantNetWei)
	if treasuryAcc != nil {
		addAssetBalance(treasuryAcc, sub.Asset, decision.FeeWei)
	}

	if err := sp.setAccount(sub.Payer[:], payerAcc); err != nil {
		return err
	}
	if err := sp.setAccount(sub.Merchant[:], merchantAcc); err != nil {
		return err
	}
	if treasuryAcc != nil {
		if err := sp.setAccount(cfg.Treasury[:], treasuryAcc); err != nil {
			return err
		}
	}

	sub.Status = decision.NewStatus
	sub.FailedAttempts = decision.NewFailedAttempts
	sub.CycleCount++
	sub.LastChargeAt = now
	sub.LastChargeStatus = subscriptions.ChargeStatusPaid
	sub.NextChargeAt = decision.NextChargeAt

	if err := registry.PutSubscription(sub); err != nil {
		return err
	}
	if err := registry.AppendCharge(sub.ID, subscriptions.Charge{
		SubscriptionID: sub.ID,
		PlanID:         sub.PlanID,
		Payer:          sub.Payer,
		Merchant:       sub.Merchant,
		Asset:          sub.Asset,
		AmountWei:      new(big.Int).Set(sub.PriceWei),
		FeeWei:         decision.FeeWei,
		Status:         subscriptions.ChargeStatusPaid,
		AttemptNumber:  attemptNumber,
		ChargedAt:      now,
	}); err != nil {
		return err
	}

	nextDay := decision.NextChargeAt / secondsPerDay
	if err := manager.SubscriptionsAppendDue(nextDay, sub.ID); err != nil {
		return err
	}

	if evt := (events.SubscriptionChargeSucceeded{
		SubscriptionID: uint64(sub.ID),
		Payer:          sub.Payer,
		Merchant:       sub.Merchant,
		Asset:          string(sub.Asset),
		AmountWei:      sub.PriceWei,
		FeeWei:         decision.FeeWei,
		AttemptNumber:  attemptNumber,
		NextChargeAt:   decision.NextChargeAt,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}

func (sp *StateProcessor) applyFailedSubscriptionCharge(manager *nhbstate.Manager, registry *subscriptions.Registry, sub *subscriptions.Subscription, decision subscriptions.ChargeDecision, attemptNumber uint32, now uint64) error {
	sub.Status = decision.NewStatus
	sub.FailedAttempts = decision.NewFailedAttempts
	sub.LastChargeAt = now
	sub.LastChargeStatus = subscriptions.ChargeStatusFailed
	sub.NextChargeAt = decision.NextChargeAt

	if err := registry.PutSubscription(sub); err != nil {
		return err
	}
	if err := registry.AppendCharge(sub.ID, subscriptions.Charge{
		SubscriptionID: sub.ID,
		PlanID:         sub.PlanID,
		Payer:          sub.Payer,
		Merchant:       sub.Merchant,
		Asset:          sub.Asset,
		AmountWei:      big.NewInt(0),
		FeeWei:         big.NewInt(0),
		Status:         subscriptions.ChargeStatusFailed,
		AttemptNumber:  attemptNumber,
		ChargedAt:      now,
		FailureReason:  decision.FailureReason,
	}); err != nil {
		return err
	}

	if decision.NewStatus == subscriptions.SubscriptionStatusSuspended {
		if evt := (events.SubscriptionSuspended{
			SubscriptionID: uint64(sub.ID),
			Payer:          sub.Payer,
			Merchant:       sub.Merchant,
			FailedAttempts: sub.FailedAttempts,
		}).Event(); evt != nil {
			sp.AppendEvent(evt)
		}
	} else {
		nextDay := decision.NextChargeAt / secondsPerDay
		if err := manager.SubscriptionsAppendDue(nextDay, sub.ID); err != nil {
			return err
		}
	}

	if evt := (events.SubscriptionChargeFailed{
		SubscriptionID: uint64(sub.ID),
		Payer:          sub.Payer,
		Merchant:       sub.Merchant,
		AttemptNumber:  attemptNumber,
		FailureReason:  decision.FailureReason,
		NewStatus:      subscriptionStatusLabel(decision.NewStatus),
		NextChargeAt:   decision.NextChargeAt,
	}).Event(); evt != nil {
		sp.AppendEvent(evt)
	}
	return nil
}

func assetBalance(acc *types.Account, asset subscriptions.Asset) *big.Int {
	var v *big.Int
	if asset == subscriptions.AssetZNHB {
		v = acc.BalanceZNHB
	} else {
		v = acc.BalanceNHB
	}
	if v == nil {
		return big.NewInt(0)
	}
	return v
}

func setAssetBalance(acc *types.Account, asset subscriptions.Asset, value *big.Int) {
	if asset == subscriptions.AssetZNHB {
		acc.BalanceZNHB = value
		return
	}
	acc.BalanceNHB = value
}

func addAssetBalance(acc *types.Account, asset subscriptions.Asset, delta *big.Int) {
	if delta == nil || delta.Sign() == 0 {
		return
	}
	current := assetBalance(acc, asset)
	setAssetBalance(acc, asset, new(big.Int).Add(current, delta))
}

func subscriptionStatusLabel(status subscriptions.SubscriptionStatus) string {
	switch status {
	case subscriptions.SubscriptionStatusActive:
		return "active"
	case subscriptions.SubscriptionStatusPastDue:
		return "past_due"
	case subscriptions.SubscriptionStatusCancelled:
		return "cancelled"
	case subscriptions.SubscriptionStatusSuspended:
		return "suspended"
	default:
		return "unknown"
	}
}
