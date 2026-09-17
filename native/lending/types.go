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
	// TotalFixedTermDepositPrincipalWei is the aggregate principal currently
	// owed back to all live fixed-term deposits (Milestone 3) -- a
	// liability parallel to, but deliberately kept separate from,
	// TotalNHBSupplied (flexible suppliers' claims via SupplyShares/
	// SupplyIndex): a fixed-term depositor's claim is their own deposit
	// record, not a SupplyShares balance. Folded into AvailableLiquidity so
	// the pool never lends out money it owes back to a depositor.
	TotalFixedTermDepositPrincipalWei *big.Int
	// TotalFixedTermDepositInterestOwedWei is the aggregate interest still
	// owed (not yet paid out) across all live fixed-term deposits.
	TotalFixedTermDepositInterestOwedWei *big.Int
	// FixedTermDepositReserveWei is NHB already collected from fixed-term
	// LOAN interest (RepayFixedTerm) and earmarked toward
	// TotalFixedTermDepositInterestOwedWei -- not yet paid out to a
	// depositor, and deliberately NOT credited to flexible suppliers via
	// SupplyIndex (crediting it there too would double-count the same
	// dollar as both "owed to a depositor" and "owed to flexible
	// suppliers"). See RepayFixedTerm's three-way interest split for how
	// this fills up and settleLendingDepositPayouts for how it drains.
	// Bounded above by TotalFixedTermDepositInterestOwedWei at all times by
	// construction -- never tops up past what is still actually owed.
	FixedTermDepositReserveWei *big.Int
	// TotalFixedTermLoanInterestReceivableWei is the aggregate interest
	// still owed BY all ACTIVE (non-delinquent) fixed-term loans -- the
	// pool's real, currently-bankable capacity to fund fixed-term deposit
	// obligations. SupplyFixedTerm's issuance-time cap requires a new
	// deposit's total interest, added to the pool's current
	// TotalFixedTermDepositInterestOwedWei, to never exceed this value.
	// Delinquent loans' remaining interest is deliberately written off from
	// this aggregate once a loan reaches FixedTermLoanStatusDelinquent (see
	// settleLendingAutoDebits' delinquency path) -- it is no longer safely
	// bankable once auto-billing has given up on collecting it.
	TotalFixedTermLoanInterestReceivableWei *big.Int
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
	// FixedTermLoanStatusDelinquent means settleLendingAutoDebits hit
	// AutoDebitMaxConsecutiveMisses consecutive missed interest
	// installments -- independent of and in addition to the LTV-based
	// Liquidate path, which never considers fixed-term debt (see
	// combinedDebtWei's doc comment for why that stays separate). Auto-debit
	// stops scheduling further attempts once a loan reaches this status.
	//
	// Deliberately does NOT itself seize collateral: doing that correctly
	// requires converting seized ZNHB into real NHB backing for the pool
	// (the module's NHB and collateral module's ZNHB balances are
	// completely separate assets -- there is no atomic, in-module way to
	// convert one into the other, that's the swap module's job), which is
	// its own dedicated design/review pass, not something to freehand
	// alongside the auto-debit billing mechanism itself. This status exists
	// so the condition is visible and actionable (an event fires, the
	// portal can surface it, an operator can intervene) rather than
	// silently doing nothing -- the collateral-recovery mechanism is a
	// deliberately separate, not-yet-built next step.
	FixedTermLoanStatusDelinquent FixedTermLoanStatus = "delinquent"
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
	// AutoDebitEnabled controls whether settleLendingAutoDebits
	// (core/lending_autodebit_settlement.go) attempts to collect this
	// loan's periodic interest installments automatically. Defaults to true
	// at issuance (opt-out, not opt-in) -- there is no transaction to flip
	// it yet (a fast-follow), but the settlement hook already honors it so
	// that future control point is a pure additive change, not a redesign.
	AutoDebitEnabled bool
	// NextAutoDebitCycle is the 1-based index of the next interest
	// installment settleLendingAutoDebits should attempt (1..
	// TotalAutoDebitCycles(TenureDays)). A 30-day loan has exactly 1 cycle,
	// due at maturity; a 90-day loan has 3, due at day 30/60/90. Once this
	// exceeds the loan's total cycle count, auto-debit is done -- the
	// borrower still must repay principal (and any interest auto-debit
	// never collected) manually via RepayFixedTerm; auto-debit never
	// touches principal or collateral itself.
	NextAutoDebitCycle uint32
	// ConsecutiveMissedAutoDebits counts auto-debit attempts in a row that
	// failed for insufficient balance, across the whole loan (not
	// per-cycle) -- resets to zero on any successful debit. Reaching
	// AutoDebitMaxConsecutiveMisses triggers LiquidateDelinquentFixedTerm.
	ConsecutiveMissedAutoDebits uint32
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

// FixedTermDepositStatus enumerates the lifecycle states of a fixed-term
// deposit.
type FixedTermDepositStatus string

