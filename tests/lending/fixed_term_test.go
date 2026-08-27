package lending_test

import (
	"math/big"
	"strings"
	"testing"

	"nhbchain/crypto"
	"nhbchain/native/lending"
)

func setupFixedTermEngine(moduleAddr, collateralAddr, borrower crypto.Address, modify func(*lending.RiskParameters)) (*lending.Engine, *mockEngineState) {
	engine, state := setupCapsEngine(moduleAddr, collateralAddr, borrower, modify)
	engine.SetBlockTimestamp(1_700_000_000)
	engine.SetFixedTermRateSchedule(lending.TenureRateSchedule{30: 400, 90: 600})
	return engine, state
}

func fixedTermLoanID(seed byte) [32]byte {
	var id [32]byte
	id[31] = seed
	return id
}

// TestBorrowFixedTermComputesInterestAndLocksLiquidity proves the core
// economics: a 1000-token, 30-day loan at 4% APR owes exactly
// 1000 * 400/10000 * 30/365 = ~3.287671... tokens of interest (integer wei
// division truncates), and only the PRINCIPAL leaves the pool's tracked
// liquidity at issuance -- the locked-in interest is a future receivable,
// not cash already disbursed.
func TestBorrowFixedTermComputesInterestAndLocksLiquidity(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x40)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x41)
	borrower := makeAddress(crypto.NHBPrefix, 0x42)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, nil)
	engine.SetState(state)
	engine.SetBlockHeight(10)

	// setupCapsEngine's fixture caps a single borrow at 1 token (PerBlock)
	// and the whole pool at 20 tokens supplied -- stay within both.
	principal := new(big.Int).Set(one)
	loanID := fixedTermLoanID(1)
	loan, err := engine.BorrowFixedTerm(borrower, loanID, 30, principal)
	if err != nil {
		t.Fatalf("borrow fixed term: %v", err)
	}
	if loan.RateBps != 400 {
		t.Fatalf("expected locked rate 400bps, got %d", loan.RateBps)
	}

	// principal * 400 * 30 / (10000 * 365), computed the same way as the
	// implementation to avoid a second, independently-wrong formula here.
	expectedInterest := new(big.Int).Mul(principal, big.NewInt(400))
	expectedInterest.Mul(expectedInterest, big.NewInt(30))
	expectedInterest.Quo(expectedInterest, big.NewInt(10_000*365))
	if loan.TotalInterestWei.Cmp(expectedInterest) != 0 {
		t.Fatalf("expected interest %s, got %s", expectedInterest, loan.TotalInterestWei)
	}
	if loan.Status != lending.FixedTermLoanStatusActive {
		t.Fatalf("expected active status, got %s", loan.Status)
	}
	if loan.MaturityTime != loan.IssuedAtTime+30*86400 {
		t.Fatalf("expected maturity 30 days after issuance, got issuedAt=%d maturity=%d", loan.IssuedAtTime, loan.MaturityTime)
	}

	// Only the principal left the pool's tracked liquidity -- the interest
	// hasn't been disbursed, it's owed.
	if state.market.TotalNHBBorrowed.Cmp(principal) != 0 {
		t.Fatalf("expected TotalNHBBorrowed to increase by principal only (%s), got %s", principal, state.market.TotalNHBBorrowed)
	}

	// Borrower's spendable balance increased by exactly the principal.
	borrowerAcc := state.accounts[state.key(borrower)]
	if borrowerAcc.BalanceNHB.Sign() != principal.Sign() || borrowerAcc.BalanceNHB.Cmp(principal) != 0 {
		t.Fatalf("expected borrower balance %s, got %s", principal, borrowerAcc.BalanceNHB)
	}
}

// TestBorrowFixedTermRejectsDisallowedTenure proves a tenure outside the
// configured schedule is rejected rather than silently defaulting to some
// rate.
func TestBorrowFixedTermRejectsDisallowedTenure(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x43)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x44)
	borrower := makeAddress(crypto.NHBPrefix, 0x45)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, nil)
	engine.SetState(state)
	engine.SetBlockHeight(10)

	_, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(2), 60, new(big.Int).Set(one))
	if err == nil || !strings.Contains(err.Error(), "tenure") {
		t.Fatalf("expected tenure-not-allowed rejection, got %v", err)
	}
}

