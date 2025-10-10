package loyalty

import "strings"

const (
	// BaseRewardBpsDenominator defines the scaling factor used for basis point math
	// when computing base spend rewards.
	BaseRewardBpsDenominator = 10_000
	// DefaultBaseRewardBps configures the default base accrual rate expressed in
	// basis points (50%).
	DefaultBaseRewardBps = 5_000
	// DefaultDynamicTargetBps is the steady-state band centre for dynamic adjustments.
	DefaultDynamicTargetBps = 50
	// DefaultDynamicMinBps bounds the lowest auto-adjusted reward rate.
	DefaultDynamicMinBps = 25
	// DefaultDynamicMaxBps bounds the highest auto-adjusted reward rate.
	DefaultDynamicMaxBps = 100
	// DefaultDynamicSmoothingStepBps controls how aggressively the controller moves toward the target.
	DefaultDynamicSmoothingStepBps = 5
	// DefaultDynamicCoverageMaxBps caps the coverage ratio considered healthy before reducing rewards (50%).
	DefaultDynamicCoverageMaxBps = 5_000
	// DefaultDynamicCoverageLookbackDays specifies the trailing days of volume considered when evaluating adjustments.
	DefaultDynamicCoverageLookbackDays = 7
	// DefaultDynamicDailyCapPctOf7dFeesBps limits daily issuance to a share of the trailing fee pool (60%).
	DefaultDynamicDailyCapPctOf7dFeesBps = 6_000
	// DefaultDynamicDailyCapUsd caps daily issuance in USD terms.
	DefaultDynamicDailyCapUsd = 5_000
	// DefaultDynamicYearlyCapPctOfInitialSupplyBps caps annual issuance relative to the initial supply (10%).
	DefaultDynamicYearlyCapPctOfInitialSupplyBps = 1_000
	// DefaultDynamicPricePair selects the oracle pair used for coverage calculations.
	DefaultDynamicPricePair = "ZNHB/USD"
	// DefaultDynamicTwapWindowSeconds controls the smoothing window applied to oracle data.
	DefaultDynamicTwapWindowSeconds = 3_600
	// DefaultDynamicPriceMaxAgeSeconds caps the staleness of accepted oracle data.
	DefaultDynamicPriceMaxAgeSeconds = 900
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
	if d.CoverageMaxBps == 0 {
		d.CoverageMaxBps = DefaultDynamicCoverageMaxBps
	}
	if d.CoverageLookbackDays == 0 {
		d.CoverageLookbackDays = DefaultDynamicCoverageLookbackDays
	}
	if d.DailyCapPctOf7dFeesBps == 0 {
		d.DailyCapPctOf7dFeesBps = DefaultDynamicDailyCapPctOf7dFeesBps
	}
	if d.DailyCapUsd == 0 {
		d.DailyCapUsd = DefaultDynamicDailyCapUsd
	}
	if d.YearlyCapPctOfInitialSupplyBps == 0 {
		d.YearlyCapPctOfInitialSupplyBps = DefaultDynamicYearlyCapPctOfInitialSupplyBps
	}
	d.PriceGuard.ApplyDefaults()
	return d
}

// ApplyDefaults ensures unset price guard fields use the conservative baseline.
func (p *PriceGuardConfig) ApplyDefaults() *PriceGuardConfig {
	if p == nil {
		return nil
	}
	if strings.TrimSpace(p.PricePair) == "" {
		p.PricePair = DefaultDynamicPricePair
	}
	if p.TwapWindowSeconds == 0 {
		p.TwapWindowSeconds = DefaultDynamicTwapWindowSeconds
	}
	if p.PriceMaxAgeSeconds == 0 {
		p.PriceMaxAgeSeconds = DefaultDynamicPriceMaxAgeSeconds
	}
	if p.MaxDeviationBps == 0 {
		p.MaxDeviationBps = DefaultDynamicPriceGuardDeviation
	}
	return p
}
