package loyalty

import (
	"fmt"
	"math/big"
	"strings"
)

// GlobalConfig controls the behaviour of the loyalty reward engine.
//
// All monetary values are expressed in the smallest denomination of the
// respective token (i.e. wei-style integers).
type GlobalConfig struct {
	Active       bool
	Treasury     []byte
	BaseBps      uint32
	MinSpend     *big.Int
	CapPerTx     *big.Int
	DailyCapUser *big.Int
	Dynamic      DynamicConfig
}

// Clone produces a deep copy of the configuration.
func (c *GlobalConfig) Clone() *GlobalConfig {
	if c == nil {
		return nil
	}
	clone := &GlobalConfig{
		Active:  c.Active,
		BaseBps: c.BaseBps,
		Dynamic: c.Dynamic.Clone(),
	}
	if len(c.Treasury) > 0 {
		clone.Treasury = append([]byte(nil), c.Treasury...)
	}
	if c.MinSpend != nil {
		clone.MinSpend = new(big.Int).Set(c.MinSpend)
	}
	if c.CapPerTx != nil {
		clone.CapPerTx = new(big.Int).Set(c.CapPerTx)
	}
	if c.DailyCapUser != nil {
		clone.DailyCapUser = new(big.Int).Set(c.DailyCapUser)
	}
	return clone
}

// Normalize ensures all pointer fields are non-nil for ease of use. The method
// returns the receiver to allow chaining.
func (c *GlobalConfig) Normalize() *GlobalConfig {
	if c == nil {
		return nil
	}
	c.ApplyDefaults()
	if c.MinSpend == nil {
		c.MinSpend = big.NewInt(0)
	}
	if c.CapPerTx == nil {
		c.CapPerTx = big.NewInt(0)
	}
	if c.DailyCapUser == nil {
		c.DailyCapUser = big.NewInt(0)
	}
	c.Dynamic.Normalize()
	if c.MinSpend.Sign() < 0 {
		c.MinSpend = big.NewInt(0)
	}
	if c.CapPerTx.Sign() < 0 {
		c.CapPerTx = big.NewInt(0)
	}
	if c.DailyCapUser.Sign() < 0 {
		c.DailyCapUser = big.NewInt(0)
	}
	return c
}

// Validate performs static validation of the configuration.
func (c *GlobalConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("nil global config")
	}
	if c.BaseBps > 10_000 {
		return fmt.Errorf("baseBps must not exceed 10_000")
	}
	if len(c.Treasury) == 0 {
		return fmt.Errorf("treasury address must be configured")
	}
	if c.MinSpend != nil && c.MinSpend.Sign() < 0 {
		return fmt.Errorf("minSpend must not be negative")
	}
	if c.CapPerTx != nil && c.CapPerTx.Sign() < 0 {
		return fmt.Errorf("capPerTx must not be negative")
	}
	if c.DailyCapUser != nil && c.DailyCapUser.Sign() < 0 {
		return fmt.Errorf("dailyCapUser must not be negative")
	}
	if err := c.Dynamic.Validate(); err != nil {
		return fmt.Errorf("dynamic: %w", err)
	}
	return nil
}

// DynamicConfig captures the adaptive loyalty controller parameters.
type DynamicConfig struct {
	TargetBps                      uint32
	MinBps                         uint32
	MaxBps                         uint32
	SmoothingStepBps               uint32
	CoverageMaxBps                 uint32
	CoverageLookbackDays           uint32
	DailyCapPctOf7dFeesBps         uint32
	DailyCapUsd                    uint64
	YearlyCapPctOfInitialSupplyBps uint32
	PriceGuard                     PriceGuardConfig
	EnableProRate                  bool
	EnforceProRate                 bool
	EnableProRateSet               bool
	EnforceProRateSet              bool
}

// PriceGuardConfig enforces sanity bounds when consuming reference prices.
type PriceGuardConfig struct {
	Enabled                  bool
	PricePair                string
	TwapWindowSeconds        uint32
	MaxDeviationBps          uint32
	PriceMaxAgeSeconds       uint32
	FallbackMinEmissionZNHB  *big.Int
	UseLastGoodPriceFallback bool
}

