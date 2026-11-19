package governance

import "testing"

// ParamHasValidator says whether a proposal for the key can ever pass validation. The
// ten keys that used to be listed as governable have no rule; the keys that have one do.
func TestParamHasValidator(t *testing.T) {
	for _, key := range []string{
		"fees.baseFee", ParamKeyStakingAprBps, ParamKeyMinimumValidatorStake,
		"potso.weights.AlphaStakeBps", " fees.baseFee ",
	} {
		if !ParamHasValidator(key) {
			t.Errorf("%q has a validator but the engine says it does not", key)
		}
	}
	for _, key := range []string{
		"swap.VelocityWindowSeconds", "swap.VelocityMaxMints", "swap.cashOut.assetMonthlyCapWei",
		"lending.MaxLTVBps", "lending.LiquidationThresholdBps", "lending.ReserveFactorBps",
		"lending.ProtocolFeeBps", "lending.breaker.MaxTotalSupplyWei", "lending.breaker.MaxTotalBorrowWei",
		"lending.breaker.MaxTotalCollateralWei", "no.such.key", "",
	} {
		if ParamHasValidator(key) {
			t.Errorf("%q has no validator but the engine says it does", key)
		}
	}
}
