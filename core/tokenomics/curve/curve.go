// Package curve implements the ZNHB Genesis Treasury Distribution Curve: a
// fixed, mechanical schedule pricing ZNHB sold out of the treasury's Sale
// Pool. It has no bearing on ZNHB's secondary-market value -- it only prices
// what the treasury itself charges for its own remaining inventory. See the
// tokenomics design document, section 2, for the full economic rationale
// and public disclosure language.
//
// All arithmetic here is exact (math/big.Rat / math/big.Int) and
// deterministic -- no floating point, no external calls. Every validator
// evaluating the same (c0, c1) pair against DefaultParams() produces the
// bit-identical result.
package curve

import (
	"errors"
	"math/big"
	"sync"
)

// Decimals is ZNHB's on-chain decimal precision (see config/genesis.json's
// token registration), matching how amounts are represented as attoZNHB
// *big.Int values everywhere else in this codebase.
const Decimals = 18

// weiPerWholeToken is 10^18, used to convert between whole-ZNHB and
// attoZNHB (wei) scale.
var weiPerWholeToken = new(big.Int).Exp(big.NewInt(10), big.NewInt(Decimals), nil)

var (
	// ErrInvalidRange indicates a Cost/InvertByBudget call with c1 < c0 or
	// a negative input, which can never happen for a well-formed caller.
	ErrInvalidRange = errors.New("curve: invalid cumulative-sold range")
	// ErrExceedsSalePool indicates the requested range would sell more
	// than the Sale Pool's fixed 800,000,000 ZNHB capacity.
	ErrExceedsSalePool = errors.New("curve: exceeds sale pool capacity")
	// ErrTrancheOutOfRange indicates a tranche index at or beyond TrancheCount.
	ErrTrancheOutOfRange = errors.New("curve: tranche index out of range")
)

// Params defines one instance of the distribution curve.
type Params struct {
	// TrancheSize is the number of whole ZNHB in each tranche (50,000).
	TrancheSize *big.Int
	// TrancheCount is the number of tranches the Sale Pool is divided into
	// (16,000 tranches of 50,000 ZNHB each = 800,000,000 ZNHB Sale Pool).
	TrancheCount uint64
	// P0 is the exact price, in USD-equivalent NHB, of one whole ZNHB in
	// tranche 0 -- $0.05, the price the business was already selling at
	// when this curve was designed.
	P0 *big.Rat
	// Ratio is the exact per-tranche multiplicative step, r. Chosen so
	// that P0 * Ratio^(TrancheCount-1) reaches the curve's terminal price
	// (a 20x multiple, $1.00) at the last tranche.
	//
	// r = 20^(1/16000) is irrational, so it is NOT computed at runtime --
	// that would require a numerically unstable Nth-root routine with no
	// canonical big.Rat result, and different validators' float
	// implementations could disagree in the last bit. Instead it is
	// frozen here as a genesis-immutable exact rational, derived once
	// offline via Newton's method at 300 bits of working precision (see
	// the derivation script referenced below) and truncated to 50
	// significant decimal digits after the leading 1.
	//
	// Verified: at this precision, r^16000 differs from the exact
	// mathematical value of 20 by approximately 1.25e-46, implying a
	// terminal-price drift of about $6.25e-48 -- thirty orders of
	// magnitude below one attoNHB (1e-18), i.e. completely undetectable
	// at any on-chain precision. See core/tokenomics/curve/curve_test.go's
	// TestDefaultRatioConverges for the same check run as part of the
	// normal test suite.
	Ratio *big.Rat

	// prices holds price[i] = P0 * Ratio^i for i in [0, TrancheCount),
	// each ROUNDED to priceRoundingScale precision at every iterative
	// step (price[i] computed from the already-rounded price[i-1], never
	// from an exact Ratio^i exponentiation). This is deliberate: naive
	// exact exponentiation of a ~50-digit-precision rational up to the
	// 16,000th power produces a numerator/denominator with hundreds of
	// thousands of digits (bit-length roughly doubles per squaring step),
	// which is far too slow for a value computed on every purchase.
	// Rounding at each step keeps every table entry bounded to roughly
	// 50-52 significant digits, permanently, while staying fully
	// deterministic -- every validator applies the identical fixed
	// rounding rule (see roundRatToScale) to the identical fixed inputs.
	// Built once by NewParams/DefaultParams, never recomputed per-call.
	prices []*big.Rat
}

