package loyalty

const (
	// BaseRewardBpsDenominator defines the scaling factor used for basis point math
	// when computing base spend rewards.
	BaseRewardBpsDenominator = 10_000
	// DefaultBaseRewardBps configures the default base accrual rate expressed in
	// basis points (50%).
	DefaultBaseRewardBps = 5_000
)

// ApplyDefaults ensures unset fields fall back to module defaults.
func (c *GlobalConfig) ApplyDefaults() *GlobalConfig {
	if c == nil {
		return nil
	}
	if c.BaseBps == 0 {
		c.BaseBps = DefaultBaseRewardBps
	}
	return c
}
