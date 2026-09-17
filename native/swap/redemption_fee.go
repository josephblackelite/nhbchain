package swap

import "math/big"

// Conservative defaults for the redeem-side (swap-out) redemption fee,
// applied by core/swap_risk_params.go's effectiveRedemptionFeeParameters
// whenever no policy.redemptionFeeParams governance proposal has ever
// executed. The fee itself is charged off-chain (see
// native/swap/redeem_risk.go's DefaultRedeemPerTxMinWei doc comment for why
// the on-chain minimum floor exists), but its rate is governance-controlled
// on-chain so it is transparent and adjustable without redeploying the
// off-chain payout service.
//
// Grounded in real comparables (2026-09-17): Tether's own institutional
// redemption fee is "greater of $1,000 or 0.1%"; OTC desks quote 5-25bps on
// $100k-$5M settlements; TRON network cost per TRC20 transfer is roughly
// $0.10-$4.50 regardless of amount. A 1% rate with a $1 floor and $1,000 cap
// tracks these: below ~$100 the floor dominates (comfortably above the
// worst-case network cost), between ~$100 and $100,000 it's a clean 1%, and
// above $100,000 the cap makes the effective rate decline toward Tether's
// own 0.1% and below, exactly like Circle/Wise's large-transfer pricing.
const (
	// DefaultRedemptionFeeBps is 100 basis points (1%).
	DefaultRedemptionFeeBps = 100
	// DefaultRedemptionFeeFloorWei is $1, expressed in NHB's 18-decimal wei
	// scale (NHB is 1:1 USD-pegged).
	DefaultRedemptionFeeFloorWei = "1000000000000000000"
	// DefaultRedemptionFeeCapWei is $1,000.
	DefaultRedemptionFeeCapWei = "1000000000000000000000"
)

// RedemptionFeeParameters is the runtime-ready, big.Int form of the
// redemption fee policy, resolved by
// core/swap_risk_params.go's effectiveRedemptionFeeParameters from the
// governance param store (if ever set) falling back to the defaults above.
type RedemptionFeeParameters struct {
	FeeBps      uint32
	FeeFloorWei *big.Int
	FeeCapWei   *big.Int
}

// ComputeRedemptionFee returns the fee owed on a redemption of amountWei,
// computed as (amountWei * FeeBps / 10_000) clamped to [FeeFloorWei,
// FeeCapWei]. This is the single source of truth for the formula -- both the
// swap_getRedemptionFeeParams RPC (for previewing a quote) and the off-chain
// payout service that actually deducts the fee must compute it identically,
// or a customer's on-screen quote could silently diverge from what they're
// actually paid. Returns a non-negative result for any non-negative
// amountWei; a nil or negative amountWei is treated as zero.
func ComputeRedemptionFee(amountWei *big.Int, params RedemptionFeeParameters) *big.Int {
	if amountWei == nil || amountWei.Sign() <= 0 {
		return big.NewInt(0)
	}
	fee := new(big.Int).Mul(amountWei, big.NewInt(int64(params.FeeBps)))
	fee.Div(fee, big.NewInt(10_000))
	if params.FeeFloorWei != nil && fee.Cmp(params.FeeFloorWei) < 0 {
		fee = new(big.Int).Set(params.FeeFloorWei)
	}
	if params.FeeCapWei != nil && fee.Cmp(params.FeeCapWei) > 0 {
		fee = new(big.Int).Set(params.FeeCapWei)
	}
	return fee
}