// frozenRatioNumerator / frozenRatioDenominator encode r = 20^(1/16000) as
// an exact rational, numerator/10^50. Regenerating this constant (e.g. if
// the terminal multiple or tranche count ever changes) requires an offline
// high-precision derivation -- do not hand-edit these digits.
const (
	frozenRatioNumerator   = "100018725079633928039158138486032778970181349454462"
	frozenRatioDenominator = "100000000000000000000000000000000000000000000000000"
)

// priceRoundingScale bounds every price-table entry to a fixed 50-digit
// decimal precision (see Params.prices), preventing the exact-exponentiation
// blowup described there. It intentionally matches the precision Ratio
// itself was derived at, so rounding a price never loses more precision
// than Ratio already carries.
var priceRoundingScale = new(big.Int).Exp(big.NewInt(10), big.NewInt(50), nil)

// roundRatToScale rounds x to the nearest multiple of 1/scale, exactly,
// using round-half-away-from-zero (round-half-up for the non-negative
// values used throughout this package). Deterministic: no floating point,
// every validator computes the identical result for identical inputs.
func roundRatToScale(x *big.Rat, scale *big.Int) *big.Rat {
	scaledNum := new(big.Int).Mul(x.Num(), scale)
	q, r := new(big.Int).QuoRem(scaledNum, x.Denom(), new(big.Int))
	twiceR := new(big.Int).Lsh(new(big.Int).Abs(r), 1)
	if twiceR.Cmp(x.Denom()) >= 0 {
		if scaledNum.Sign() >= 0 {
			q.Add(q, big.NewInt(1))
		} else {
			q.Sub(q, big.NewInt(1))
		}
	}
	return new(big.Rat).SetFrac(q, new(big.Int).Set(scale))
}

// buildPriceTable computes price[i] = P0 * Ratio^i for i in [0, count)
// iteratively, rounding to priceRoundingScale at every step so no
// intermediate value ever grows unbounded. O(count) bounded-size
// multiplications -- fast (milliseconds for count=16,000), unlike exact
// exponentiation of the full-precision Ratio.
func buildPriceTable(p0, ratio *big.Rat, count uint64) []*big.Rat {
	table := make([]*big.Rat, count)
	if count == 0 {
		return table
	}
	table[0] = roundRatToScale(p0, priceRoundingScale)
	for i := uint64(1); i < count; i++ {
		next := new(big.Rat).Mul(table[i-1], ratio)
		table[i] = roundRatToScale(next, priceRoundingScale)
	}
	return table
}

// NewParams constructs a curve with the given shape, precomputing its
// bounded-precision price table once. Used by DefaultParams and by tests
// exercising non-default shapes.
func NewParams(trancheSize *big.Int, trancheCount uint64, p0, ratio *big.Rat) Params {
	return Params{
		TrancheSize:  trancheSize,
		TrancheCount: trancheCount,
		P0:           p0,
		Ratio:        ratio,
		prices:       buildPriceTable(p0, ratio, trancheCount),
	}
}

// TerminalPrice returns the curve's price at 100% sold -- one step beyond
// the last valid tranche index (TrancheCount-1), i.e. P0 * Ratio^TrancheCount.
// Used for display/reporting once the Sale Pool has fully sold out, since
// TranchePrice(TrancheCount) is intentionally out of range (there is no
// tranche beyond the last one to price a NEW purchase against).
func (p Params) TerminalPrice() *big.Rat {
	if p.TrancheCount == 0 {
		return new(big.Rat).Set(p.P0)
	}
	return roundRatToScale(new(big.Rat).Mul(p.prices[p.TrancheCount-1], p.Ratio), priceRoundingScale)
}

// DefaultParams returns the founder-approved Genesis Treasury Distribution
// Curve parameters: 16,000 tranches of 50,000 ZNHB, $0.05 to $1.00 (20x),
// $1B implied terminal value for the treasury's own Sale Pool inventory.
func DefaultParams() Params {
	ratio, ok := new(big.Rat).SetString(frozenRatioNumerator + "/" + frozenRatioDenominator)
	if !ok {
		panic("curve: invalid frozen ratio literal")
	}
	return NewParams(big.NewInt(50000), 16000, big.NewRat(5, 100), ratio)
}

