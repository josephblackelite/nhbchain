package lending

import (
	"math/big"

	"nhbchain/crypto"
)

// Market captures the global accounting state for the lending protocol. Amount
// values are denominated in wei and expressed as big integers to match on-chain
// precision.
type Market struct {
	// TotalNHBSupplied is the aggregate NHB liquidity currently deposited by
	// lenders.
	TotalNHBSupplied *big.Int
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

// UserAccount maintains the lending position for an individual participant.
type UserAccount struct {
	// Address is the unique account identifier within the NHB network.
	Address crypto.Address
	// CollateralZNHB records the ZNHB amount pledged as collateral for
	// borrowing.
	CollateralZNHB *big.Int
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
}
