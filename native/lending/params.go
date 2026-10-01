package lending

import "math/big"

// BorrowCaps captures the throttles applied to lending markets to limit borrow growth.
// Toml tags let this same type be embedded directly in Config (native/lending/config.go)
// for genesis/config-file wiring, instead of duplicating an equivalent *Config shape.
type BorrowCaps struct {
	// PerBlock limits the amount of NHB that may be borrowed within a single block.
	PerBlock *big.Int `toml:"PerBlockWei"`
	// Total constrains the aggregate outstanding NHB borrow exposure.
	Total *big.Int `toml:"TotalWei"`
	// UtilisationBps bounds the borrow utilisation relative to supplied liquidity.
	UtilisationBps uint64 `toml:"UtilisationBps"`
}

// Clone returns a deep copy of the borrow caps structure.
func (c BorrowCaps) Clone() BorrowCaps {
	clone := BorrowCaps{UtilisationBps: c.UtilisationBps}
	if c.PerBlock != nil {
		clone.PerBlock = new(big.Int).Set(c.PerBlock)
	}
	if c.Total != nil {
		clone.Total = new(big.Int).Set(c.Total)
	}
	return clone
}

// ActionPauses exposes fine-grained switches for pausing individual lending flows.
// Toml tags let this same type be embedded directly in Config (native/lending/config.go)
// for genesis/config-file wiring, instead of duplicating an equivalent *Config shape.
type ActionPauses struct {
	Supply    bool `toml:"Supply"`
	Borrow    bool `toml:"Borrow"`
	Repay     bool `toml:"Repay"`
	Liquidate bool `toml:"Liquidate"`
}

// OracleConfig captures oracle age and deviation tolerances applied when
// validating market data.
type OracleConfig struct {
	MaxAgeBlocks    uint64
	MaxDeviationBps uint64
}