const (
	// FixedTermDepositStatusActive means the deposit is outstanding: some
	// principal and/or interest has not yet been paid out.
	FixedTermDepositStatusActive FixedTermDepositStatus = "active"
	// FixedTermDepositStatusMatured means the deposit has been paid out in
	// full (principal and all interest) and is closed.
	FixedTermDepositStatusMatured FixedTermDepositStatus = "matured"
)

// FixedTermDepositPayout selects how a fixed-term deposit's interest is
// paid out over its term.
type FixedTermDepositPayout string

const (
	// FixedTermDepositPayoutLumpSumAtMaturity pays principal and the
	// ENTIRE locked-in interest in one payment at maturity -- nothing is
	// paid before then.
	FixedTermDepositPayoutLumpSumAtMaturity FixedTermDepositPayout = "lump_sum_at_maturity"
	// FixedTermDepositPayoutPeriodic pays interest in monthly installments
	// during the term (mirroring FixedTermLoan's own auto-debit cadence,
	// AutoDebitCycleLengthDays, in the opposite direction), with principal
	// returned separately at maturity once all interest has been paid.
	FixedTermDepositPayoutPeriodic FixedTermDepositPayout = "periodic_interest_principal_at_maturity"
)

// FixedTermDeposit is a locked-rate, fixed-tenure deposit -- the mirror
// image of FixedTermLoan on the pool's liability side. Deliberately
// separate state from UserAccount.SupplyShares/Market.SupplyIndex (the
// flexible model's continuously-re-priced, pro-rata accounting): a
// depositor here is promised a SPECIFIC return regardless of what the
// flexible pool actually earns, which is economically incompatible with a
// shared variable index. This locked promise is only safe because of the
// pool-level cap enforced at issuance (see SupplyFixedTerm): the pool
// never promises more in aggregate deposit interest than it is already
// owed in aggregate fixed-term LOAN interest
// (Market.TotalFixedTermLoanInterestReceivableWei), so the fixed/fixed
// spread -- not the flexible pool's variable performance -- is what backs
// this guarantee.
type FixedTermDeposit struct {
	// DepositID uniquely identifies this deposit, derived deterministically
	// from the depositing transaction's own hash (same discipline as
	// FixedTermLoan.LoanID -- never wall-clock time).
	DepositID [32]byte
	// Depositor is the deposit's beneficiary.
	Depositor crypto.Address
	// PoolID identifies which lending pool this deposit's principal was
	// added to.
	PoolID string
	// TenureDays is the deposit's fixed term length.
	TenureDays uint64
	// RateBps is the flat interest rate for this deposit's ENTIRE tenure,
	// locked in at issuance, expressed in basis points -- same flat
	// period-rate convention as FixedTermLoan.RateBps, not an annualised
	// rate. NOT structurally enforced to stay <= the borrow-side schedule's
	// rate for the same tenure -- that comparison is deliberately left
	// un-validated at governance proposal-submission time (see
	// ProposalKindLendingDepositRateSchedule's doc comment in
	// native/governance/types.go for why). The pool's actual solvency
	// backstop is SupplyFixedTerm's aggregate capacity check, not a
	// per-tenure rate spread.
	RateBps uint64
	// PrincipalWei is the amount originally deposited.
	PrincipalWei *big.Int
	// TotalInterestOwedWei is computed once at issuance as
	// PrincipalWei * RateBps/10000, mirroring computeFixedTermInterest.
	TotalInterestOwedWei *big.Int
	// PaidInterestWei is the cumulative interest actually paid out so far.
	// For FixedTermDepositPayoutLumpSumAtMaturity this stays zero until
	// maturity, when it jumps straight to TotalInterestOwedWei in the same
	// payment principal is returned. For FixedTermDepositPayoutPeriodic it
	// grows with each periodic payout, mirroring FixedTermLoan.RepaidWei's
	// role on the borrow side.
	PaidInterestWei *big.Int
	// Payout selects the interest payout schedule (see
	// FixedTermDepositPayout).
	Payout FixedTermDepositPayout
	// IssuedAtBlock is the height at which this deposit was originated.
	IssuedAtBlock uint64
	// IssuedAtTime is the block timestamp (deterministic -- never
	// wall-clock time) at issuance.
	IssuedAtTime uint64
	// MaturityTime is IssuedAtTime + TenureDays*86400.
	MaturityTime uint64
	// Status is the deposit's current lifecycle state.
	Status FixedTermDepositStatus
	// NextPayoutCycle is the 1-based index of the next periodic interest
	// installment settleLendingDepositPayouts should attempt -- only
	// meaningful when Payout is FixedTermDepositPayoutPeriodic (unused,
	// left at zero, for the lump-sum preference).
	NextPayoutCycle uint32
}

// OutstandingInterestWei returns the interest still owed on this deposit
// (TotalInterestOwedWei minus PaidInterestWei so far), floored at zero.
func (d *FixedTermDeposit) OutstandingInterestWei() *big.Int {
	if d == nil {
		return big.NewInt(0)
	}
	owed := d.TotalInterestOwedWei
	if owed == nil {
		owed = big.NewInt(0)
	}
	paid := d.PaidInterestWei
	if paid == nil {
		paid = big.NewInt(0)
	}
	outstanding := new(big.Int).Sub(owed, paid)
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
