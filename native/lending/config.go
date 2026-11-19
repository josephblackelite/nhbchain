package lending

import (
	"fmt"
	"math/big"
)

// Config captures the runtime configuration for the native lending module.
type Config struct {
	MaxLTVBps               uint64                  `toml:"MaxLTVBps"`
	LiquidationThresholdBps uint64                  `toml:"LiquidationThresholdBps"`
	ReserveFactorBps        uint64                  `toml:"ReserveFactorBps"`
	Breaker                 BreakerThresholds       `toml:"breaker"`
	Risk                    RiskConfig              `toml:"risk"`
	ProtocolFeeBps          uint64                  `toml:"ProtocolFeeBps"`
	DeveloperFeeBps         uint64                  `toml:"DeveloperFeeBps"`
	DeveloperFeeCollector   string                  `toml:"DeveloperFeeCollector"`
	CollateralRouting       CollateralRoutingConfig `toml:"collateralRouting"`
	// OracleMaxAgeBlocks bounds how many blocks may elapse since a market's
	// last ref-price update (Market.OracleUpdatedBlock) before guardOracle
	// treats the quote as stale and refuses to Borrow/WithdrawCollateral/
	// Liquidate against it. Zero disables the staleness check.
	OracleMaxAgeBlocks uint64 `toml:"OracleMaxAgeBlocks"`
	// OracleMaxDeviationBps bounds how far a new ref-price submission may
	// move relative to the market's previous one (Market.OraclePrevMedianWei)
	// before guardOracle refuses to lend against it. Zero disables the
	// deviation check.
	OracleMaxDeviationBps uint64 `toml:"OracleMaxDeviationBps"`
}

// CollateralRoutingConfig describes the default collateral distribution applied
// during liquidations when a pool has not specified overrides.
type CollateralRoutingConfig struct {
	LiquidatorBps    uint64 `toml:"LiquidatorBps"`
	DeveloperBps     uint64 `toml:"DeveloperBps"`
	DeveloperAddress string `toml:"DeveloperAddress"`
	ProtocolBps      uint64 `toml:"ProtocolBps"`
	ProtocolAddress  string `toml:"ProtocolAddress"`
}

// BreakerThresholds describes the limit switches for disabling module flows.
type BreakerThresholds struct {
	MaxTotalSupplyWei  *big.Int `toml:"MaxTotalSupplyWei"`
	MaxTotalBorrowWei  *big.Int `toml:"MaxTotalBorrowWei"`
	MaxTotalCollateral *big.Int `toml:"MaxTotalCollateralWei"`
}

// RiskConfig names the risk limits of [lending.risk]. All of them are off at their
// zero value, and none is applied by the engine until core's
// LendingRiskActivationHeight (ledger PL-DC-19). The sizes are owner decisions.
type RiskConfig struct {
	// LiquidationBonusBps is what a liquidator receives above the debt it repays, in
	// basis points of that debt, paid in collateral. It must not exceed what the
	// liquidation threshold leaves: at a threshold of T basis points a position at
	// the threshold holds 10000/T times its debt in collateral, so a larger bonus makes
	// every liquidation at the threshold leave bad debt (see RiskParameters).
	LiquidationBonusBps uint64 `toml:"LiquidationBonusBps"`
	// BorrowCapPerBlockWei bounds the NHB borrowed in one block, fees included.
	BorrowCapPerBlockWei *big.Int `toml:"BorrowCapPerBlockWei"`
	// BorrowCapUtilisationBps bounds borrowed over supplied, in basis points, after a borrow.
	BorrowCapUtilisationBps uint64 `toml:"BorrowCapUtilisationBps"`
	// PauseSupply, PauseBorrow, PauseRepay and PauseLiquidate halt that action alone.
	PauseSupply    bool `toml:"PauseSupply"`
	PauseBorrow    bool `toml:"PauseBorrow"`
	PauseRepay     bool `toml:"PauseRepay"`
	PauseLiquidate bool `toml:"PauseLiquidate"`
	// CircuitBreakerActive halts all new borrowing, flexible and fixed-term.
	CircuitBreakerActive bool `toml:"CircuitBreakerActive"`
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
	if c.Risk.BorrowCapPerBlockWei == nil {
		c.Risk.BorrowCapPerBlockWei = big.NewInt(0)
	}
}