var (
	defaultOnce  sync.Once
	defaultCache Params
)

// Default returns a cached instance of DefaultParams(). The 16,000-entry
// price table is built at most once per process, regardless of how many
// times Default is called -- callers on a hot path (every ZNHB purchase)
// must use this, not DefaultParams() directly, which rebuilds the table on
// every call. Safe for concurrent use: table entries are read-only after
// construction, never mutated in place by Cost/InvertByBudget/TranchePrice.
func Default() Params {
	defaultOnce.Do(func() {
		defaultCache = DefaultParams()
	})
	return defaultCache
}

// TrancheSizeWei returns the tranche size in attoZNHB.
func (p Params) TrancheSizeWei() *big.Int {
	return new(big.Int).Mul(p.TrancheSize, weiPerWholeToken)
}

// SalePoolCapWei returns the total Sale Pool capacity in attoZNHB
// (TrancheSize * TrancheCount, i.e. 800,000,000 ZNHB).
func (p Params) SalePoolCapWei() *big.Int {
	return new(big.Int).Mul(p.TrancheSizeWei(), new(big.Int).SetUint64(p.TrancheCount))
}

// TrancheIndexFor returns the tranche index that owns the given cumulative
// (attoZNHB) position -- floor(cumulativeWei / TrancheSizeWei).
func (p Params) TrancheIndexFor(cumulativeWei *big.Int) uint64 {
	q := new(big.Int).Div(cumulativeWei, p.TrancheSizeWei())
	return q.Uint64()
}

// TranchePrice returns the exact spot price (USD-equivalent NHB per whole
// ZNHB) of tranche i. This is for display/quoting purposes only -- actual
// purchases must always be priced via Cost or InvertByBudget, never a
// single spot-price lookup, to preserve the order-splitting-proof property
// documented on Cost below.
func (p Params) TranchePrice(index uint64) (*big.Rat, error) {
	if index >= p.TrancheCount {
		return nil, ErrTrancheOutOfRange
	}
	return p.tranchePriceWhole(index), nil
}

// tranchePriceWhole returns P(i) = P0 * Ratio^i via the precomputed,
// bounded-precision table (see Params.prices) -- an O(1) lookup, not a
// runtime exponentiation.
func (p Params) tranchePriceWhole(i uint64) *big.Rat {
	return p.prices[i]
}

// Cost returns the exact cost, in USD-equivalent NHB, to move the
// cumulative-sold counter from c0 to c1 (both attoZNHB / wei amounts),
// blending the price across every tranche boundary the interval [c0, c1)
// crosses.
//
// This is what closes the order-splitting exploit: Cost is exactly
// path-independent -- Cost(a, c) == Cost(a, b) + Cost(b, c) for any
// a <= b <= c -- so slicing one purchase into many transactions never
// changes the total charged. Callers must always use this function (or
// InvertByBudget for the inverse direction), never a single spot-price
// lookup multiplied by quantity.
//
// Bounded to at most TrancheCount (16,000) loop iterations in the worst
// case (c0=0, c1=SalePoolCapWei()) -- cheap for consensus execution.
func (p Params) Cost(c0, c1 *big.Int) (*big.Rat, error) {
	if c0.Sign() < 0 || c1.Cmp(c0) < 0 {
		return nil, ErrInvalidRange
	}
	if c1.Cmp(p.SalePoolCapWei()) > 0 {
		return nil, ErrExceedsSalePool
	}
	if c0.Cmp(c1) == 0 {
		return new(big.Rat), nil
	}

	trancheSizeWei := p.TrancheSizeWei()
	total := new(big.Rat)
	cursor := new(big.Int).Set(c0)
	for cursor.Cmp(c1) < 0 {
		idx := p.TrancheIndexFor(cursor)
		trancheEnd := new(big.Int).Mul(new(big.Int).SetUint64(idx+1), trancheSizeWei)
		segmentEnd := trancheEnd
		if segmentEnd.Cmp(c1) > 0 {
			segmentEnd = c1
		}
		segmentWei := new(big.Int).Sub(segmentEnd, cursor)
		segmentWholeRat := new(big.Rat).SetFrac(segmentWei, weiPerWholeToken)
		price := p.tranchePriceWhole(idx)
		cost := new(big.Rat).Mul(price, segmentWholeRat)
		total.Add(total, cost)
		cursor = segmentEnd
	}
	return total, nil
}

