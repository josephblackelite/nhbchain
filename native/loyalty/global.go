package loyalty

import (
	"fmt"
	"math/big"
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
	TargetBps          uint32
	MinBps             uint32
	MaxBps             uint32
	SmoothingStepBps   uint32
	CoverageWindowDays uint32
	DailyCap           *big.Int
	YearlyCap          *big.Int
	PriceGuard         PriceGuardConfig
}

// PriceGuardConfig enforces sanity bounds when consuming reference prices.
type PriceGuardConfig struct {
	Enabled         bool
	MaxDeviationBps uint32
}

// Clone produces a deep copy of the dynamic configuration.
func (d DynamicConfig) Clone() DynamicConfig {
	clone := DynamicConfig{
		TargetBps:          d.TargetBps,
		MinBps:             d.MinBps,
		MaxBps:             d.MaxBps,
		SmoothingStepBps:   d.SmoothingStepBps,
		CoverageWindowDays: d.CoverageWindowDays,
		PriceGuard:         d.PriceGuard,
	}
	if d.DailyCap != nil {
		clone.DailyCap = new(big.Int).Set(d.DailyCap)
	}
	if d.YearlyCap != nil {
		clone.YearlyCap = new(big.Int).Set(d.YearlyCap)
	}
	return clone
}

// Normalize ensures all pointer fields are non-nil.
func (d *DynamicConfig) Normalize() *DynamicConfig {
	if d == nil {
		return nil
	}
	d.ApplyDefaults()
	if d.DailyCap == nil {
		d.DailyCap = big.NewInt(0)
	}
	if d.YearlyCap == nil {
		d.YearlyCap = big.NewInt(0)
	}
	if d.DailyCap.Sign() < 0 {
		d.DailyCap = big.NewInt(0)
	}
	if d.YearlyCap.Sign() < 0 {
		d.YearlyCap = big.NewInt(0)
	}
	d.PriceGuard.Normalize()
	return d
}

// Normalize ensures the price guard defaults are applied.
func (p *PriceGuardConfig) Normalize() *PriceGuardConfig {
	if p == nil {
		return nil
	}
	p.ApplyDefaults()
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
	if d.CoverageWindowDays == 0 {
		return fmt.Errorf("coverageWindowDays must be >= 1")
	}
	if d.DailyCap != nil && d.DailyCap.Sign() < 0 {
		return fmt.Errorf("dailyCap must not be negative")
	}
	if d.YearlyCap != nil && d.YearlyCap.Sign() < 0 {
		return fmt.Errorf("yearlyCap must not be negative")
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
	if p.MaxDeviationBps > BaseRewardBpsDenominator {
		return fmt.Errorf("maxDeviationBps must be <= %d", BaseRewardBpsDenominator)
	}
	return nil
}
