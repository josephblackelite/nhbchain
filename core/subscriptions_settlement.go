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
	// cheap, since settleSubscriptionsDueOnDay removes whatever it
	// processes, leaving nothing but genuinely new entries (and entries that
	// are not due yet) to find on the next pass. Only a day that has fully elapsed (today has moved past
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
	// Settling an entry can schedule the same subscription again on this very
	// day: a retry shorter than the time left in the day (a billing interval is
	// at least a day, so the next cycle never lands on it). That entry is
	// appended to the bucket being settled, so the bucket is not cleared as a
	// whole: only the entries read above and handled are removed, and the ones a
	// charge added stay for a later pass, which charges them once they are due.
	handled := make([]subscriptions.SubscriptionID, 0, len(due))
	for _, subID := range due {
		done, err := sp.settleOneSubscriptionCharge(manager, registry, cfg, subID, now)
		if err != nil {
			return err
		}
		if done {
			handled = append(handled, subID)
		}
	}
	return manager.SubscriptionsRemoveDue(day, handled)
}

// settleOneSubscriptionCharge charges one due-list entry. It reports whether
// the entry is finished with -- charged, failed, or dropped because its
// subscription is gone or terminal -- and false when the entry is not due yet
// and has to stay in its bucket.
func (sp *StateProcessor) settleOneSubscriptionCharge(manager *nhbstate.Manager, registry *subscriptions.Registry, cfg subscriptions.Config, subID subscriptions.SubscriptionID, now uint64) (bool, error) {
	sub, ok := registry.GetSubscription(subID)
	if !ok {
		// Nothing to do -- not an error.
		return true, nil
	}
	// A subscription can reach a terminal status (payer/merchant/admin
	// cancellation) while still sitting in a due-index bucket -- see
	// applySubscriptionCancelTransaction's doc comment for why
	// cancellation never rewrites the bucket it happens to be in. Skip
	// silently: this is expected, not a bug.
	if sub.Status != subscriptions.SubscriptionStatusActive && sub.Status != subscriptions.SubscriptionStatusPastDue {
		return true, nil
	}
	// The bucket is a calendar day, the charge time is a second. Today's
	// bucket is scanned on every block, so an entry whose own NextChargeAt is
	// still ahead (a retry, or a trial that ends, later today) has to wait for
	// it: charging it on the next block would bill the payer a block after the
	// last attempt instead of one interval after it.
	if sub.NextChargeAt > now {
		return false, nil
	}

	chargeCount, err := registry.ChargeCount(subID)
	if err != nil {
		return false, fmt.Errorf("subscriptions: load charge history for %d: %w", subID, err)
	}
	attemptNumber := uint32(chargeCount + 1)

	payerAcc, err := sp.getAccount(sub.Payer[:])
	if err != nil {
		return false, fmt.Errorf("subscriptions: load payer %x: %w", sub.Payer, err)
	}
	balance := assetBalance(payerAcc, sub.Asset)

	decision := subscriptions.DecideCharge(sub, cfg, balance, now)

	if decision.Success {
		return true, sp.applySuccessfulSubscriptionCharge(manager, registry, sub, decision, payerAcc, balance, cfg, attemptNumber, now)
	}
	return true, sp.applyFailedSubscriptionCharge(manager, registry, sub, decision, attemptNumber, now)
}

func (sp *StateProcessor) applySuccessfulSubscriptionCharge(manager *nhbstate.Manager, registry *subscriptions.Registry, sub *subscriptions.Subscription, decision subscriptions.ChargeDecision, payerAcc *types.Account, payerBalance *big.Int, cfg subscriptions.Config, attemptNumber uint32, now uint64) error {
	// Payer, merchant and fee treasury are independent roles, and any two of
	// them can be one address (a plan subscribed to by its own merchant, a
	// merchant that is also the fee treasury, ...). Every account is persisted
	// from the object it was loaded into, so two objects for one address would
	// be written back one after the other and the later one, which never saw
	// the earlier one's delta, would overwrite it: the debit or a credit
	// would vanish and the charge would mint or destroy its amount. Hence one
	// object per address, all deltas applied to it, and one write.
	merchantAcc := payerAcc
	if sub.Merchant != sub.Payer {
		var err error
		merchantAcc, err = sp.getAccount(sub.Merchant[:])
		if err != nil {
			return fmt.Errorf("subscriptions: load merchant %x: %w", sub.Merchant, err)
		}
	}

	var treasuryAcc *types.Account
	if decision.FeeWei.Sign() > 0 {
		switch cfg.Treasury {
		case sub.Payer:
			treasuryAcc = payerAcc
		case sub.Merchant:
			treasuryAcc = merchantAcc
		default:
			var err error
			treasuryAcc, err = sp.getAccount(cfg.Treasury[:])
			if err != nil {
				return fmt.Errorf("subscriptions: load treasury: %w", err)
			}
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
	if merchantAcc != payerAcc {
		if err := sp.setAccount(sub.Merchant[:], merchantAcc); err != nil {
			return err
		}
	}
	if treasuryAcc != nil && treasuryAcc != payerAcc && treasuryAcc != merchantAcc {
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
