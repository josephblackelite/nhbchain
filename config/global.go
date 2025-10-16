package config

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"nhbchain/crypto"
	"nhbchain/native/loyalty"
)

// PaymasterLimits represents the parsed paymaster throttling limits.
type PaymasterLimits struct {
	MerchantDailyCapWei *big.Int
	DeviceDailyTxCap    uint64
	GlobalDailyCapWei   *big.Int
}

// PaymasterAutoTopUpConfig represents the parsed automatic top-up policy values.
type PaymasterAutoTopUpConfig struct {
	Enabled        bool
	Token          string
	MinBalanceWei  *big.Int
	TopUpAmountWei *big.Int
	DailyCapWei    *big.Int
	Cooldown       time.Duration
	Operator       [20]byte
	ApproverRole   string
	MinterRole     string
}

// PaymasterLimits parses the configured paymaster throttling caps into runtime values.
func (g Global) PaymasterLimits() (PaymasterLimits, error) {
	limits := PaymasterLimits{DeviceDailyTxCap: g.Paymaster.DeviceDailyTxCap}
	merchantCap, err := parseUintAmount(g.Paymaster.MerchantDailyCapWei)
	if err != nil {
		return limits, fmt.Errorf("invalid global.paymaster.MerchantDailyCapWei: %w", err)
	}
	limits.MerchantDailyCapWei = merchantCap
	globalCap, err := parseUintAmount(g.Paymaster.GlobalDailyCapWei)
	if err != nil {
		return limits, fmt.Errorf("invalid global.paymaster.GlobalDailyCapWei: %w", err)
	}
	limits.GlobalDailyCapWei = globalCap
	return limits, nil
}

// PaymasterAutoTopUpConfig parses the automatic top-up policy into runtime values.
func (g Global) PaymasterAutoTopUpConfig() (PaymasterAutoTopUpConfig, error) {
	cfg := PaymasterAutoTopUpConfig{Enabled: g.Paymaster.AutoTopUp.Enabled}
	token := strings.ToUpper(strings.TrimSpace(g.Paymaster.AutoTopUp.Token))
	if token == "" {
		token = "ZNHB"
	}
	if token != "ZNHB" {
		return cfg, fmt.Errorf("invalid global.paymaster.AutoTopUp.Token: must be ZNHB, got %q", g.Paymaster.AutoTopUp.Token)
	}
	cfg.Token = token

	minBalance, err := parseUintAmount(g.Paymaster.AutoTopUp.MinBalanceWei)
	if err != nil {
		return cfg, fmt.Errorf("invalid global.paymaster.AutoTopUp.MinBalanceWei: %w", err)
	}
	cfg.MinBalanceWei = minBalance

	topUpAmount, err := parseUintAmount(g.Paymaster.AutoTopUp.TopUpAmountWei)
	if err != nil {
		return cfg, fmt.Errorf("invalid global.paymaster.AutoTopUp.TopUpAmountWei: %w", err)
	}
	cfg.TopUpAmountWei = topUpAmount

	dailyCap, err := parseUintAmount(g.Paymaster.AutoTopUp.DailyCapWei)
	if err != nil {
		return cfg, fmt.Errorf("invalid global.paymaster.AutoTopUp.DailyCapWei: %w", err)
	}
	cfg.DailyCapWei = dailyCap
	if cfg.Enabled && (cfg.DailyCapWei == nil || cfg.DailyCapWei.Sign() <= 0) {
		return cfg, fmt.Errorf("invalid global.paymaster.AutoTopUp.DailyCapWei: must be a positive value when auto-top-up is enabled")
	}

	if g.Paymaster.AutoTopUp.CooldownSeconds > 0 {
		cfg.Cooldown = time.Duration(g.Paymaster.AutoTopUp.CooldownSeconds) * time.Second
	}

	operatorRef := strings.TrimSpace(g.Paymaster.AutoTopUp.Governance.Operator)
	if operatorRef != "" {
		addr, err := crypto.DecodeAddress(operatorRef)
		if err != nil {
			return cfg, fmt.Errorf("invalid global.paymaster.AutoTopUp.Governance.Operator: %w", err)
		}
		copy(cfg.Operator[:], addr.Bytes())
	}

	cfg.ApproverRole = strings.TrimSpace(g.Paymaster.AutoTopUp.Governance.ApproverRole)
	cfg.MinterRole = strings.TrimSpace(g.Paymaster.AutoTopUp.Governance.MinterRole)

	return cfg, nil
}