// InvertByBudget finds the maximum attoZNHB amount purchasable starting at
// c0 without the exact cost exceeding maxCost (USD-equivalent NHB),
// walking tranche-by-tranche (bounded by TrancheCount iterations). Returns
// the ZNHB amount granted and its exact cost (always <= maxCost).
//
// Used by the swap-voucher path, which is handed a fiat/NHB budget rather
// than a caller-specified ZNHB amount, unlike the direct-purchase path
// which uses Cost.
func (p Params) InvertByBudget(c0 *big.Int, maxCost *big.Rat) (*big.Int, *big.Rat, error) {
	if c0.Sign() < 0 {
		return nil, nil, ErrInvalidRange
	}
	cap := p.SalePoolCapWei()
	if maxCost.Sign() <= 0 || c0.Cmp(cap) >= 0 {
		return new(big.Int), new(big.Rat), nil
	}

	trancheSizeWei := p.TrancheSizeWei()
	remaining := new(big.Rat).Set(maxCost)
	cursor := new(big.Int).Set(c0)
	spent := new(big.Rat)

	for cursor.Cmp(cap) < 0 && remaining.Sign() > 0 {
		idx := p.TrancheIndexFor(cursor)
		trancheEnd := new(big.Int).Mul(new(big.Int).SetUint64(idx+1), trancheSizeWei)
		if trancheEnd.Cmp(cap) > 0 {
			trancheEnd = cap
		}
		segmentWeiAvailable := new(big.Int).Sub(trancheEnd, cursor)
		price := p.tranchePriceWhole(idx)
		if price.Sign() <= 0 {
			break
		}

		segmentWholeRat := new(big.Rat).SetFrac(segmentWeiAvailable, weiPerWholeToken)
		segmentFullCost := new(big.Rat).Mul(price, segmentWholeRat)

		if segmentFullCost.Cmp(remaining) <= 0 {
			cursor = trancheEnd
			spent.Add(spent, segmentFullCost)
			remaining.Sub(remaining, segmentFullCost)
			continue
		}

		// Can only afford part of this tranche: affordableWhole = remaining / price.
		affordableWholeRat := new(big.Rat).Quo(remaining, price)
		affordableWeiRat := new(big.Rat).Mul(affordableWholeRat, new(big.Rat).SetInt(weiPerWholeToken))
		affordableWei := RoundZNHBDown(affordableWeiRat)
		if affordableWei.Cmp(segmentWeiAvailable) > 0 {
			affordableWei = new(big.Int).Set(segmentWeiAvailable)
		}
		if affordableWei.Sign() > 0 {
			actualCost := new(big.Rat).Mul(price, new(big.Rat).SetFrac(affordableWei, weiPerWholeToken))
			cursor = new(big.Int).Add(cursor, affordableWei)
			spent.Add(spent, actualCost)
		}
		break
	}

	granted := new(big.Int).Sub(cursor, c0)
	return granted, spent, nil
}

// RoundCostUp rounds an exact USD-equivalent-NHB cost up to the nearest
// attoNHB. Always used when CHARGING a buyer, so the protocol is never
// shortchanged by truncation across many small purchases -- the protocol
// rounds in its own favor, consistently, by a documented fixed rule.
func RoundCostUp(cost *big.Rat) *big.Int {
	scaled := new(big.Rat).Mul(cost, new(big.Rat).SetInt(weiPerWholeToken))
	q, r := new(big.Int).QuoRem(scaled.Num(), scaled.Denom(), new(big.Int))
	if r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

// RoundZNHBDown rounds an exact attoZNHB-scaled rational amount down to
// the nearest whole attoZNHB. Used when GRANTING ZNHB to a buyer, so the
// protocol never grants more than was actually paid for.
func RoundZNHBDown(weiAmount *big.Rat) *big.Int {
	return new(big.Int).Quo(weiAmount.Num(), weiAmount.Denom())
}
