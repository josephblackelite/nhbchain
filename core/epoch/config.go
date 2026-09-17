package epoch

import "fmt"

// Config describes how epochs and validator rotation are managed.
type Config struct {
	// Length is the number of blocks that make up a single epoch. The value
	// must be greater than zero.
	Length uint64

	// StakeWeight is the multiplier applied to validator stake when
	// computing the composite weight W_i.
	StakeWeight uint64

	// EngagementWeight is the multiplier applied to validator engagement
	// score when computing the composite weight W_i.
	EngagementWeight uint64

	// RotationEnabled toggles validator set rotation at epoch boundaries.
	RotationEnabled bool

	// MaxValidators specifies how many validators should remain active after
	// rotation when RotationEnabled is true. A zero value means "no limit".
	MaxValidators uint64

	// SnapshotHistory controls how many historical epoch snapshots are
	// retained in state. A zero value means that all snapshots are retained.
	SnapshotHistory uint64
}

// DefaultConfig returns a conservative default configuration.
func DefaultConfig() Config {
	return Config{
		Length:           100,
		StakeWeight:      100,
		EngagementWeight: 1,
		RotationEnabled:  false,
		MaxValidators:    0,
		SnapshotHistory:  64,
	}
}

// Validate ensures the configuration is self-consistent.
func (c Config) Validate() error {
	if c.Length == 0 {
		return fmt.Errorf("epoch length must be greater than zero")
	}
	if c.StakeWeight == 0 && c.EngagementWeight == 0 {
		return fmt.Errorf("at least one weight component must be non-zero")
	}
	if c.RotationEnabled && c.MaxValidators == 0 {
		return fmt.Errorf("max validators must be greater than zero when rotation is enabled")
	}
	return nil
}
