package lending_test

import (
	"math/big"
	"strings"
	"testing"

	"nhbchain/core/types"
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
// economics: a 1-token, 30-day loan at a flat 400bps (4%) PERIOD rate owes
// exactly 1 * 400/10000 = 0.04 tokens of interest for the full 30-day term
// (rateBps is not an APR prorated by tenureDays/365 -- see
// computeFixedTermInterest's own doc comment for why), and only the
// PRINCIPAL leaves the pool's tracked liquidity at issuance -- the
// locked-in interest is a future receivable, not cash already disbursed.
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

	// principal * 400 / 10000 -- a flat period rate, computed the same way
	// as the implementation to avoid a second, independently-wrong formula
	// here.
	expectedInterest := new(big.Int).Mul(principal, big.NewInt(400))
	expectedInterest.Quo(expectedInterest, big.NewInt(10_000))
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
	// max PRINCIPAL-only borrowable is 7.5, but BorrowFixedTerm's own check
	// now also counts this loan's own locked-in interest (see
	// TestBorrowFixedTermIncludesOwnInterestInMaxLTVCheck), so an 8-token
	// loan breaches both the 75% MaxLTV cap AND (once interest is added)
	// the 80% liquidation threshold -- accept either rejection reason,
	// since which check trips first is incidental to whether combined
	// exposure is correctly enforced.
	amount := new(big.Int).Mul(one, big.NewInt(8))
	_, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(5), 30, amount)
	if err == nil || (!strings.Contains(err.Error(), "loan-to-value") && !strings.Contains(err.Error(), "health factor")) {
		t.Fatalf("expected MaxLTV/health rejection, got %v", err)
	}

	// Existing flexible debt counts toward the same combined exposure check.
	if _, err := engine.Borrow(borrower, new(big.Int).Mul(one, big.NewInt(5)), crypto.Address{}, 0); err != nil {
		t.Fatalf("flexible borrow within its own limit should succeed: %v", err)
	}
	_, err = engine.BorrowFixedTerm(borrower, fixedTermLoanID(6), 30, new(big.Int).Mul(one, big.NewInt(3)))
	if err == nil || (!strings.Contains(err.Error(), "loan-to-value") && !strings.Contains(err.Error(), "health factor")) {
		t.Fatalf("expected combined-exposure rejection (5 flexible + 3 fixed-term > 7.5 cap), got %v", err)
	}
}

// TestBorrowRespectsCombinedExposureFromExistingFixedTermLoan is the mirror
// image of TestBorrowFixedTermRespectsMaxLTV above: an existing ACTIVE
// fixed-term loan's outstanding balance must count toward the FLEXIBLE
// Borrow path's own health/MaxLTV check too, not just the other direction.
// Before combinedDebtWei existed, Borrow's health check only ever read
// UserAccount.DebtNHB, which BorrowFixedTerm never updates -- so a borrower
// could take out a fixed-term loan against their full collateral and then
// separately borrow flexibly up to that same full collateral again.
func TestBorrowRespectsCombinedExposureFromExistingFixedTermLoan(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x5A)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x5B)
	borrower := makeAddress(crypto.NHBPrefix, 0x5C)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	// setupCapsEngine gives the borrower 10 collateral, MaxLTV=7500bps ->
	// max borrowable 7.5.
	if _, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(7), 30, new(big.Int).Mul(one, big.NewInt(5))); err != nil {
		t.Fatalf("fixed-term borrow within its own limit should succeed: %v", err)
	}

	// The fixed-term loan's 5-token principal must now count against the
	// flexible path's own check too: 5 (fixed-term, plus a sliver of
	// already-locked-in interest) + 3 (flexible) clears both the 7.5-token
	// MaxLTV cap and (by the same sliver of interest) the 8-token
	// liquidation threshold, so this must be rejected -- accept either
	// rejection reason since which of the two checks trips first depends on
	// that small interest amount, not on whether combined exposure is
	// correctly enforced.
	if _, err := engine.Borrow(borrower, new(big.Int).Mul(one, big.NewInt(3)), crypto.Address{}, 0); err == nil ||
		(!strings.Contains(err.Error(), "loan-to-value") && !strings.Contains(err.Error(), "health factor")) {
		t.Fatalf("expected combined-exposure rejection (5 fixed-term + 3 flexible > 7.5 cap), got %v", err)
	}

	// A flexible borrow that respects the REMAINING headroom (5 fixed-term +
	// 2 flexible = 7 <= 7.5 cap) must still succeed -- this isn't a blanket
	// "any flexible borrow with an active fixed-term loan is rejected" bug.
	if _, err := engine.Borrow(borrower, new(big.Int).Mul(one, big.NewInt(2)), crypto.Address{}, 0); err != nil {
		t.Fatalf("flexible borrow within the remaining combined headroom should succeed: %v", err)
	}
}

