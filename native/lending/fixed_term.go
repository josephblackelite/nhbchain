package lending

import (
	"errors"
	"math/big"

	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
)

var (
	errFixedTermTenureNotAllowed  = errors.New("lending engine: tenure not in the fixed-term rate schedule")
	errFixedTermLoanAlreadyActive = errors.New("lending engine: borrower already has an active fixed-term loan in this pool")
)

const daysPerYear = 365

// DefaultFixedTermRateSchedule is the v1 tenure->rate table: 30 days at 4%
// APR, 90 days at 6% APR -- placeholder-but-real numbers per the fixed-term
// lending plan. Config/governance-driven overrides are a deliberate
// fast-follow (not built in this pass, see the plan's Phase 2A note); using
// a hardcoded default here is a deployment-flexibility gap, not a safety
// one -- changing it later only affects newly-issued loans, never
// already-locked-in ones, so there is no urgency to wire it up before this
// ships.
var DefaultFixedTermRateSchedule = TenureRateSchedule{
	30: 400,
	90: 600,
}

// computeFixedTermInterest returns the full-term interest owed, computed
// once at issuance: principal * rateBps/10000 * tenureDays/365. Multiplies
// before dividing throughout to avoid precision loss from an intermediate
// truncation (matching this codebase's established big.Int discipline).
func computeFixedTermInterest(principal *big.Int, rateBps, tenureDays uint64) *big.Int {
	if principal == nil || principal.Sign() <= 0 || rateBps == 0 || tenureDays == 0 {
		return big.NewInt(0)
	}
	numerator := new(big.Int).Mul(principal, new(big.Int).SetUint64(rateBps))
	numerator.Mul(numerator, new(big.Int).SetUint64(tenureDays))
	denominator := new(big.Int).SetUint64(10_000 * daysPerYear)
	return numerator.Quo(numerator, denominator)
}

