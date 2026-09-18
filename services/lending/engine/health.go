package engine

import (
	"math/big"
	"strings"
)

// weiPerToken is the 1e18 scale shared by every wei-denominated amount this
// package handles (ZNHB collateral and NHB debt both use 18 decimals),
// mirroring native/lending's own weiPerToken and rpc/lending_handlers.go's
// weiPerWholeUnit.
var weiPerToken = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

// ComputeHealthFactor derives the collateral/debt ratio surfaced to clients
// as a position's "health factor" -- the single implementation both
// NodeAdapter.GetHealth (this file's package) and
// services/lending/server/server.go's toProtoPosition call, closing the
// duplication that previously let the two drift apart (NHB-AUDIT-S2
// follow-up).
//
// Before this fix, both call sites independently recomputed the ratio from
// RAW fields instead: CollateralZNHBWei (untouched by any oracle price) over
// a sum of only the flexible-rate Borrowed[] entries (excluding any active
// fixed-term loan's outstanding balance). That is exactly the pair of gaps
// native/lending/engine.go's OracleAdjustedCollateralValue and
// combinedDebtWei exist to close for the chain's own authoritative
// positionHealthy/withinMaxLTV checks (see their doc comments there): a
// market whose oracle price is not exactly 1:1, or a borrower with an
// active fixed-term loan, made the gateway report a healthier position than
// the chain would ever actually allow.
//
// collateralValueUsd and borrowedValueUsd are AccountSnapshot's fields of
// the same name (rpc/lending_handlers.go's collateralValueUsd helper and
// newLendingAccountResult on the wire side): decimal strings, not wei, but
// already computed with exactly this oracle adjustment and fixed-term
// inclusion -- collateralValueUsd runs the ZNHB collateral through the same
// OracleAdjustedCollateralValue conversion the engine enforces borrows
// against, and borrowedValueUsd already folds in any active fixed-term
// loan's OutstandingWei() alongside the flexible-rate debt. Their ratio
// equals the wei-level ratio the chain enforces: both were produced by
// dividing a wei figure by the same 1e18 scale (weiToDecimalString), an
// exact, lossless decimal expansion, so that shared factor cancels out of
// the ratio unchanged.
//
// collateralZnhbWei (AccountSnapshot.CollateralZNHBWei, raw ZNHB wei) is
// used only when collateralValueUsd is "" -- meaning the market has never
// received an oracle price -- matching OracleAdjustedCollateralValue's own
// strict 1:1 fallback for that exact case, rather than silently treating a
// missing-price position's collateral as zero (which would misreport it as
// maximally unhealthy instead of falling back the way the chain does). It
// is scaled down from wei to the same whole-token decimal terms as
// borrowedValueUsd (dividing by weiPerToken) before use -- collateralValueUsd
// and borrowedValueUsd are always whole-token decimal strings, never wei, so
// comparing raw wei against one of them directly would be off by a factor
// of 1e18.
func ComputeHealthFactor(collateralZnhbWei, collateralValueUsd, borrowedValueUsd string) string {
	collateral, ok := new(big.Rat).SetString(strings.TrimSpace(collateralValueUsd))
	if !ok {
		collateral = weiToDecimal(collateralZnhbWei)
	}
	debt, ok := new(big.Rat).SetString(strings.TrimSpace(borrowedValueUsd))
	if !ok {
		debt = new(big.Rat)
	}
	if debt.Sign() <= 0 {
		return "0"
	}
	if collateral.Sign() < 0 {
		return "0"
	}
	decimal := new(big.Rat).Quo(collateral, debt).FloatString(18)
	decimal = strings.TrimRight(strings.TrimRight(decimal, "0"), ".")
	if decimal == "" {
		return "0"
	}
	return decimal
}

// weiToDecimal parses a wei-denominated integer string and expresses it in
// whole-token decimal terms (dividing by weiPerToken), returning zero for an
// empty or unparseable value. Used only by ComputeHealthFactor's
// no-oracle-price fallback -- see its doc comment.
func weiToDecimal(wei string) *big.Rat {
	value, ok := new(big.Int).SetString(strings.TrimSpace(wei), 10)
	if !ok {
		return new(big.Rat)
	}
	return new(big.Rat).SetFrac(value, weiPerToken)
}
