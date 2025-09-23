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
	Active       bool     `rlp:"1"`
	Treasury     []byte   `rlp:"2"`
	BaseBps      uint32   `rlp:"3"`
	MinSpend     *big.Int `rlp:"4"`
	CapPerTx     *big.Int `rlp:"5"`
	DailyCapUser *big.Int `rlp:"6"`
}

// Clone produces a deep copy of the configuration.
func (c *GlobalConfig) Clone() *GlobalConfig {
	if c == nil {
		return nil
	}
	clone := &GlobalConfig{
		Active:  c.Active,
		BaseBps: c.BaseBps,
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
	if c.MinSpend == nil {
		c.MinSpend = big.NewInt(0)
	}
	if c.CapPerTx == nil {
		c.CapPerTx = big.NewInt(0)
	}
	if c.DailyCapUser == nil {
		c.DailyCapUser = big.NewInt(0)
	}
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
	return nil
}