// BorrowFixedTerm originates a new locked-rate, fixed-tenure loan. loanID
// must be derived by the caller from the originating transaction's own hash
// (see core/lending_native.go's applyLendingBorrowFixedTerm) -- never from
// wall-clock time or any other process-local value, since it is hashed into
// persisted consensus state and must be identical across every validator
// that processes this same transaction.
func (e *Engine) BorrowFixedTerm(borrower crypto.Address, loanID [32]byte, tenureDays uint64, amount *big.Int) (*FixedTermLoan, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return nil, err
	}
	if e.params.Pauses.Borrow || e.params.CircuitBreakerActive {
		return nil, errBorrowPaused
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errInvalidAmount
	}
	rateBps, tenureAllowed := e.fixedTermRates[tenureDays]
	if !tenureAllowed || rateBps == 0 {
		return nil, errFixedTermTenureNotAllowed
	}

	market, err := e.ensureMarket()
	if err != nil {
		return nil, err
	}

	if err := e.guardOracle(market); err != nil {
		return nil, err
	}

	if _, exists, err := e.state.GetActiveFixedTermLoanID(e.poolID, borrower); err != nil {
		return nil, err
	} else if exists {
		return nil, errFixedTermLoanAlreadyActive
	}

	liquidity := e.AvailableLiquidity(market)
	if liquidity.Cmp(amount) < 0 {
		return nil, errInsufficientLiquidity
	}

	borrowerUser, err := e.ensureUserAccount(borrower)
	if err != nil {
		return nil, err
	}
	// Sync any existing flexible-side debt to its current accrued value so
	// the combined-exposure health check below (existing flexible debt +
	// this new fixed-term principal) is accurate -- a borrower must not be
	// able to post one collateral balance and independently max out both
	// the flexible and fixed-term borrow paths against it.
	e.syncDebt(borrowerUser, market)

	// The health/MaxLTV check must be run against this loan's REAL total
	// claim -- principal plus its own full-term locked-in interest, exactly
	// what FixedTermLoan.OutstandingWei() reports for an unpaid loan at
	// issuance -- not principal alone. Using principal-only here let a loan
	// originate exactly at the MaxLTV boundary and be immediately
	// inconsistent with combinedDebtWei (the check every other borrow/
	// withdraw path in this engine now applies), which correctly includes a
	// fixed-term loan's interest: the instant this loan is issued, its own
	// interest could already push true exposure past the same cap this
	// check just approved it under.
	newLoanInterest := computeFixedTermInterest(amount, rateBps, tenureDays)
	newLoanTotalClaim := new(big.Int).Add(amount, newLoanInterest)
	projectedDebt := new(big.Int).Add(borrowerUser.DebtNHB, newLoanTotalClaim)
	if !e.positionHealthy(market, borrowerUser.CollateralZNHB, projectedDebt) {
		return nil, errHealthCheckFailed
	}
	if !e.withinMaxLTV(market, borrowerUser.CollateralZNHB, projectedDebt) {
		return nil, errMaxLTVExceeded
	}

	moduleAcc, err := e.loadAccount(e.moduleAddress)
	if err != nil {
		return nil, err
	}
	if moduleAcc.BalanceNHB.Cmp(amount) < 0 {
		return nil, errInsufficientLiquidity
	}
	borrowerAcc, err := e.loadAccount(borrower)
	if err != nil {
		return nil, err
	}

	moduleAcc.BalanceNHB = new(big.Int).Sub(moduleAcc.BalanceNHB, amount)
	borrowerAcc.BalanceNHB = new(big.Int).Add(borrowerAcc.BalanceNHB, amount)

	if err := e.persistAccount(e.moduleAddress, moduleAcc); err != nil {
		return nil, err
	}
	if err := e.persistAccount(borrower, borrowerAcc); err != nil {
		return nil, err
	}

	issuedAt := uint64(0)
	if e.blockTimestamp > 0 {
		issuedAt = uint64(e.blockTimestamp)
	}
	loan := &FixedTermLoan{
		LoanID:           loanID,
		Borrower:         borrower,
		PoolID:           e.poolID,
		TenureDays:       tenureDays,
		RateBps:          rateBps,
		PrincipalWei:     new(big.Int).Set(amount),
		TotalInterestWei: newLoanInterest,
		RepaidWei:        big.NewInt(0),
		IssuedAtBlock:    e.blockHeight,
		IssuedAtTime:     issuedAt,
		MaturityTime:     issuedAt + tenureDays*86400,
		Status:           FixedTermLoanStatusActive,
	}

	// Only the principal leaves the pool's real liquidity right now -- the
	// locked-in interest is a future receivable, not cash already
	// disbursed, so it must not reduce AvailableLiquidity until it's
	// actually repaid (see RepayFixedTerm's matching principal-only
	// TotalNHBBorrowed adjustment).
	market.TotalNHBBorrowed = new(big.Int).Add(market.TotalNHBBorrowed, amount)

	if err := e.state.PutFixedTermLoan(loan); err != nil {
		return nil, err
	}
	if err := e.state.SetActiveFixedTermLoanID(e.poolID, borrower, loanID); err != nil {
		return nil, err
	}
	if err := e.state.PutUserAccount(e.poolID, borrowerUser); err != nil {
		return nil, err
	}
	if err := e.state.PutMarket(e.poolID, market); err != nil {
		return nil, err
	}

	return loan, nil
}

