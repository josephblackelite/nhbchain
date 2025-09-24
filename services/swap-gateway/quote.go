package main

import (
	"context"
	"fmt"
	"math/big"
	"strings"
)

var weiMultiplier = new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)

// Quoter provides fiat to ZNHB quoting using a configured price source.
type Quoter struct {
	source priceSource
}

type priceSource interface {
	Rate(ctx context.Context, fiat string) (*big.Rat, string, error)
}

type fixedPriceSource struct {
	rate    *big.Rat
	rateStr string
}

// NewQuoter constructs a quoter from the SWAP_PRICE_SOURCE env string.
func NewQuoter(cfg string) (*Quoter, error) {
	parts := strings.SplitN(cfg, ":", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid price source %q", cfg)
	}
	switch strings.ToLower(strings.TrimSpace(parts[0])) {
	case "fixed":
		rateStr := strings.TrimSpace(parts[1])
		rate, err := parseDecimal(rateStr)
		if err != nil {
			return nil, fmt.Errorf("invalid fixed price: %w", err)
		}
		if rate.Cmp(big.NewRat(0, 1)) <= 0 {
			return nil, fmt.Errorf("rate must be positive")
		}
		return &Quoter{source: &fixedPriceSource{rate: rate, rateStr: rateStr}}, nil
	default:
		return nil, fmt.Errorf("unsupported price source %q", parts[0])
	}
}

func (q *Quoter) Quote(ctx context.Context, fiat, amountFiat string) (string, string, error) {
	if !strings.EqualFold(fiat, "USD") {
		return "", "", fmt.Errorf("unsupported fiat %q", fiat)
	}
	amount, err := parseDecimal(amountFiat)
	if err != nil {
		return "", "", fmt.Errorf("invalid amount: %w", err)
	}
	rate, rateStr, err := q.source.Rate(ctx, fiat)
	if err != nil {
		return "", "", err
	}

	minted := new(big.Rat).Quo(amount, rate)
	wei := new(big.Rat).Mul(minted, new(big.Rat).SetInt(weiMultiplier))
	weiStr, err := ratToIntegerString(wei)
	if err != nil {
		return "", "", fmt.Errorf("non integral mint amount: %w", err)
	}
	return rateStr, weiStr, nil
}

func (f *fixedPriceSource) Rate(ctx context.Context, fiat string) (*big.Rat, string, error) {
	if !strings.EqualFold(fiat, "USD") {
		return nil, "", fmt.Errorf("unsupported fiat %q", fiat)
	}
	return new(big.Rat).Set(f.rate), f.rateStr, nil
}

func parseDecimal(v string) (*big.Rat, error) {
	r := new(big.Rat)
	if _, ok := r.SetString(v); !ok {
		return nil, fmt.Errorf("cannot parse %q", v)
	}
	return r, nil
}

func ratToIntegerString(r *big.Rat) (string, error) {
	num := new(big.Int).Set(r.Num())
	den := new(big.Int).Set(r.Denom())
	quotient := new(big.Int).Quo(num, den)
	remainder := new(big.Int).Mod(num, den)
	if remainder.Sign() != 0 {
		return "", fmt.Errorf("remainder %s/%s", remainder.String(), den.String())
	}
	return quotient.String(), nil
}