// TestBorrowFixedTermOneActiveLoanPerBorrower proves the v1 scoping rule: a
// borrower with an active fixed-term loan in this pool cannot originate a
// second one until the first is fully repaid.
func TestBorrowFixedTermOneActiveLoanPerBorrower(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x46)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x47)
	borrower := makeAddress(crypto.NHBPrefix, 0x48)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, nil)
	engine.SetState(state)
	engine.SetBlockHeight(10)

	if _, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(3), 30, new(big.Int).Set(one)); err != nil {
		t.Fatalf("first borrow should succeed: %v", err)
	}
	_, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(4), 30, new(big.Int).Set(one))
	if err == nil || !strings.Contains(err.Error(), "already has an active fixed-term loan") {
		t.Fatalf("expected already-active rejection, got %v", err)
	}
}

// TestBorrowFixedTermRespectsMaxLTV proves the fixed-term path reuses the
// same collateral health/MaxLTV checks as the flexible path, against
// COMBINED exposure -- a borrower cannot post one collateral balance and
// separately max out both the flexible and fixed-term borrow paths against
// it.
func TestBorrowFixedTermRespectsMaxLTV(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x49)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x4A)
	borrower := makeAddress(crypto.NHBPrefix, 0x4B)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	// setupCapsEngine gives the borrower 10 collateral, MaxLTV=7500bps ->
	// max borrowable 7.5. Borrowing 8 must be rejected.
	amount := new(big.Int).Mul(one, big.NewInt(8))
	_, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(5), 30, amount)
	if err == nil || !strings.Contains(err.Error(), "loan-to-value") {
		t.Fatalf("expected MaxLTV rejection, got %v", err)
	}

	// Existing flexible debt counts toward the same combined exposure check.
	if _, err := engine.Borrow(borrower, new(big.Int).Mul(one, big.NewInt(5)), crypto.Address{}, 0); err != nil {
		t.Fatalf("flexible borrow within its own limit should succeed: %v", err)
	}
	_, err = engine.BorrowFixedTerm(borrower, fixedTermLoanID(6), 30, new(big.Int).Mul(one, big.NewInt(3)))
	if err == nil || !strings.Contains(err.Error(), "loan-to-value") {
		t.Fatalf("expected combined-exposure MaxLTV rejection (5 flexible + 3 fixed-term > 7.5 cap), got %v", err)
	}
}

