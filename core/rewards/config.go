package rewards

import (
	"fmt"
	"math/big"
	"sort"
)

const (
	// SplitDenominator defines the basis point denominator used for reward splits.
	SplitDenominator uint32 = 10_000
)

// Config controls the epoch reward emission behaviour.
type Config struct {
	// Schedule describes the piecewise-constant emission schedule. Each step is
	// active from StartEpoch (inclusive) until the next step. When no steps are
	// defined the emission defaults to zero.
	Schedule []EmissionStep

	// ValidatorSplit is the proportion of the epoch emission allocated to
	// validator rewards, expressed in basis points.
	ValidatorSplit uint32

	// StakerSplit is the proportion of the epoch emission allocated to staker
	// rewards, expressed in basis points.
	StakerSplit uint32

	// EngagementSplit is the proportion of the epoch emission allocated to the
	// engagement reward pool, expressed in basis points.
	EngagementSplit uint32

	// HistoryLength controls how many epoch settlement records are retained in
	// memory and persisted in state. A zero value keeps the full history.
	HistoryLength uint64
}

// EmissionStep defines the emission amount active from StartEpoch onward.
type EmissionStep struct {
	StartEpoch uint64
	Amount     *big.Int
}

// DefaultConfig returns a disabled reward configuration.
func DefaultConfig() Config {
	return Config{
		Schedule:        []EmissionStep{},
		ValidatorSplit:  0,
		StakerSplit:     0,
		EngagementSplit: 0,
		HistoryLength:   64,
	}
}

// Validate ensures the configuration is internally consistent.
func (c Config) Validate() error {
	if err := validateSchedule(c.Schedule); err != nil {
		return err
	}
	total := c.ValidatorSplit + c.StakerSplit + c.EngagementSplit
	if total != 0 && total != SplitDenominator {
		return fmt.Errorf("reward splits must sum to %d basis points", SplitDenominator)
	}
	return nil
}

func validateSchedule(schedule []EmissionStep) error {
	if len(schedule) == 0 {
		return nil
	}
	steps := make([]EmissionStep, len(schedule))
	copy(steps, schedule)
	sort.Slice(steps, func(i, j int) bool {
		return steps[i].StartEpoch < steps[j].StartEpoch
	})
	for i := range steps {
		if steps[i].StartEpoch == 0 {
			return fmt.Errorf("schedule step %d: start epoch must be greater than zero", i)
		}
		if steps[i].Amount == nil {
			return fmt.Errorf("schedule step %d: amount must not be nil", i)
		}
		if steps[i].Amount.Sign() < 0 {
			return fmt.Errorf("schedule step %d: amount must be non-negative", i)
		}
		if i > 0 && steps[i].StartEpoch == steps[i-1].StartEpoch {
			return fmt.Errorf("schedule step %d: duplicate start epoch %d", i, steps[i].StartEpoch)
		}
	}
	return nil
}

// EmissionForEpoch returns the configured emission amount for the supplied
// epoch number (1-indexed). When no schedule entry applies the function returns
// zero.
func (c Config) EmissionForEpoch(epoch uint64) *big.Int {
	if epoch == 0 || len(c.Schedule) == 0 {
		return big.NewInt(0)
	}
	var selected *big.Int
	for i := range c.Schedule {
		step := c.Schedule[i]
		if step.StartEpoch > epoch {
			break
		}
		if selected == nil {
			selected = new(big.Int).Set(step.Amount)
		} else if step.StartEpoch <= epoch {
			selected = new(big.Int).Set(step.Amount)
		}
	}
	if selected == nil {
		return big.NewInt(0)
	}
	return selected
}

// SplitEmission splits the provided emission amount across the configured
// categories. The returned values are copies that can be mutated by the caller.
func (c Config) SplitEmission(total *big.Int) (validators, stakers, engagement *big.Int) {
	validators = big.NewInt(0)
	stakers = big.NewInt(0)
	engagement = big.NewInt(0)
	if total == nil || total.Sign() == 0 {
		return
	}
	denom := big.NewInt(int64(SplitDenominator))
	totalCopy := new(big.Int).Set(total)

	if c.ValidatorSplit > 0 {
		validators = new(big.Int).Mul(totalCopy, big.NewInt(int64(c.ValidatorSplit)))
		validators.Quo(validators, denom)
	}
	if c.StakerSplit > 0 {
		stakers = new(big.Int).Mul(totalCopy, big.NewInt(int64(c.StakerSplit)))
		stakers.Quo(stakers, denom)
	}
	if c.EngagementSplit > 0 {
		engagement = new(big.Int).Mul(totalCopy, big.NewInt(int64(c.EngagementSplit)))
		engagement.Quo(engagement, denom)
	}

	distributed := new(big.Int).Add(validators, stakers)
	distributed.Add(distributed, engagement)
	if distributed.Cmp(totalCopy) < 0 {
		// Assign any rounding dust to the engagement pool to ensure the total
		// emission remains conserved.
		delta := new(big.Int).Sub(totalCopy, distributed)
		engagement.Add(engagement, delta)
	}
	return
}

// IsEnabled returns true when the configuration results in non-zero emissions.
func (c Config) IsEnabled() bool {
	if len(c.Schedule) == 0 {
		return false
	}
	if c.ValidatorSplit == 0 && c.StakerSplit == 0 && c.EngagementSplit == 0 {
		return false
	}
	for i := range c.Schedule {
		if c.Schedule[i].Amount != nil && c.Schedule[i].Amount.Sign() > 0 {
			return true
		}
	}
	return false
}

// Clone creates a deep copy of the configuration.
func (c Config) Clone() Config {
	clone := Config{
		Schedule:        make([]EmissionStep, len(c.Schedule)),
		ValidatorSplit:  c.ValidatorSplit,
		StakerSplit:     c.StakerSplit,
		EngagementSplit: c.EngagementSplit,
		HistoryLength:   c.HistoryLength,
	}
	for i := range c.Schedule {
		amt := big.NewInt(0)
		if c.Schedule[i].Amount != nil {
			amt = new(big.Int).Set(c.Schedule[i].Amount)
		}
		clone.Schedule[i] = EmissionStep{StartEpoch: c.Schedule[i].StartEpoch, Amount: amt}
	}
	return clone
}