// TestBorrowFixedTermIncludesOwnInterestInMaxLTVCheck proves BorrowFixedTerm's
// own origination-time health/MaxLTV check counts the NEW loan's own locked-
// in interest, not just its principal -- matching combinedDebtWei's
// OutstandingWei()-based standard used by every other check in this engine.
// Sizing a loan at exactly the MaxLTV boundary using principal alone must be
// rejected once its own interest is folded in, since the loan would
// otherwise be born already inconsistent with the very combined-exposure
// check that governs every subsequent Borrow/WithdrawCollateral call against
// the same position.
func TestBorrowFixedTermIncludesOwnInterestInMaxLTVCheck(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x60)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x61)
	borrower := makeAddress(crypto.NHBPrefix, 0x62)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	// setupCapsEngine gives the borrower 10 collateral, MaxLTV=7500bps ->
	// max PRINCIPAL-only borrowable is exactly 7.5. A 90-day loan at 600bps
	// on 7.5 tokens owes ~0.111 tokens of interest the instant it's issued
	// (7.5 * 600/10000 * 90/365), pushing true exposure to ~76.11%, over
	// the 75% cap -- so this must now be rejected at issuance, not silently
	// accepted only to already violate combinedDebtWei's standard a moment
	// later.
	principal := new(big.Int).Div(new(big.Int).Mul(one, big.NewInt(75)), big.NewInt(10))
	if _, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(9), 90, principal); err == nil || !strings.Contains(err.Error(), "loan-to-value") {
		t.Fatalf("expected MaxLTV rejection once the loan's own interest is counted, got %v", err)
	}

	// A slightly smaller principal that leaves room for its own interest
	// must still succeed -- this isn't a blanket rejection of every
	// boundary-adjacent fixed-term borrow.
	smallerPrincipal := new(big.Int).Mul(one, big.NewInt(7))
	if _, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(10), 90, smallerPrincipal); err != nil {
		t.Fatalf("fixed-term borrow that leaves room for its own interest should succeed: %v", err)
	}
}

