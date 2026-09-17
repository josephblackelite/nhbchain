package state

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/native/loyalty"
)

var oneZNHBWei = new(big.Int).SetUint64(1000000000000000000)

func tokensToWei(tokens int64) *big.Int {
	if tokens == 0 {
		return big.NewInt(0)
	}
	amt := big.NewInt(tokens)
	return amt.Mul(amt, new(big.Int).Set(oneZNHBWei))
}

func TestCalcDailyBudgetZNHB_USDCapWins(t *testing.T) {
	cfg := &loyalty.DynamicConfig{DailyCapPctOf7dFeesBps: 8000, DailyCapUsd: 100}
	price := big.NewRat(2, 1)
	now := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	feesNHB := tokensToWei(1000)
	feesZNHB := tokensToWei(1000)

	budget := CalcDailyBudgetZNHB(now, feesNHB, feesZNHB, price, cfg)
	want := tokensToWei(50)
	if budget.Cmp(want) != 0 {
		t.Fatalf("unexpected budget: got %s want %s", budget.String(), want.String())
	}
}

func TestCalcDailyBudgetZNHB_PercentWins(t *testing.T) {
	cfg := &loyalty.DynamicConfig{DailyCapPctOf7dFeesBps: 1000, DailyCapUsd: 10000}
	price := big.NewRat(3, 2) // 1.5 USD per ZNHB
	now := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)

	feesNHB := tokensToWei(0)
	feesZNHB := tokensToWei(10000)

	budget := CalcDailyBudgetZNHB(now, feesNHB, feesZNHB, price, cfg)
	want := tokensToWei(1000)
	if budget.Cmp(want) != 0 {
		t.Fatalf("unexpected budget: got %s want %s", budget.String(), want.String())
	}
}

func TestCalcDailyBudgetZNHB_EdgeCases(t *testing.T) {
	now := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	baseCfg := &loyalty.DynamicConfig{DailyCapPctOf7dFeesBps: 5000, DailyCapUsd: 200}
	cases := []struct {
		name   string
		cfg    *loyalty.DynamicConfig
		price  *big.Rat
		feesN  *big.Int
		feesZ  *big.Int
		expect *big.Int
	}{
		{
			name:   "nil config",
			cfg:    nil,
			price:  big.NewRat(2, 1),
			expect: big.NewInt(0),
		},
		{
			name:   "nil price",
			cfg:    baseCfg,
			price:  nil,
			expect: big.NewInt(0),
		},
		{
			name:   "zero price",
			cfg:    baseCfg,
			price:  big.NewRat(0, 1),
			expect: big.NewInt(0),
		},
		{
			name:   "percent zero uses usd cap",
			cfg:    &loyalty.DynamicConfig{DailyCapPctOf7dFeesBps: 0, DailyCapUsd: 200},
			price:  big.NewRat(2, 1),
			feesN:  tokensToWei(100),
			feesZ:  tokensToWei(100),
			expect: tokensToWei(100),
		},
		{
			name:   "usd cap zero uses percent",
			cfg:    &loyalty.DynamicConfig{DailyCapPctOf7dFeesBps: 5000, DailyCapUsd: 0},
			price:  big.NewRat(2, 1),
			feesN:  tokensToWei(40),
			feesZ:  tokensToWei(60),
			expect: tokensToWei(40),
		},
		{
			name:   "no constraints",
			cfg:    &loyalty.DynamicConfig{DailyCapPctOf7dFeesBps: 0, DailyCapUsd: 0},
			price:  big.NewRat(2, 1),
			expect: big.NewInt(0),
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			budget := CalcDailyBudgetZNHB(now, tc.feesN, tc.feesZ, tc.price, tc.cfg)
			if tc.expect.Cmp(budget) != 0 {
				t.Fatalf("%s: unexpected budget: got %s want %s", tc.name, budget.String(), tc.expect.String())
			}
		})
	}
}
