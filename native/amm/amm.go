// Package amm is the arithmetic of a constant-product pool, with nothing else in it: no state, no clock, no
// addresses. Every function is exact big.Int arithmetic (no floats), deterministic, and rounds in the pool's favour:
// what goes out is rounded down, what must come in is rounded up. The chain code in core calls it and does the
// accounting (ledger PL-BC-B).
package amm

import (
	"errors"
	"math/big"
)

// FeeDenominator is the denominator of every basis-point figure.
const FeeDenominator = 10_000

var (
	ErrInvalidAmount     = errors.New("amm: an amount must be positive")
	ErrEmptyPool         = errors.New("amm: the pool has no liquidity")
	ErrInvalidFee        = errors.New("amm: the fee must be below 100 percent")
	ErrNotProportional   = errors.New("amm: the amounts offered cannot match the pool's price")
	ErrZeroShares        = errors.New("amm: the deposit is too small to mint a share")
	ErrSharesAboveSupply = errors.New("amm: more shares than exist")
)

// AmountOut is the amount a swap pays out for amountIn, with the fee taken from the amount in and left in the pool:
//
//	out = floor( amountIn * (10000 - feeBps) * reserveOut / (reserveIn * 10000 + amountIn * (10000 - feeBps)) )
//
// Both reserves and the amount in must be positive. The result is always below reserveOut, so a swap can never empty
// a reserve, and (reserveIn + amountIn) * (reserveOut - out) is never below reserveIn * reserveOut: the product only
// grows.
func AmountOut(reserveIn, reserveOut, amountIn *big.Int, feeBps uint32) (*big.Int, error) {
	if reserveIn == nil || reserveOut == nil || reserveIn.Sign() <= 0 || reserveOut.Sign() <= 0 {
		return nil, ErrEmptyPool
	}
	if amountIn == nil || amountIn.Sign() <= 0 {
		return nil, ErrInvalidAmount
	}
	if feeBps >= FeeDenominator {
		return nil, ErrInvalidFee
	}
	inAfterFee := new(big.Int).Mul(amountIn, big.NewInt(int64(FeeDenominator-feeBps)))
	numerator := new(big.Int).Mul(inAfterFee, reserveOut)
	denominator := new(big.Int).Mul(reserveIn, big.NewInt(FeeDenominator))
	denominator.Add(denominator, inAfterFee)
	return numerator.Div(numerator, denominator), nil // Div on positive operands is floor
}

func ceilDiv(a, b *big.Int) *big.Int {
	q, r := new(big.Int).QuoRem(a, b, new(big.Int))
	if r.Sign() != 0 {
		q.Add(q, big.NewInt(1))
	}
	return q
}

func minInt(a, b *big.Int) *big.Int {
	if a.Cmp(b) <= 0 {
		return new(big.Int).Set(a)
	}
	return new(big.Int).Set(b)
}

// Deposit is what AddLiquidity decides: how much of each asset the pool takes and how many shares it mints.
type Deposit struct {
	Znhb   *big.Int
	Nhb    *big.Int
	Shares *big.Int
}

// AddLiquidity decides a deposit of up to wantZnhb and wantNhb. Into an empty pool it takes both amounts and
// mints floor(sqrt(znhb * nhb)) shares, which sets the starting price. Into a pool with liquidity it takes the pair
// that matches the pool's price (rounded up in the pool's favour, never more than offered) and mints the smaller of
// the two proportional share counts, rounded down. A deposit that mints no share is refused.
func AddLiquidity(reserveZnhb, reserveNhb, totalShares, wantZnhb, wantNhb *big.Int) (*Deposit, error) {
	if wantZnhb == nil || wantNhb == nil || wantZnhb.Sign() <= 0 || wantNhb.Sign() <= 0 {
		return nil, ErrInvalidAmount
	}
	if totalShares.Sign() == 0 {
		// Both amounts are at least 1, so the product is at least 1 and so is its square root: a first deposit always mints.
		shares := new(big.Int).Sqrt(new(big.Int).Mul(wantZnhb, wantNhb))
		return &Deposit{Znhb: new(big.Int).Set(wantZnhb), Nhb: new(big.Int).Set(wantNhb), Shares: shares}, nil
	}
	if reserveZnhb.Sign() <= 0 || reserveNhb.Sign() <= 0 {
		return nil, ErrEmptyPool
	}
	useZnhb := new(big.Int).Set(wantZnhb)
	useNhb := ceilDiv(new(big.Int).Mul(wantZnhb, reserveNhb), reserveZnhb)
	if useNhb.Cmp(wantNhb) > 0 {
		useNhb = new(big.Int).Set(wantNhb)
		useZnhb = ceilDiv(new(big.Int).Mul(wantNhb, reserveZnhb), reserveNhb)
		if useZnhb.Cmp(wantZnhb) > 0 {
			return nil, ErrNotProportional
		}
	}
	byZnhb := new(big.Int).Div(new(big.Int).Mul(useZnhb, totalShares), reserveZnhb)
	byNhb := new(big.Int).Div(new(big.Int).Mul(useNhb, totalShares), reserveNhb)
	shares := minInt(byZnhb, byNhb)
	if shares.Sign() == 0 {
		return nil, ErrZeroShares
	}
	return &Deposit{Znhb: useZnhb, Nhb: useNhb, Shares: shares}, nil
}

// RemoveLiquidity is what burning shares pays out, each asset rounded down.
func RemoveLiquidity(reserveZnhb, reserveNhb, totalShares, shares *big.Int) (znhb, nhb *big.Int, err error) {
	if shares == nil || shares.Sign() <= 0 {
		return nil, nil, ErrInvalidAmount
	}
	if totalShares.Sign() == 0 {
		return nil, nil, ErrEmptyPool
	}
	if shares.Cmp(totalShares) > 0 {
		return nil, nil, ErrSharesAboveSupply
	}
	znhb = new(big.Int).Div(new(big.Int).Mul(reserveZnhb, shares), totalShares)
	nhb = new(big.Int).Div(new(big.Int).Mul(reserveNhb, shares), totalShares)
	return znhb, nhb, nil
}