// TestWithdrawCollateralRespectsCombinedExposureFromFixedTermLoan proves
// WithdrawCollateral's own health check also sees an active fixed-term
// loan's outstanding balance -- a withdrawal that would strand a fixed-term
// loan under-collateralized must be rejected even though the flexible-side
// DebtNHB alone (zero, in this case) would look perfectly healthy.
func TestWithdrawCollateralRespectsCombinedExposureFromFixedTermLoan(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x5D)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x5E)
	borrower := makeAddress(crypto.NHBPrefix, 0x5F)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	// WithdrawCollateral moves real ZNHB balance between the collateral
	// module and the borrower's own account, on top of the UserAccount
	// health check -- setupCapsEngine only seeds the UserAccount-level
	// CollateralZNHB figure, not these underlying balances, so a withdrawal
	// that clears the health check still needs them present to complete.
	state.accounts[state.key(collateralAddr)] = &types.Account{BalanceZNHB: new(big.Int).Mul(one, big.NewInt(10))}
	state.accounts[state.key(borrower)] = &types.Account{BalanceZNHB: big.NewInt(0)}

	// Borrower starts with 10 collateral, LiquidationThreshold=8000bps, and
	// no flexible debt at all. A fixed-term loan of 5 tokens principal is
	// the borrower's ONLY exposure -- pre-fix, WithdrawCollateral's check
	// read only DebtNHB (zero here), so it would have let this withdrawal
	// through.
	if _, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(8), 30, new(big.Int).Mul(one, big.NewInt(5))); err != nil {
		t.Fatalf("fixed-term borrow should succeed: %v", err)
	}

	// Withdrawing down to 5 remaining collateral against ~5 combined debt is
	// at ~100% LTV, breaching the 80% liquidation threshold -- must be
	// rejected.
	if err := engine.WithdrawCollateral(borrower, new(big.Int).Mul(one, big.NewInt(5))); err == nil {
		t.Fatal("expected withdrawal to be rejected for stranding the fixed-term loan under-collateralized, got nil error")
	}

	// Withdrawing down to 9 remaining collateral against ~5 combined debt
	// (9*8000 = 72000 >= 5*10000 = 50000) stays healthy and must succeed.
	if err := engine.WithdrawCollateral(borrower, one); err != nil {
		t.Fatalf("withdrawal that keeps the position healthy against combined exposure should succeed: %v", err)
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

// TestRepayFixedTermInterestRoutesToPoolSupplyIndex proves a fixed-term
// repayment's interest portion is credited to the FLEXIBLE pool's
// SupplyIndex (so existing supply-share holders can redeem more), not to
// FeeAccrual.ProtocolFeesWei -- and that it's credited fairly: an existing
// shareholder's redeemable balance grows by exactly the same proportion the
// pool's total redeemable value grew by.
func TestRepayFixedTermInterestRoutesToPoolSupplyIndex(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x70)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x71)
	borrower := makeAddress(crypto.NHBPrefix, 0x72)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	// setupCapsEngine seeds TotalNHBSupplied=TotalSupplyShares=20 tokens at
	// the initial 1:1 ray index -- i.e. an existing flexible supplier (or
	// suppliers, aggregated) redeemable for exactly 20 tokens before this
	// repayment.
	suppliedBefore := new(big.Int).Set(state.market.TotalNHBSupplied)
	sharesBefore := new(big.Int).Set(state.market.TotalSupplyShares)
	redeemableBefore := lending.RedeemableSupply(sharesBefore, state.market.SupplyIndex)
	if redeemableBefore.Cmp(suppliedBefore) != 0 {
		t.Fatalf("test fixture sanity: expected redeemable == supplied at the initial index, got %s want %s", redeemableBefore, suppliedBefore)
	}

	principal := new(big.Int).Set(one)
	loan, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(20), 30, principal)
	if err != nil {
		t.Fatalf("borrow: %v", err)
	}
	interestOwed := new(big.Int).Set(loan.TotalInterestWei)
	if interestOwed.Sign() <= 0 {
		t.Fatalf("test fixture expects nonzero interest, got %s", interestOwed)
	}

	// Fund the borrower enough to pay interest + principal in full (interest
	// is money owed TO the pool, not part of the loan's own proceeds -- see
	// TestRepayFixedTermInterestFirstThenPrincipal's identical reasoning).
	borrowerAcc := state.accounts[state.key(borrower)]
	borrowerAcc.BalanceNHB = new(big.Int).Add(borrowerAcc.BalanceNHB, interestOwed)

	if _, err := engine.RepayFixedTerm(borrower, loan.OutstandingWei()); err != nil {
		t.Fatalf("repay in full: %v", err)
	}

	// TotalNHBSupplied must have grown by EXACTLY the interest portion (here,
	// the whole repayment's interest, i.e. all of interestOwed).
	suppliedAfter := state.market.TotalNHBSupplied
	wantSuppliedAfter := new(big.Int).Add(suppliedBefore, interestOwed)
	if suppliedAfter.Cmp(wantSuppliedAfter) != 0 {
		t.Fatalf("expected TotalNHBSupplied to grow by the interest portion: got %s want %s", suppliedAfter, wantSuppliedAfter)
	}

	// The existing shareholder's redeemable balance (shares unchanged, index
	// bumped) must now equal the new TotalNHBSupplied exactly -- proving the
	// WHOLE lump sum is fairly reflected in what they can redeem, not diluted
	// or partially lost to rounding beyond a single ray-precision unit.
	redeemableAfter := lending.RedeemableSupply(sharesBefore, state.market.SupplyIndex)
	diff := new(big.Int).Sub(redeemableAfter, suppliedAfter)
	if diff.Sign() < 0 {
		diff = diff.Neg(diff)
	}
	if diff.Cmp(big.NewInt(1)) > 0 {
		t.Fatalf("expected redeemable supply to track TotalNHBSupplied within rounding, got redeemable=%s totalSupplied=%s", redeemableAfter, suppliedAfter)
	}

	// The interest must NOT have gone to protocol fees -- accrueInterest
	// never runs a real rate curve in this fixture (no InterestModel set),
	// so feesChanged should never have flipped true via any path other than
	// the one this test guards against.
	if state.fees != nil && state.fees.ProtocolFeesWei != nil && state.fees.ProtocolFeesWei.Sign() != 0 {
		t.Fatalf("expected no protocol fees from this repayment, got %s", state.fees.ProtocolFeesWei)
	}
}