// TransferNHBPaused reports whether direct NHB transfers are currently disabled via governance.
func (g Global) TransferNHBPaused() bool {
	return g.Pauses.TransferNHB
}

// TransferZNHBPaused reports whether direct ZNHB transfers are currently disabled via governance.
func (g Global) TransferZNHBPaused() bool {
	return g.Pauses.TransferZNHB
}

// LoyaltyDynamicConfig converts the loaded TOML dynamic policy into the runtime representation.
func (g Global) LoyaltyDynamicConfig() loyalty.DynamicConfig {
	dyn := g.Loyalty.Dynamic
	coverageBps := math.Round(dyn.CoverageMax * loyalty.BaseRewardBpsDenominator)
	if coverageBps < 0 {
		coverageBps = 0
	}
	if coverageBps > loyalty.BaseRewardBpsDenominator {
		coverageBps = loyalty.BaseRewardBpsDenominator
	}

	dailyCapBps := math.Round(dyn.DailyCapPctOf7dFees * loyalty.BaseRewardBpsDenominator)
	if dailyCapBps < 0 {
		dailyCapBps = 0
	}
	if dailyCapBps > loyalty.BaseRewardBpsDenominator {
		dailyCapBps = loyalty.BaseRewardBpsDenominator
	}

	yearlyCapBps := math.Round(dyn.YearlyCapPctOfInitialSupply * 100)
	if yearlyCapBps < 0 {
		yearlyCapBps = 0
	}
	if yearlyCapBps > loyalty.BaseRewardBpsDenominator {
		yearlyCapBps = loyalty.BaseRewardBpsDenominator
	}

	dailyCapUsd := math.Round(dyn.DailyCapUSD)
	if dailyCapUsd < 0 {
		dailyCapUsd = 0
	}

	fallbackMin, err := parseUintAmount(dyn.PriceGuard.FallbackMinEmissionZNHBWei)
	if err != nil {
		fallbackMin = big.NewInt(0)
	}

	cfg := loyalty.DynamicConfig{
		TargetBps:                      dyn.TargetBPS,
		MinBps:                         dyn.MinBPS,
		MaxBps:                         dyn.MaxBPS,
		SmoothingStepBps:               dyn.SmoothingStepBPS,
		CoverageMaxBps:                 uint32(coverageBps),
		CoverageLookbackDays:           dyn.CoverageLookbackDays,
		DailyCapPctOf7dFeesBps:         uint32(dailyCapBps),
		DailyCapUsd:                    uint64(dailyCapUsd),
		YearlyCapPctOfInitialSupplyBps: uint32(yearlyCapBps),
		PriceGuard: loyalty.PriceGuardConfig{
			Enabled:                  dyn.PriceGuard.Enabled,
			PricePair:                dyn.PriceGuard.PricePair,
			TwapWindowSeconds:        dyn.PriceGuard.TwapWindowSeconds,
			MaxDeviationBps:          dyn.PriceGuard.MaxDeviationBPS,
			PriceMaxAgeSeconds:       dyn.PriceGuard.PriceMaxAgeSeconds,
			FallbackMinEmissionZNHB:  fallbackMin,
			UseLastGoodPriceFallback: dyn.PriceGuard.UseLastGoodPriceFallback,
		},
		EnableProRate:     dyn.EnableProRate,
		EnforceProRate:    dyn.EnforceProRate,
		EnableProRateSet:  true,
		EnforceProRateSet: true,
	}
	return cfg
}
