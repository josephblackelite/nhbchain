package lending

import "math/big"

// Config captures the runtime configuration for the native lending module.
type Config struct {
	MaxLTVBps               uint64            `toml:"MaxLTVBps"`
	LiquidationThresholdBps uint64            `toml:"LiquidationThresholdBps"`
	ReserveFactorBps        uint64            `toml:"ReserveFactorBps"`
	Breaker                 BreakerThresholds `toml:"breaker"`
	ProtocolFeeBps          uint64            `toml:"ProtocolFeeBps"`
	DeveloperFeeBps         uint64            `toml:"DeveloperFeeBps"`
	DeveloperFeeCollector   string            `toml:"DeveloperFeeCollector"`
}

// BreakerThresholds describes the limit switches for disabling module flows.
type BreakerThresholds struct {
	MaxTotalSupplyWei  *big.Int `toml:"MaxTotalSupplyWei"`
	MaxTotalBorrowWei  *big.Int `toml:"MaxTotalBorrowWei"`
	MaxTotalCollateral *big.Int `toml:"MaxTotalCollateralWei"`
}

// PoolIndexes tracks the cumulative indexes for a lending pool.
type PoolIndexes struct {
	SupplyIndex *big.Int
	BorrowIndex *big.Int
	LastAccrual uint64
}

// AccountPosition stores per-account lending positions.
type AccountPosition struct {
	CollateralWei *big.Int
	DebtPrincipal *big.Int
	SupplyShares  *big.Int
	SupplyIndex   *big.Int
	BorrowIndex   *big.Int
}

// FeeAccrual captures the in-flight protocol and developer fee totals.
type FeeAccrual struct {
	ProtocolFeesWei  *big.Int
	DeveloperFeesWei *big.Int
}

// Clone returns a deep copy of the pool indexes.
func (p *PoolIndexes) Clone() *PoolIndexes {
	if p == nil {
		return nil
	}
	clone := &PoolIndexes{LastAccrual: p.LastAccrual}
	if p.SupplyIndex != nil {
		clone.SupplyIndex = new(big.Int).Set(p.SupplyIndex)
	}
	if p.BorrowIndex != nil {
		clone.BorrowIndex = new(big.Int).Set(p.BorrowIndex)
	}
	return clone
}

// Clone returns a deep copy of the account position.
func (p *AccountPosition) Clone() *AccountPosition {
	if p == nil {
		return nil
	}
	clone := &AccountPosition{}
	if p.CollateralWei != nil {
		clone.CollateralWei = new(big.Int).Set(p.CollateralWei)
	}
	if p.DebtPrincipal != nil {
		clone.DebtPrincipal = new(big.Int).Set(p.DebtPrincipal)
	}
	if p.SupplyShares != nil {
		clone.SupplyShares = new(big.Int).Set(p.SupplyShares)
	}
	if p.SupplyIndex != nil {
		clone.SupplyIndex = new(big.Int).Set(p.SupplyIndex)
	}
	if p.BorrowIndex != nil {
		clone.BorrowIndex = new(big.Int).Set(p.BorrowIndex)
	}
	return clone
}

// Clone returns a deep copy of the fee accrual structure.
func (f *FeeAccrual) Clone() *FeeAccrual {
	if f == nil {
		return nil
	}
	clone := &FeeAccrual{}
	if f.ProtocolFeesWei != nil {
		clone.ProtocolFeesWei = new(big.Int).Set(f.ProtocolFeesWei)
	}
	if f.DeveloperFeesWei != nil {
		clone.DeveloperFeesWei = new(big.Int).Set(f.DeveloperFeesWei)
	}
	return clone
}

// EnsureDefaults populates nil big.Int fields so JSON/RLP handling is safe.
func (c *Config) EnsureDefaults() {
	if c.Breaker.MaxTotalSupplyWei == nil {
		c.Breaker.MaxTotalSupplyWei = big.NewInt(0)
	}
	if c.Breaker.MaxTotalBorrowWei == nil {
		c.Breaker.MaxTotalBorrowWei = big.NewInt(0)
	}
	if c.Breaker.MaxTotalCollateral == nil {
		c.Breaker.MaxTotalCollateral = big.NewInt(0)
	}
}
