package subscriptions

import "math/big"

// ChargeDecision is the pure, deterministic outcome of attempting to settle
// one cycle of a Subscription against a live balance snapshot. A pure
// function with no trie/account access -- fully unit-testable without a
// live chain, mirroring native/loyalty's engine/*State interface split and
// core/tokenomics/buyback's pure fill-math package.
type ChargeDecision struct {
	Success           bool
	FeeWei            *big.Int
	MerchantNetWei    *big.Int
	NewStatus         SubscriptionStatus
	NewFailedAttempts uint32
	NextChargeAt      uint64
	FailureReason     string
}

// ComputeManagementFee applies Config.ManagementFeeBps to a charge amount
// using the same bps-on-gross, big.Int-only convention every other fee in
// this codebase follows (native/fees.Apply, native/lending's Borrow
// developer fee). ManagementFeeBps is already bounded by
// ManagementFeeCapBps at configuration time (Config.Validate), so no
// additional capping is needed here beyond never exceeding the charge
// amount itself.
func ComputeManagementFee(amountWei *big.Int, cfg Config) *big.Int {
	if amountWei == nil || amountWei.Sign() <= 0 || cfg.ManagementFeeBps == 0 {
		return big.NewInt(0)
	}
	fee := new(big.Int).Mul(amountWei, big.NewInt(int64(cfg.ManagementFeeBps)))
	fee.Quo(fee, big.NewInt(ManagementFeeBpsDenominator))
	if fee.Cmp(amountWei) > 0 {
		fee = new(big.Int).Set(amountWei)
	}
	return fee
}

// DecideCharge computes the outcome of one settlement attempt against a
// subscription that is currently due: whether the payer's live balance
// covers the subscription's snapshotted PriceWei, and the resulting state
// transition either way. payerBalanceWei is the caller's live balance in
// the subscription's own Asset -- the engine never touches account state
// itself; core/subscriptions_settlement.go owns reading the live balance
// and applying the debit/credit this decision implies.
//
// On success: FailedAttempts resets to zero, NextChargeAt advances by the
// subscription's own IntervalSeconds (snapshotted at subscribe time, see
// Subscription's doc comment).
//
// On failure: FailedAttempts increments. If it has now reached
// cfg.MaxRetries, the subscription is permanently suspended (dropped from
// the due-index, no NextChargeAt). Otherwise it is marked past-due and
// re-scheduled cfg.RetryIntervalSeconds out for another attempt --
// distinct from the Plan's own billing cadence, matching a real dunning
// retry schedule rather than simply waiting a full cycle to try again.
func DecideCharge(sub *Subscription, cfg Config, payerBalanceWei *big.Int, now uint64) ChargeDecision {
	price := cloneBigInt(sub.PriceWei)
	balance := cloneBigInt(payerBalanceWei)

	if balance.Cmp(price) >= 0 {
		fee := ComputeManagementFee(price, cfg)
		net := new(big.Int).Sub(price, fee)
		return ChargeDecision{
			Success:           true,
			FeeWei:            fee,
			MerchantNetWei:    net,
			NewStatus:         SubscriptionStatusActive,
			NewFailedAttempts: 0,
			NextChargeAt:      now + sub.IntervalSeconds,
		}
	}

	attempts := sub.FailedAttempts + 1
	if attempts >= cfg.MaxRetries {
		return ChargeDecision{
			Success:           false,
			FeeWei:            big.NewInt(0),
			MerchantNetWei:    big.NewInt(0),
			NewStatus:         SubscriptionStatusSuspended,
			NewFailedAttempts: attempts,
			NextChargeAt:      0,
			FailureReason:     "insufficient_balance_max_retries_exceeded",
		}
	}
	return ChargeDecision{
		Success:           false,
		FeeWei:            big.NewInt(0),
		MerchantNetWei:    big.NewInt(0),
		NewStatus:         SubscriptionStatusPastDue,
		NewFailedAttempts: attempts,
		NextChargeAt:      now + cfg.RetryIntervalSeconds,
		FailureReason:     "insufficient_balance",
	}
}