// Clone produces a deep copy of the dynamic configuration.
func (d DynamicConfig) Clone() DynamicConfig {
	clone := DynamicConfig{
		TargetBps:                      d.TargetBps,
		MinBps:                         d.MinBps,
		MaxBps:                         d.MaxBps,
		SmoothingStepBps:               d.SmoothingStepBps,
		CoverageMaxBps:                 d.CoverageMaxBps,
		CoverageLookbackDays:           d.CoverageLookbackDays,
		DailyCapPctOf7dFeesBps:         d.DailyCapPctOf7dFeesBps,
		DailyCapUsd:                    d.DailyCapUsd,
		YearlyCapPctOfInitialSupplyBps: d.YearlyCapPctOfInitialSupplyBps,
		PriceGuard:                     d.PriceGuard.Clone(),
		EnableProRate:                  d.EnableProRate,
		EnforceProRate:                 d.EnforceProRate,
		EnableProRateSet:               d.EnableProRateSet,
		EnforceProRateSet:              d.EnforceProRateSet,
	}
	return clone
}

// Normalize ensures all pointer fields are non-nil.
func (d *DynamicConfig) Normalize() *DynamicConfig {
	if d == nil {
		return nil
	}
	d.ApplyDefaults()
	d.PriceGuard.Normalize()
	return d
}

// Normalize ensures the price guard defaults are applied.
func (p *PriceGuardConfig) Normalize() *PriceGuardConfig {
	if p == nil {
		return nil
	}
	p.ApplyDefaults()
	p.PricePair = strings.TrimSpace(p.PricePair)
	if p.FallbackMinEmissionZNHB == nil {
		p.FallbackMinEmissionZNHB = big.NewInt(0)
	}
	if p.FallbackMinEmissionZNHB.Sign() < 0 {
		p.FallbackMinEmissionZNHB = big.NewInt(0)
	}
	return p
}

// Validate ensures the dynamic configuration is self-consistent.
func (d *DynamicConfig) Validate() error {
	if d == nil {
		return nil
	}
	if d.MinBps > d.MaxBps {
		return fmt.Errorf("minBps must be <= maxBps")
	}
	if d.MaxBps > BaseRewardBpsDenominator {
		return fmt.Errorf("maxBps must be <= %d", BaseRewardBpsDenominator)
	}
	if d.TargetBps < d.MinBps || d.TargetBps > d.MaxBps {
		return fmt.Errorf("targetBps must lie within min/max bounds")
	}
	if d.SmoothingStepBps == 0 {
		return fmt.Errorf("smoothingStepBps must be >= 1")
	}
	if d.CoverageMaxBps > BaseRewardBpsDenominator {
		return fmt.Errorf("coverageMaxBps must be <= %d", BaseRewardBpsDenominator)
	}
	if d.CoverageLookbackDays == 0 {
		return fmt.Errorf("coverageLookbackDays must be >= 1")
	}
	if d.DailyCapPctOf7dFeesBps > BaseRewardBpsDenominator {
		return fmt.Errorf("dailyCapPctOf7dFeesBps must be <= %d", BaseRewardBpsDenominator)
	}
	if d.YearlyCapPctOfInitialSupplyBps > BaseRewardBpsDenominator {
		return fmt.Errorf("yearlyCapPctOfInitialSupplyBps must be <= %d", BaseRewardBpsDenominator)
	}
	if err := d.PriceGuard.Validate(); err != nil {
		return fmt.Errorf("priceGuard: %w", err)
	}
	return nil
}

// Validate enforces bounds on the price guard configuration.
func (p *PriceGuardConfig) Validate() error {
	if p == nil {
		return nil
	}
	if strings.TrimSpace(p.PricePair) == "" {
		return fmt.Errorf("pricePair must not be empty")
	}
	if p.TwapWindowSeconds == 0 {
		return fmt.Errorf("twapWindowSeconds must be >= 1")
	}
	if p.PriceMaxAgeSeconds == 0 {
		return fmt.Errorf("priceMaxAgeSeconds must be >= 1")
	}
	if p.MaxDeviationBps > BaseRewardBpsDenominator {
		return fmt.Errorf("maxDeviationBps must be <= %d", BaseRewardBpsDenominator)
	}
	if p.FallbackMinEmissionZNHB != nil && p.FallbackMinEmissionZNHB.Sign() < 0 {
		return fmt.Errorf("fallbackMinEmissionZNHB must be >= 0")
	}
	return nil
}

// Clone returns a deep copy of the price guard configuration.
func (p PriceGuardConfig) Clone() PriceGuardConfig {
	clone := PriceGuardConfig{
		Enabled:                  p.Enabled,
		PricePair:                p.PricePair,
		TwapWindowSeconds:        p.TwapWindowSeconds,
		MaxDeviationBps:          p.MaxDeviationBps,
		PriceMaxAgeSeconds:       p.PriceMaxAgeSeconds,
		UseLastGoodPriceFallback: p.UseLastGoodPriceFallback,
	}
	if p.FallbackMinEmissionZNHB != nil {
		clone.FallbackMinEmissionZNHB = new(big.Int).Set(p.FallbackMinEmissionZNHB)
	}
	return clone
}