// RepayFixedTerm applies a payment to the borrower's active fixed-term loan
// in this pool, interest-first then principal (matching the flexible
// model's Repay ordering convention). Full-term interest is owed in full
// regardless of how early this repayment happens -- a deliberate product
// decision, not a bug (see the fixed-term plan's "Risks" section). Returns
// the amount actually applied (capped at the loan's remaining outstanding
// balance, mirroring Repay's existing overpayment-capping discipline).
func (e *Engine) RepayFixedTerm(borrower crypto.Address, amount *big.Int) (*big.Int, error) {
	if e == nil || e.state == nil {
		return nil, errNilState
	}
	if err := nativecommon.Guard(e.pauses, moduleName); err != nil {
		return nil, err
	}
	if e.params.Pauses.Repay {
		return nil, errRepayPaused
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, errInvalidAmount
	}

	loanID, exists, err := e.state.GetActiveFixedTermLoanID(e.poolID, borrower)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errNoDebtToRepay
	}
	loan, err := e.state.GetFixedTermLoan(loanID)
	if err != nil {
		return nil, err
	}
	if loan == nil || loan.Status != FixedTermLoanStatusActive {
		return nil, errNoDebtToRepay
	}

	outstandingBefore := loan.OutstandingWei()
	if outstandingBefore.Sign() <= 0 {
		return nil, errNoDebtToRepay
	}

	applied := new(big.Int).Set(amount)
	if applied.Cmp(outstandingBefore) > 0 {
		applied = new(big.Int).Set(outstandingBefore)
	}

	borrowerAcc, err := e.loadAccount(borrower)
	if err != nil {
		return nil, err
	}
	if borrowerAcc.BalanceNHB.Cmp(applied) < 0 {
		return nil, errInsufficientBalance
	}
	moduleAcc, err := e.loadAccount(e.moduleAddress)
	if err != nil {
		return nil, err
	}

	borrowerAcc.BalanceNHB = new(big.Int).Sub(borrowerAcc.BalanceNHB, applied)
	moduleAcc.BalanceNHB = new(big.Int).Add(moduleAcc.BalanceNHB, applied)

	if err := e.persistAccount(borrower, borrowerAcc); err != nil {
		return nil, err
	}
	if err := e.persistAccount(e.moduleAddress, moduleAcc); err != nil {
		return nil, err
	}

	// Interest-first, then principal: figure out how much of THIS payment
	// reduces principal (the rest reduces the locked-in interest owed).
	interestOwed := loan.TotalInterestWei
	if interestOwed == nil {
		interestOwed = big.NewInt(0)
	}
	principalRepaidBefore := big.NewInt(0)
	if loan.RepaidWei.Cmp(interestOwed) > 0 {
		principalRepaidBefore = new(big.Int).Sub(loan.RepaidWei, interestOwed)
	}
	loan.RepaidWei = new(big.Int).Add(loan.RepaidWei, applied)
	principalRepaidAfter := big.NewInt(0)
	if loan.RepaidWei.Cmp(interestOwed) > 0 {
		principalRepaidAfter = new(big.Int).Sub(loan.RepaidWei, interestOwed)
	}
	principalPortion := new(big.Int).Sub(principalRepaidAfter, principalRepaidBefore)

	market, err := e.ensureMarket()
	if err != nil {
		return nil, err
	}
	// Only the principal portion frees up real pool liquidity -- see
	// BorrowFixedTerm's matching comment. The interest portion is routed to
	// protocol fee accrual for now (there is no fixed-term depositor to pay
	// it out to yet -- that's Phase 2C/investor payout, not built in this
	// pass); revisit once that exists so fixed-term interest actually funds
	// fixed-term depositor yield rather than sitting as protocol revenue.
	if principalPortion.Sign() > 0 {
		market.TotalNHBBorrowed = new(big.Int).Sub(market.TotalNHBBorrowed, principalPortion)
		if market.TotalNHBBorrowed.Sign() < 0 {
			market.TotalNHBBorrowed = big.NewInt(0)
		}
	}
	interestPortion := new(big.Int).Sub(applied, principalPortion)

	fullyRepaid := loan.OutstandingWei().Sign() == 0
	if fullyRepaid {
		loan.Status = FixedTermLoanStatusRepaid
	}

	if err := e.state.PutFixedTermLoan(loan); err != nil {
		return nil, err
	}
	if fullyRepaid {
		if err := e.state.ClearActiveFixedTermLoan(e.poolID, borrower); err != nil {
			return nil, err
		}
	}
	if err := e.state.PutMarket(e.poolID, market); err != nil {
		return nil, err
	}

	if interestPortion.Sign() > 0 {
		fees, err := e.ensureFeeAccrual()
		if err != nil {
			return nil, err
		}
		fees.ProtocolFeesWei = new(big.Int).Add(fees.ProtocolFeesWei, interestPortion)
		if err := e.state.PutFeeAccrual(e.poolID, fees); err != nil {
			return nil, err
		}
	}

	return applied, nil
}
