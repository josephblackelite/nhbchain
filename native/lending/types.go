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
	// apportion interest to suppliers. Shares are scaled by 1e27 to match
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
	// BorrowedThisBlock tracks the borrow volume issued in the current block
	// for enforcing per-block caps.
	BorrowedThisBlock *big.Int
	// LastBorrowBlock records the block height when the per-block borrow
	// counter was last reset.
	LastBorrowBlock uint64
	// OracleMedianWei stores the latest oracle median quote for the market.
	OracleMedianWei *big.Int
	// OraclePrevMedianWei tracks the previous oracle median quote enabling
	// deviation checks between sequential updates.
	OraclePrevMedianWei *big.Int
	// OracleUpdatedBlock captures the block height when the median quote was
	// last refreshed.
	OracleUpdatedBlock uint64
	// DepositApyBps exposes the current supplier-side APY, expressed in basis
	// points, derived from the real utilisation-based interest curve (see
	// InterestModel.SupplyAPY). It is computed on demand by the RPC layer for
	// client consumption and is intentionally excluded from the persisted
	// on-chain market snapshot (see storedLendingMarket in
	// core/state/manager.go), so it always reflects the current utilisation
	// rather than a stale value.
	DepositApyBps uint64 `json:"depositApyBps"`
	// BorrowApyBps exposes the current borrower-side APR, expressed in basis
	// points, derived from the real utilisation-based interest curve (see
	// InterestModel.BorrowAPR). Like DepositApyBps it is computed on demand
	// and is not persisted.
	BorrowApyBps uint64 `json:"borrowApyBps"`
	// AvailableLiquidityWei is TotalNHBSupplied minus TotalNHBBorrowed
	// (floored at zero, see Engine.AvailableLiquidity), the actual amount
	// left to borrow or withdraw right now. Like DepositApyBps/BorrowApyBps
	// it is computed on demand by the RPC layer and not persisted -- without
	// it, clients had no way to tell net-available liquidity from gross
	// total supplied (see nhbportal's lendingStore.ts computeUtilization,
	// which used to double-count borrowed funds as if still on hand).
	AvailableLiquidityWei string `json:"availableLiquidityWei"`
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
		clone.DeveloperTarget = crypto.MustNewAddress(r.DeveloperTarget.Prefix(), append([]byte(nil), bytes...))
	}
	if bytes := r.ProtocolTarget.Bytes(); len(bytes) != 0 {
		clone.ProtocolTarget = crypto.MustNewAddress(r.ProtocolTarget.Prefix(), append([]byte(nil), bytes...))
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
	// LastSupplyBlock records the block height of this account's most recent
	// Supply call. Withdraw rejects a same-block withdrawal (see
	// errWithdrawSameBlockAsSupply) so a lump-sum interest credit routed into
	// SupplyIndex (RepayFixedTerm's pool-routing path) can't be sniped by an
	// atomic supply-then-withdraw sequence around it with zero real
	// capital-at-risk duration. Zero means no supply has ever been recorded.
	LastSupplyBlock uint64
}

// FixedTermLoanStatus enumerates the lifecycle states of a fixed-term loan.
type FixedTermLoanStatus string

const (
	// FixedTermLoanStatusActive means the loan is outstanding and not yet
	// fully repaid.
	FixedTermLoanStatusActive FixedTermLoanStatus = "active"
	// FixedTermLoanStatusRepaid means the borrower has repaid principal and
	// the full term's interest in full.
	FixedTermLoanStatusRepaid FixedTermLoanStatus = "repaid"
)

// FixedTermLoan is a locked-rate, fixed-tenure loan -- deliberately separate
// state from UserAccount.DebtNHB/ScaledDebt (the flexible-rate model's
// continuously-re-priced index-based accounting), since a rate locked at
// issuance is economically incompatible with a shared variable index. Draws
// from and credits back to the same pool aggregate (Market.TotalNHBSupplied/
// TotalNHBBorrowed) as the flexible model -- not a separate pool per tenure.
type FixedTermLoan struct {
	// LoanID uniquely identifies this loan, derived deterministically from
	// the borrowing transaction's own hash (see native/market's
	// non-determinism lesson -- never derived from wall-clock time).
	LoanID [32]byte
	// Borrower is the loan's obligor.
	Borrower crypto.Address
	// PoolID identifies which lending pool this loan drew liquidity from.
	PoolID string
	// TenureDays is the loan's fixed term length (30 or 90 in v1).
	TenureDays uint64
	// RateBps is the flat interest rate for this loan's ENTIRE tenure,
	// locked in at issuance and expressed in basis points (e.g. 1200 = 12%
	// of principal owed in total for the 30-day term) -- not an annualised
	// rate prorated by tenure. Later changes to the governance/config rate
	// schedule never affect an already-issued loan.
	RateBps uint64
	// PrincipalWei is the original amount borrowed.
	PrincipalWei *big.Int
	// TotalInterestWei is computed once at issuance as
	// PrincipalWei * RateBps/10000 -- RateBps is a flat rate for the whole
	// tenure, not an APR (see computeFixedTermInterest) -- and owed in full
	// regardless of early repayment timing (a deliberate product decision,
	// not a bug -- see the fixed-term plan's "Risks" section).
	TotalInterestWei *big.Int
	// RepaidWei is the cumulative amount repaid so far across all
	// repayments, applied interest-first then principal (matching the
	// flexible model's existing Repay ordering convention).
	RepaidWei *big.Int
	// IssuedAtBlock is the height at which this loan was originated.
	IssuedAtBlock uint64
	// IssuedAtTime is the block timestamp (deterministic, from
	// StateProcessor.blockTimestamp -- never wall-clock time) at issuance.
	IssuedAtTime uint64
	// MaturityTime is IssuedAtTime + TenureDays*86400.
	MaturityTime uint64
	// Status is the loan's current lifecycle state.
	Status FixedTermLoanStatus
}

// OutstandingWei returns the total remaining obligation (principal +
// full-term interest, minus whatever has already been repaid), floored at
// zero. This is what RepayFixedTerm requires in full to close the loan.
func (l *FixedTermLoan) OutstandingWei() *big.Int {
	if l == nil {
		return big.NewInt(0)
	}
	principal := l.PrincipalWei
	if principal == nil {
		principal = big.NewInt(0)
	}
	interest := l.TotalInterestWei
	if interest == nil {
		interest = big.NewInt(0)
	}
	repaid := l.RepaidWei
	if repaid == nil {
		repaid = big.NewInt(0)
	}
	total := new(big.Int).Add(principal, interest)
	outstanding := new(big.Int).Sub(total, repaid)
	if outstanding.Sign() < 0 {
		return big.NewInt(0)
	}
	return outstanding
}

// TenureRateSchedule maps an allowed tenure (in days) to its locked-at-issuance
// rate, in basis points, flat for the whole tenure (not annualised -- see
// computeFixedTermInterest). Governance-adjustable (see
// Engine.SetFixedTermRateSchedule and native/governance's lending rate
// schedule proposal type); changing it never affects an already-issued
// loan's locked rate.
type TenureRateSchedule map[uint64]uint64

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
	// BorrowCaps aggregates the various borrow throttles applied to the market.
	BorrowCaps BorrowCaps
	// Oracle describes the acceptable freshness and volatility windows for
	// oracle data used to determine market health.
	Oracle OracleConfig
	// Pauses exposes fine-grained switches for halting market operations.
	Pauses ActionPauses
}