// RiskParameters is the engine's risk configuration as the node's [lending]
// section names it. Both entry points call it, so they cannot disagree: the
// split-process daemon used to build its own copy and dropped the fields the engine
// never received (ledger PL-DC-19). The fields that core applies only from
// LendingRiskActivationHeight are filled here whatever the height; core clears them
// before then. It refuses a liquidation bonus larger than the threshold leaves room
// for, and any limit that is nonsense.
func (c Config) RiskParameters() (RiskParameters, error) {
	params := RiskParameters{
		MaxLTV:               c.MaxLTVBps,
		LiquidationThreshold: c.LiquidationThresholdBps,
		DeveloperFeeCapBps:   c.DeveloperFeeBps,
		Oracle: OracleConfig{
			MaxAgeBlocks:    c.OracleMaxAgeBlocks,
			MaxDeviationBps: c.OracleMaxDeviationBps,
		},
		LiquidationBonus:     c.Risk.LiquidationBonusBps,
		CircuitBreakerActive: c.Risk.CircuitBreakerActive,
		Pauses: ActionPauses{
			Supply:    c.Risk.PauseSupply,
			Borrow:    c.Risk.PauseBorrow,
			Repay:     c.Risk.PauseRepay,
			Liquidate: c.Risk.PauseLiquidate,
		},
		BorrowCaps: BorrowCaps{UtilisationBps: c.Risk.BorrowCapUtilisationBps},
	}
	for name, v := range map[string]*big.Int{
		"BorrowCapPerBlockWei":  c.Risk.BorrowCapPerBlockWei,
		"MaxTotalSupplyWei":     c.Breaker.MaxTotalSupplyWei,
		"MaxTotalBorrowWei":     c.Breaker.MaxTotalBorrowWei,
		"MaxTotalCollateralWei": c.Breaker.MaxTotalCollateral,
	} {
		if v != nil && v.Sign() < 0 {
			return RiskParameters{}, fmt.Errorf("lending: %s must not be negative", name)
		}
	}
	if c.Risk.BorrowCapUtilisationBps > 10_000 {
		return RiskParameters{}, fmt.Errorf("lending: BorrowCapUtilisationBps %d is above 10000", c.Risk.BorrowCapUtilisationBps)
	}
	if c.Risk.LiquidationBonusBps > 0 && c.LiquidationThresholdBps > 0 {
		// A position sitting exactly at the threshold holds 10000/T of its debt in
		// collateral (T in basis points); seizing debt*(10000+bonus)/10000 of it takes
		// more than there is when the bonus is above 10000*10000/T - 10000.
		room := 10_000*10_000/c.LiquidationThresholdBps - 10_000
		if c.LiquidationThresholdBps >= 10_000 || c.Risk.LiquidationBonusBps > room {
			return RiskParameters{}, fmt.Errorf("lending: LiquidationBonusBps %d leaves bad debt on every liquidation at a threshold of %d (the most it can be is %d)", c.Risk.LiquidationBonusBps, c.LiquidationThresholdBps, room)
		}
	}
	if v := c.Risk.BorrowCapPerBlockWei; v != nil && v.Sign() > 0 {
		params.BorrowCaps.PerBlock = new(big.Int).Set(v)
	}
	if v := c.Breaker.MaxTotalBorrowWei; v != nil && v.Sign() > 0 {
		params.BorrowCaps.Total = new(big.Int).Set(v)
	}
	if v := c.Breaker.MaxTotalSupplyWei; v != nil && v.Sign() > 0 {
		params.MaxTotalSupply = new(big.Int).Set(v)
	}
	if v := c.Breaker.MaxTotalCollateral; v != nil && v.Sign() > 0 {
		params.MaxTotalCollateral = new(big.Int).Set(v)
	}
	return params, nil
}
