package lending

import (
	"math/big"

	"nhbchain/crypto"
)

// Market captures the global accounting state for the lending protocol. Amount
// values are denominated in wei and expressed as big integers to match on-chain
// precision.
type Market struct {
	// PoolID is the unique identifier for the market instance allowing the
	// engine to differentiate state for independently operated pools.
	PoolID string
	// DeveloperOwner identifies the on-chain account that controls pool
	// level configuration and is entitled to the developer fee stream.
	DeveloperOwner crypto.Address
	// DeveloperFeeCollector receives the developer fee portion of accrued
	// interest routed through this market. When unset, developer fees are
	// disabled for the pool.
	DeveloperFeeCollector crypto.Address
	// DeveloperFeeBps captures the developer fee share expressed in basis
	// points. A zero value disables developer fee accruals.
	DeveloperFeeBps uint64
	// TotalNHBSupplied is the aggregate NHB liquidity currently deposited by
	// lenders.
	TotalNHBSupplied *big.Int
	// TotalSupplyShares represents the aggregate LP token supply used to
	// apportion interest to suppliers. Shares are scaled by 1e18 to match
	// the supply index precision.
	TotalSupplyShares *big.Int
	// TotalNHBBorrowed tracks the outstanding NHB borrowed across all
	// accounts.
	TotalNHBBorrowed *big.Int
	// SupplyIndex is the cumulative interest index applied to supplier
	// balances.
	SupplyIndex *big.Int
	// BorrowIndex is the cumulative interest index applied to borrower debt.
	BorrowIndex *big.Int
	// LastUpdateBlock records the block height when indexes were last
	// refreshed.
	LastUpdateBlock uint64
	// ReserveFactor defines the share of interest routed to protocol reserves
	// expressed in basis points for deterministic accounting.
	ReserveFactor uint64
}

// CollateralRouting captures the liquidation collateral distribution between
// the liquidator, developer, and protocol reserve accounts.
type CollateralRouting struct {
	LiquidatorBps   uint64
	DeveloperBps    uint64
	DeveloperTarget crypto.Address
	ProtocolBps     uint64
	ProtocolTarget  crypto.Address
}

// Clone produces a deep copy of the collateral routing configuration to ensure
// callers do not mutate shared address slices.
func (r CollateralRouting) Clone() CollateralRouting {
	clone := CollateralRouting{
		LiquidatorBps: r.LiquidatorBps,
		DeveloperBps:  r.DeveloperBps,
		ProtocolBps:   r.ProtocolBps,
	}
	if bytes := r.DeveloperTarget.Bytes(); len(bytes) != 0 {
		clone.DeveloperTarget = crypto.NewAddress(r.DeveloperTarget.Prefix(), append([]byte(nil), bytes...))
	}
	if bytes := r.ProtocolTarget.Bytes(); len(bytes) != 0 {
		clone.ProtocolTarget = crypto.NewAddress(r.ProtocolTarget.Prefix(), append([]byte(nil), bytes...))
	}
	return clone
}

// UserAccount maintains the lending position for an individual participant.
type UserAccount struct {
	// Address is the unique account identifier within the NHB network.
	Address crypto.Address
	// CollateralZNHB records the ZNHB amount pledged as collateral for
	// borrowing.
	CollateralZNHB *big.Int
	// SupplyShares stores the LP token amount minted when supplying
	// liquidity. Shares are scaled by 1e18 to align with the supply index.
	SupplyShares *big.Int
	// DebtNHB stores the principal NHB borrowed before interest accrual.
	DebtNHB *big.Int
	// ScaledDebt reflects the debt adjusted by the borrow index to capture
	// accrued interest.
	ScaledDebt *big.Int
}

// RiskParameters groups the governance controlled safety limits governing
// lending activity.
type RiskParameters struct {
	// MaxLTV specifies the maximum loan-to-value ratio permitted, expressed in
	// basis points.
	MaxLTV uint64
	// LiquidationThreshold represents the LTV where positions become eligible
	// for liquidation, expressed in basis points.
	LiquidationThreshold uint64
	// LiquidationBonus captures the discount applied to collateral during
	// liquidation, expressed in basis points.
	LiquidationBonus uint64
	// OracleAddress identifies the trusted ZNHB/NHB price feed provider.
	OracleAddress crypto.Address
	// CircuitBreakerActive signals whether new borrowing should be halted due
	// to oracle issues or governance intervention.
	CircuitBreakerActive bool
	// DeveloperFeeCapBps bounds the developer fee that may be charged on
	// `BorrowNHBWithFee` operations. A zero value disables developer fees.
	DeveloperFeeCapBps uint64
}