// TestRepayFixedTermInterestFirstThenPrincipal proves the repayment
// ordering and pool-liquidity accounting: a partial payment smaller than
// the total interest owed reduces ONLY the interest balance (principal
// stays fully outstanding, TotalNHBBorrowed unchanged), and a subsequent
// payment that finishes interest and starts on principal frees up the
// corresponding pool liquidity. A final payment that fully closes the loan
// clears the active-loan pointer and marks it repaid.
func TestRepayFixedTermInterestFirstThenPrincipal(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x4C)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x4D)
	borrower := makeAddress(crypto.NHBPrefix, 0x4E)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	principal := new(big.Int).Set(one)
	loan, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(7), 30, principal)
	if err != nil {
		t.Fatalf("borrow: %v", err)
	}
	interestOwed := new(big.Int).Set(loan.TotalInterestWei)
	if interestOwed.Sign() <= 0 {
		t.Fatalf("test fixture expects nonzero interest, got %s", interestOwed)
	}

	// Fund the borrower's spendable balance enough to make these payments
	// (BorrowFixedTerm already credited them `principal`, which is more
	// than enough for these smaller repayments).
	borrowedAfterAmount := new(big.Int).Set(state.market.TotalNHBBorrowed)

	// Payment 1: exactly half the interest owed. Principal must stay fully
	// outstanding and pool liquidity (TotalNHBBorrowed) must be unchanged.
	halfInterest := new(big.Int).Rsh(interestOwed, 1)
	if halfInterest.Sign() == 0 {
		halfInterest = big.NewInt(1)
	}
	if _, err := engine.RepayFixedTerm(borrower, halfInterest); err != nil {
		t.Fatalf("repay half interest: %v", err)
	}
	if state.market.TotalNHBBorrowed.Cmp(borrowedAfterAmount) != 0 {
		t.Fatalf("expected TotalNHBBorrowed unchanged while only interest is being repaid, got %s want %s", state.market.TotalNHBBorrowed, borrowedAfterAmount)
	}
	reloaded := state.loans[loan.LoanID]
	if reloaded.Status != lending.FixedTermLoanStatusActive {
		t.Fatalf("expected loan still active after partial interest payment")
	}

	// Payment 2: pay everything else (remaining interest + full principal).
	// The borrower only ever received `principal` from the borrow itself
	// (the interest is money owed TO the pool, not credited to them) --
	// fund the interest portion here to simulate outside income, the same
	// way a real borrower would need to earn it rather than pay it from
	// the loan's own proceeds.
	remaining := reloaded.OutstandingWei()
	borrowerAcc := state.accounts[state.key(borrower)]
	borrowerAcc.BalanceNHB = new(big.Int).Add(borrowerAcc.BalanceNHB, interestOwed)
	if _, err := engine.RepayFixedTerm(borrower, remaining); err != nil {
		t.Fatalf("repay remainder: %v", err)
	}
	finalLoan := state.loans[loan.LoanID]
	if finalLoan.Status != lending.FixedTermLoanStatusRepaid {
		t.Fatalf("expected loan repaid, got status %s", finalLoan.Status)
	}
	if finalLoan.OutstandingWei().Sign() != 0 {
		t.Fatalf("expected zero outstanding after full repayment, got %s", finalLoan.OutstandingWei())
	}
	if state.market.TotalNHBBorrowed.Sign() != 0 {
		t.Fatalf("expected TotalNHBBorrowed back to zero after full principal repayment, got %s", state.market.TotalNHBBorrowed)
	}

	// The active-loan pointer must be cleared -- the borrower can now take
	// out a new fixed-term loan.
	if _, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(8), 30, new(big.Int).Set(one)); err != nil {
		t.Fatalf("expected a new fixed-term loan to be allowed after the first was fully repaid, got %v", err)
	}
}

// TestRepayFixedTermCapsOverpaymentAtOutstanding proves offering more than
// the loan's remaining outstanding balance only applies (and only debits)
// the outstanding amount, mirroring the flexible Repay's existing
// overpayment-capping discipline.
func TestRepayFixedTermCapsOverpaymentAtOutstanding(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x4F)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x50)
	borrower := makeAddress(crypto.NHBPrefix, 0x51)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, nil)
	engine.SetState(state)
	engine.SetBlockHeight(10)

	principal := new(big.Int).Set(one)
	loan, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(9), 30, principal)
	if err != nil {
		t.Fatalf("borrow: %v", err)
	}
	outstanding := loan.OutstandingWei()

	// Fund the borrower well beyond outstanding (the borrow above only
	// credited them `principal`, which alone wouldn't cover principal +
	// interest) so the test actually exercises capping, not an unrelated
	// insufficient-balance rejection.
	borrowerAcc := state.accounts[state.key(borrower)]
	borrowerAcc.BalanceNHB = new(big.Int).Mul(one, big.NewInt(1000))

	overpay := new(big.Int).Mul(one, big.NewInt(1000))
	applied, err := engine.RepayFixedTerm(borrower, overpay)
	if err != nil {
		t.Fatalf("repay: %v", err)
	}
	if applied.Cmp(outstanding) != 0 {
		t.Fatalf("expected applied amount capped at outstanding %s, got %s", outstanding, applied)
	}
}

// TestRepayFixedTermNoActiveLoan proves repaying with nothing outstanding
// is rejected rather than silently succeeding.
func TestRepayFixedTermNoActiveLoan(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x52)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x53)
	borrower := makeAddress(crypto.NHBPrefix, 0x54)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, nil)
	engine.SetState(state)
	engine.SetBlockHeight(10)

	_, err := engine.RepayFixedTerm(borrower, new(big.Int).Set(one))
	if err == nil || !strings.Contains(err.Error(), "no outstanding debt") {
		t.Fatalf("expected no-debt-to-repay rejection, got %v", err)
	}
}
