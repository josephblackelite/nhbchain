package lending

import (
	"math/big"
	"strings"
	"testing"

	"nhbchain/crypto"
	gatewayengine "nhbchain/services/lending/engine"
)

// weiToDecimalStringForTest mirrors rpc/lending_handlers.go's
// weiToDecimalString closely enough for test-fixture purposes: an exact,
// lossless decimal expansion of a wei amount (18 decimals), trimmed of
// trailing zeros. It exists only so this test can build the same
// AccountSnapshot.CollateralValueUsd/BorrowedValueUsd wire shape the real
// RPC layer sends, without importing the rpc package (which would be a
// layering inversion for a native/lending test).
func weiToDecimalStringForTest(amount *big.Int) string {
	if amount == nil || amount.Sign() == 0 {
		return "0"
	}
	decimal := new(big.Rat).SetFrac(amount, weiPerToken).FloatString(18)
	decimal = strings.TrimRight(strings.TrimRight(decimal, "0"), ".")
	if decimal == "" {
		return "0"
	}
	return decimal
}

// TestGatewayHealthFactorMatchesChainAuthoritativeMath is the regression
// test for the NHB-AUDIT-S2 follow-up: services/lending's gateway
// (services/lending/engine.ComputeHealthFactor, called from both
// NodeAdapter.GetHealth and services/lending/server's toProtoPosition)
// used to recompute a position's health factor from RAW fields --
// CollateralZNHBWei with no oracle adjustment, and a sum over only the
// flexible-rate Borrowed[] entries excluding any active fixed-term loan --
// silently diverging from this engine's own authoritative
// OracleAdjustedCollateralValue/combinedDebtWei math (used by
// positionHealthy/withinMaxLTV, the checks the chain actually enforces
// borrows against) the moment the oracle price was not exactly 1:1 or a
// fixed-term loan was active.
//
// For each synthetic position below, this proves two things against the
// SAME underlying engine state:
//  1. The gateway's ComputeHealthFactor, fed the wire-shaped
//     CollateralValueUsd/BorrowedValueUsd figures a real RPC response would
//     carry, yields EXACTLY the same collateral/debt ratio as computing it
//     directly from this engine's own OracleAdjustedCollateralValue and
//     combinedDebtWei outputs.
//  2. Checking that identical ratio against the SAME LiquidationThreshold/
//     MaxLTV basis-point thresholds this engine's own positionHealthy/
//     withinMaxLTV enforce produces the identical pass/fail verdict those
//     unexported methods actually return -- i.e. the gateway's displayed
//     health factor would never mislead an operator or borrower about
//     whether the chain considers this exact position healthy.
func TestGatewayHealthFactorMatchesChainAuthoritativeMath(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x40)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x41)
	borrower := makeAddress(crypto.NHBPrefix, 0x42)

	params := RiskParameters{
		LiquidationThreshold: 8000, // 80%
		MaxLTV:               7000, // 70%
	}

	oneToken := new(big.Int).Set(weiPerToken)
	scale := func(whole int64) *big.Int {
		return new(big.Int).Mul(big.NewInt(whole), oneToken)
	}
	halfToken := new(big.Int).Quo(oneToken, big.NewInt(2)) // 0.5 NHB per ZNHB, in wei

	cases := []struct {
		name            string
		oracleMedianWei *big.Int // nil => no price ever submitted
		collateralZNHB  *big.Int
		flexibleDebt    *big.Int
		fixedTermPrinc  *big.Int // nil => no active fixed-term loan
		fixedTermInt    *big.Int
		fixedTermRepaid *big.Int
	}{
		{
			// Oracle prices ZNHB at 0.5 NHB -- well below 1:1 parity. No
			// fixed-term loan. The old gateway math (raw CollateralZNHBWei,
			// no oracle adjustment) would have reported this position as
			// TWICE as healthy as it actually is.
			name:            "oracle_price_below_parity_only",
			oracleMedianWei: halfToken,
			collateralZNHB:  scale(1000),
			flexibleDebt:    scale(300),
		},
		{
			// No oracle price ever submitted (falls back to strict 1:1 --
			// see OracleAdjustedCollateralValue). An active fixed-term loan
			// the old gateway math never summed into total debt.
			name:            "fixed_term_loan_only",
			oracleMedianWei: nil,
			collateralZNHB:  scale(1000),
			flexibleDebt:    scale(100),
			fixedTermPrinc:  scale(200),
			fixedTermInt:    scale(50),
			fixedTermRepaid: big.NewInt(0),
		},
		{
			// Both gaps at once: sub-parity oracle price AND an active
			// fixed-term loan.
			name:            "oracle_below_parity_and_fixed_term_loan",
			oracleMedianWei: halfToken,
			collateralZNHB:  scale(1000),
			flexibleDebt:    scale(100),
			fixedTermPrinc:  scale(150),
			fixedTermInt:    scale(50),
			fixedTermRepaid: big.NewInt(0),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := NewEngine(moduleAddr, collateralAddr, params)
			engine.SetPoolID("default")

			state := newMockEngineState()
			market := &Market{
				PoolID:           "default",
				TotalNHBSupplied: big.NewInt(0),
				TotalNHBBorrowed: big.NewInt(0),
				SupplyIndex:      new(big.Int).Set(ray),
				BorrowIndex:      new(big.Int).Set(ray),
			}
			if tc.oracleMedianWei != nil {
				market.OracleMedianWei = new(big.Int).Set(tc.oracleMedianWei)
			}
			state.market = market

			if tc.fixedTermPrinc != nil {
				loan := &FixedTermLoan{
					Borrower:         borrower,
					PoolID:           "default",
					TenureDays:       30,
					RateBps:          1200,
					PrincipalWei:     new(big.Int).Set(tc.fixedTermPrinc),
					TotalInterestWei: new(big.Int).Set(tc.fixedTermInt),
					RepaidWei:        new(big.Int).Set(tc.fixedTermRepaid),
					Status:           FixedTermLoanStatusActive,
				}
				loan.LoanID[0] = 0x01
				if err := state.PutFixedTermLoan(loan); err != nil {
					t.Fatalf("put fixed term loan: %v", err)
				}
				if err := state.SetActiveFixedTermLoanID("default", borrower, loan.LoanID); err != nil {
					t.Fatalf("set active fixed term loan: %v", err)
				}
			}

			engine.SetState(state)

			// --- The chain's own authoritative math ---
			combinedDebt, err := engine.combinedDebtWei(borrower, tc.flexibleDebt)
			if err != nil {
				t.Fatalf("combinedDebtWei: %v", err)
			}
			chainCollateralValue := OracleAdjustedCollateralValue(market, tc.collateralZNHB)
			chainHealthy := engine.positionHealthy(market, tc.collateralZNHB, combinedDebt)
			chainWithinLTV := engine.withinMaxLTV(market, tc.collateralZNHB, combinedDebt)

			if combinedDebt.Sign() <= 0 {
				t.Fatalf("test fixture must produce positive combined debt, got %s", combinedDebt)
			}

			// --- The gateway's math, fed the wire shape a real
			// lending_getUserAccount response carries (AccountSnapshot's
			// CollateralValueUsd/BorrowedValueUsd, mirroring
			// rpc/lending_handlers.go's collateralValueUsd/
			// newLendingAccountResult) ---
			collateralValueUsd := ""
			if market.OracleMedianWei != nil && market.OracleMedianWei.Sign() > 0 {
				collateralValueUsd = weiToDecimalStringForTest(chainCollateralValue)
			}
			borrowedValueUsd := weiToDecimalStringForTest(combinedDebt)

			gatewayHF := gatewayengine.ComputeHealthFactor(
				tc.collateralZNHB.String(),
				collateralValueUsd,
				borrowedValueUsd,
			)

			gatewayRat, ok := new(big.Rat).SetString(gatewayHF)
			if !ok {
				t.Fatalf("gateway health factor %q did not parse as a rational number", gatewayHF)
			}

			// 1. Exact string parity against the SAME collateral/debt ratio
			// computed directly from the chain's own wei figures, formatted
			// with the identical FloatString(18)-then-trim rule
			// ComputeHealthFactor itself uses. This is the ratio's exact
			// value, not a re-parsed approximation of gatewayHF, so any
			// mismatch here is a real divergence, not rounding noise from
			// gatewayHF's own necessarily-finite decimal representation
			// (5/3, for example, has no exact terminating decimal).
			chainRat := new(big.Rat).SetFrac(chainCollateralValue, combinedDebt)
			expectedHF := chainRat.FloatString(18)
			expectedHF = strings.TrimRight(strings.TrimRight(expectedHF, "0"), ".")
			if expectedHF == "" {
				expectedHF = "0"
			}
			if gatewayHF != expectedHF {
				t.Fatalf("gateway health factor %q does not match chain-derived health factor %q (collateral %s / debt %s)",
					gatewayHF, expectedHF, chainCollateralValue, combinedDebt)
			}

			// 2. Threshold verdicts computed from the gateway's ratio must
			// match the chain's own positionHealthy/withinMaxLTV verdicts.
			liqThreshold := big.NewRat(int64(params.LiquidationThreshold), 10_000)
			maxLTVThreshold := big.NewRat(int64(params.MaxLTV), 10_000)
			gatewayHealthy := gatewayRat.Cmp(liqThreshold) >= 0
			gatewayWithinLTV := gatewayRat.Cmp(maxLTVThreshold) >= 0

			if gatewayHealthy != chainHealthy {
				t.Fatalf("gateway-derived healthy verdict (%v) diverges from chain positionHealthy (%v) for ratio %s vs threshold %s",
					gatewayHealthy, chainHealthy, gatewayRat.FloatString(6), liqThreshold.FloatString(6))
			}
			if gatewayWithinLTV != chainWithinLTV {
				t.Fatalf("gateway-derived withinMaxLTV verdict (%v) diverges from chain withinMaxLTV (%v) for ratio %s vs threshold %s",
					gatewayWithinLTV, chainWithinLTV, gatewayRat.FloatString(6), maxLTVThreshold.FloatString(6))
			}
		})
	}
}
