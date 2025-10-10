package loyalty

const (
	// BaseRewardBpsDenominator defines the scaling factor used for basis point math
	// when computing base spend rewards.
	BaseRewardBpsDenominator = 10_000
	// DefaultBaseRewardBps configures the default base accrual rate expressed in
	// basis points (50%).
	DefaultBaseRewardBps = 5_000
	// DefaultDynamicTargetBps is the steady-state band centre for dynamic adjustments.
	DefaultDynamicTargetBps = DefaultBaseRewardBps
	// DefaultDynamicMinBps bounds the lowest auto-adjusted reward rate.
	DefaultDynamicMinBps = 3_000
	// DefaultDynamicMaxBps bounds the highest auto-adjusted reward rate.
	DefaultDynamicMaxBps = 7_000
	// DefaultDynamicSmoothingStepBps controls how aggressively the controller moves toward the target.
	DefaultDynamicSmoothingStepBps = 50
	// DefaultDynamicCoverageWindowDays specifies the trailing days of volume considered when evaluating adjustments.
	DefaultDynamicCoverageWindowDays = 7
	// DefaultDynamicPriceGuardDeviation constrains acceptable oracle variance in basis points (5%).
	DefaultDynamicPriceGuardDeviation = 500
)

// ApplyDefaults ensures unset fields fall back to module defaults.
func (c *GlobalConfig) ApplyDefaults() *GlobalConfig {
	if c == nil {
		return nil
	}
	if c.BaseBps == 0 {
		c.BaseBps = DefaultBaseRewardBps
	}
	c.Dynamic.ApplyDefaults()
	return c
}

// ApplyDefaults ensures unset fields fall back to the dynamic controller defaults.
func (d *DynamicConfig) ApplyDefaults() *DynamicConfig {
	if d == nil {
		return nil
	}
	if d.TargetBps == 0 {
		d.TargetBps = DefaultDynamicTargetBps
	}
	if d.MinBps == 0 {
		d.MinBps = DefaultDynamicMinBps
	}
	if d.MaxBps == 0 {
		d.MaxBps = DefaultDynamicMaxBps
	}
	if d.SmoothingStepBps == 0 {
		d.SmoothingStepBps = DefaultDynamicSmoothingStepBps
	}
	if d.CoverageWindowDays == 0 {
		d.CoverageWindowDays = DefaultDynamicCoverageWindowDays
	}
	d.PriceGuard.ApplyDefaults()
	return d
}

// ApplyDefaults ensures unset price guard fields use the conservative baseline.
func (p *PriceGuardConfig) ApplyDefaults() *PriceGuardConfig {
	if p == nil {
		return nil
	}
	if p.MaxDeviationBps == 0 {
		p.MaxDeviationBps = DefaultDynamicPriceGuardDeviation
	}
	return p
}