// TestRepayFixedTermInterestFallsBackToFeesWithNoFlexibleSupply proves the
// pool-routing bump is skipped in favor of the old protocol-fees behavior
// when there is no flexible shareholder to fairly credit -- bumping a
// zero-share SupplyIndex would divide by zero, not distribute anything.
// TotalSupplyShares is zeroed AFTER the borrow (rather than before, which
// would make the borrow itself fail on insufficient liquidity, since a
// fixed-term loan draws its principal from this same TotalNHBSupplied pool)
// to isolate exactly the guard this test targets: a pool with real supplied
// liquidity but no shareholder left to claim it, e.g. after every flexible
// depositor has fully withdrawn.
func TestRepayFixedTermInterestFallsBackToFeesWithNoFlexibleSupply(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x73)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x74)
	borrower := makeAddress(crypto.NHBPrefix, 0x75)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	principal := new(big.Int).Set(one)
	loan, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(21), 30, principal)
	if err != nil {
		t.Fatalf("borrow: %v", err)
	}

	// Every flexible shareholder has since fully withdrawn -- no one is left
	// to fairly credit with this loan's interest.
	state.market.TotalSupplyShares = big.NewInt(0)
	interestOwed := new(big.Int).Set(loan.TotalInterestWei)
	if interestOwed.Sign() <= 0 {
		t.Fatalf("test fixture expects nonzero interest, got %s", interestOwed)
	}

	suppliedBeforeRepay := new(big.Int).Set(state.market.TotalNHBSupplied)

	borrowerAcc := state.accounts[state.key(borrower)]
	borrowerAcc.BalanceNHB = new(big.Int).Add(borrowerAcc.BalanceNHB, interestOwed)

	if _, err := engine.RepayFixedTerm(borrower, loan.OutstandingWei()); err != nil {
		t.Fatalf("repay in full: %v", err)
	}

	if state.market.TotalNHBSupplied.Cmp(suppliedBeforeRepay) != 0 {
		t.Fatalf("expected TotalNHBSupplied unchanged (no pool-routing bump) with no flexible shareholder to credit, got %s want %s", state.market.TotalNHBSupplied, suppliedBeforeRepay)
	}
	if state.fees == nil || state.fees.ProtocolFeesWei == nil || state.fees.ProtocolFeesWei.Cmp(interestOwed) != 0 {
		var got interface{} = "nil"
		if state.fees != nil {
			got = state.fees.ProtocolFeesWei
		}
		t.Fatalf("expected the full interest portion routed to protocol fees, got %v want %s", got, interestOwed)
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

// TestFixedTermRateScheduleChangeDoesNotAlterExistingLoan proves the
// invariant governance.applyLendingRateSchedule's own doc comment calls
// out: replacing the tenure->rate schedule (as a passed
// ProposalKindLendingRateSchedule proposal does via
// engine.SetFixedTermRateSchedule) only affects loans issued AFTER the
// change -- an already-issued loan's locked-in RateBps/TotalInterestWei,
// and therefore the amount it charges on repayment, must stay exactly what
// it was at issuance regardless of how many times the schedule changes in
// between.
func TestFixedTermRateScheduleChangeDoesNotAlterExistingLoan(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x80)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x81)
	borrower := makeAddress(crypto.NHBPrefix, 0x82)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	principal := new(big.Int).Set(one)
	loan, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(90), 30, principal)
	if err != nil {
		t.Fatalf("borrow: %v", err)
	}
	if loan.RateBps != 400 {
		t.Fatalf("expected loan locked at the original 400bps rate, got %d", loan.RateBps)
	}
	lockedInterest := new(big.Int).Set(loan.TotalInterestWei)
	lockedOutstanding := new(big.Int).Set(loan.OutstandingWei())

	// Simulate a governance proposal changing the schedule for NEW borrows
	// -- a large jump (400bps -> 5000bps) so any accidental re-read of the
	// live schedule during repayment would be impossible to miss.
	engine.SetFixedTermRateSchedule(lending.TenureRateSchedule{30: 5000, 90: 600})

	// Fund the borrower enough to cover the ORIGINAL locked interest amount
	// only -- if repayment were (incorrectly) charging the new 5000bps
	// rate, this balance would be insufficient and the repay would fail.
	borrowerAcc := state.accounts[state.key(borrower)]
	borrowerAcc.BalanceNHB = new(big.Int).Add(borrowerAcc.BalanceNHB, lockedInterest)

	applied, err := engine.RepayFixedTerm(borrower, lockedOutstanding)
	if err != nil {
		t.Fatalf("repay after schedule change: %v", err)
	}
	if applied.Cmp(lockedOutstanding) != 0 {
		t.Fatalf("expected repayment to fully settle the ORIGINAL locked amount %s, got %s", lockedOutstanding, applied)
	}

	repaidLoan, err := state.GetFixedTermLoan(loan.LoanID)
	if err != nil {
		t.Fatalf("reload loan: %v", err)
	}
	if repaidLoan.RateBps != 400 {
		t.Fatalf("expected the repaid loan record to still show its original 400bps rate, got %d", repaidLoan.RateBps)
	}
	if repaidLoan.TotalInterestWei.Cmp(lockedInterest) != 0 {
		t.Fatalf("expected the repaid loan's TotalInterestWei to remain %s, got %s", lockedInterest, repaidLoan.TotalInterestWei)
	}
	if repaidLoan.Status != lending.FixedTermLoanStatusRepaid {
		t.Fatalf("expected loan status repaid, got %s", repaidLoan.Status)
	}
}
