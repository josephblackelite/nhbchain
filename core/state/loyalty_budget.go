package state

import (
	"math/big"
	"time"

	"nhbchain/native/loyalty"
)

var (
	znhbWeiRat = big.NewRat(1000000000000000000, 1)
)

func CalcDailyBudgetZNHB(now time.Time, rolling7dFeesNHB, rolling7dFeesZNHB *big.Int, price *big.Rat, cfg *loyalty.DynamicConfig) *big.Int {
	_ = now
	if cfg == nil {
		return big.NewInt(0)
	}
	if price == nil || price.Sign() <= 0 {
		return big.NewInt(0)
	}

	capBps := cfg.DailyCapPctOf7dFeesBps
	if capBps > loyalty.BaseRewardBpsDenominator {
		capBps = loyalty.BaseRewardBpsDenominator
	}

	budgets := make([]*big.Int, 0, 2)

	if cfg.DailyCapUsd > 0 {
		usdBudget := new(big.Rat).SetUint64(cfg.DailyCapUsd)
		usdBudget.Quo(usdBudget, price)
		budgets = append(budgets, ratToWei(usdBudget))
	}

	if capBps > 0 {
		feesZNHB := ratFromWei(rolling7dFeesZNHB)
		feesNHB := ratFromWei(rolling7dFeesNHB)
		if feesNHB.Sign() > 0 {
			feesNHB.Quo(feesNHB, price)
		}
		totalFees := new(big.Rat).Add(feesZNHB, feesNHB)
		if totalFees.Sign() > 0 {
			pct := new(big.Rat).SetUint64(uint64(capBps))
			pct.Quo(pct, big.NewRat(loyalty.BaseRewardBpsDenominator, 1))
			totalFees.Mul(totalFees, pct)
		}
		budgets = append(budgets, ratToWei(totalFees))
	}

	if len(budgets) == 0 {
		return big.NewInt(0)
	}

	min := budgets[0]
	for _, candidate := range budgets[1:] {
		if candidate.Cmp(min) < 0 {
			min = candidate
		}
	}

	if min.Sign() < 0 {
		return big.NewInt(0)
	}

	return new(big.Int).Set(min)
}

func ratFromWei(amount *big.Int) *big.Rat {
	if amount == nil || amount.Sign() <= 0 {
		return new(big.Rat)
	}
	r := new(big.Rat).SetInt(amount)
	return r.Quo(r, znhbWeiRat)
}

func ratToWei(value *big.Rat) *big.Int {
	if value == nil || value.Sign() <= 0 {
		return big.NewInt(0)
	}
	scaled := new(big.Rat).Mul(value, znhbWeiRat)
	num := new(big.Int).Set(scaled.Num())
	den := scaled.Denom()
	if den.Sign() == 0 {
		return big.NewInt(0)
	}
	num.Quo(num, den)
	if num.Sign() < 0 {
		return big.NewInt(0)
	}
	return num
}
