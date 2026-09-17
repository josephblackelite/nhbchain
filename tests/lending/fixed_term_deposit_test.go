package lending_test

import (
	"errors"
	"math/big"
	"strings"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

func setupFixedTermDepositEngine(moduleAddr, collateralAddr, borrower crypto.Address, modify func(*lending.RiskParameters)) (*lending.Engine, *mockEngineState) {
	engine, state := setupFixedTermEngine(moduleAddr, collateralAddr, borrower, modify)
	engine.SetFixedTermDepositRateSchedule(lending.TenureRateSchedule{30: 200, 90: 300})
	return engine, state
}

func fixedTermDepositID(seed byte) [32]byte {
	var id [32]byte
	id[31] = seed
	return id
}

// TestSupplyFixedTermRejectsWithNoLoanInterestReceivable proves the core
// solvency invariant from the ground up: with no active fixed-term LOANS,
// the pool has zero TotalFixedTermLoanInterestReceivableWei to back ANY
// locked-in deposit promise, so even a modest deposit must be rejected --
// the flexible pool's own performance is never allowed to silently
// backstop this guarantee.
func TestSupplyFixedTermRejectsWithNoLoanInterestReceivable(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x90)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x91)
	borrower := makeAddress(crypto.NHBPrefix, 0x92)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermDepositEngine(moduleAddr, collateralAddr, borrower, nil)
	engine.SetState(state)
	engine.SetBlockHeight(10)

	depositor := borrower
	state.accounts[state.key(depositor)].BalanceNHB = new(big.Int).Set(one)

	_, err := engine.SupplyFixedTerm(depositor, fixedTermDepositID(1), 30, one, lending.FixedTermDepositPayoutLumpSumAtMaturity)
	if err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("expected capacity-exceeded rejection with zero loan interest receivable, got %v", err)
	}
}

// TestSupplyFixedTermAcceptsWithinCapacityAndMovesPrincipal proves a deposit
// sized within the pool's outstanding fixed-term loan interest receivable
// is accepted, moves real principal from depositor to module, and updates
// both aggregate counters (principal outstanding + interest owed) by
// exactly the deposit's own contribution.
func TestSupplyFixedTermAcceptsWithinCapacityAndMovesPrincipal(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x93)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x94)
	borrower := makeAddress(crypto.NHBPrefix, 0x95)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermDepositEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	// Borrow first to create real loan-interest receivable capacity: a
	// 7-token, 90-day loan at 600bps owes 0.42 tokens of interest (7 tokens
	// keeps the borrower within the 10-collateral/7500bps MaxLTV cap once
	// its own interest is folded in -- see
	// TestBorrowFixedTermIncludesOwnInterestInMaxLTVCheck for why 10 would
	// be rejected).
	loanPrincipal := new(big.Int).Mul(one, big.NewInt(7))
	loan, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(30), 90, loanPrincipal)
	if err != nil {
		t.Fatalf("borrow: %v", err)
	}
	receivable := new(big.Int).Set(loan.TotalInterestWei)
	if receivable.Sign() <= 0 {
		t.Fatalf("test fixture expects nonzero loan interest receivable, got %s", receivable)
	}

	// Size the deposit so its own interest obligation stays comfortably
	// under the receivable: 1 token at 200bps for 30 days owes 0.02 tokens,
	// far below the ~0.42 token receivable above.
	depositor := makeAddress(crypto.NHBPrefix, 0x96)
	state.accounts[state.key(depositor)] = &types.Account{BalanceNHB: new(big.Int).Set(one)}
	principal := new(big.Int).Set(one)

	deposit, err := engine.SupplyFixedTerm(depositor, fixedTermDepositID(2), 30, principal, lending.FixedTermDepositPayoutPeriodic)
	if err != nil {
		t.Fatalf("supply fixed term: %v", err)
	}
	if deposit.RateBps != 200 {
		t.Fatalf("expected locked rate 200bps, got %d", deposit.RateBps)
	}
	expectedInterest := new(big.Int).Mul(principal, big.NewInt(200))
	expectedInterest.Quo(expectedInterest, big.NewInt(10_000))
	if deposit.TotalInterestOwedWei.Cmp(expectedInterest) != 0 {
		t.Fatalf("expected deposit interest %s, got %s", expectedInterest, deposit.TotalInterestOwedWei)
	}
	if deposit.NextPayoutCycle != 1 {
		t.Fatalf("expected periodic deposit to start at cycle 1, got %d", deposit.NextPayoutCycle)
	}

	if state.market.TotalFixedTermDepositPrincipalWei.Cmp(principal) != 0 {
		t.Fatalf("expected TotalFixedTermDepositPrincipalWei == principal, got %s", state.market.TotalFixedTermDepositPrincipalWei)
	}
	if state.market.TotalFixedTermDepositInterestOwedWei.Cmp(expectedInterest) != 0 {
		t.Fatalf("expected TotalFixedTermDepositInterestOwedWei == deposit interest, got %s", state.market.TotalFixedTermDepositInterestOwedWei)
	}

	depositorAcc := state.accounts[state.key(depositor)]
	if depositorAcc.BalanceNHB.Sign() != 0 {
		t.Fatalf("expected depositor's principal fully debited, got balance %s", depositorAcc.BalanceNHB)
	}
}

// TestSupplyFixedTermRejectsOnceCapacityExhausted proves the capacity cap is
// a real aggregate limit, not a per-call check that resets: once prior
// deposits have consumed the pool's entire loan-interest receivable, a
// further deposit -- even a small one -- is rejected.
func TestSupplyFixedTermRejectsOnceCapacityExhausted(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x97)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x98)
	borrower := makeAddress(crypto.NHBPrefix, 0x99)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermDepositEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	loanPrincipal := new(big.Int).Mul(one, big.NewInt(7))
	if _, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(31), 90, loanPrincipal); err != nil {
		t.Fatalf("borrow: %v", err)
	}

	depositorA := makeAddress(crypto.NHBPrefix, 0x9A)
	state.accounts[state.key(depositorA)] = &types.Account{BalanceNHB: new(big.Int).Mul(one, big.NewInt(100))}
	// A deposit that consumes the ENTIRE ~0.42 token receivable exactly: 21
	// tokens at 200bps/30days owes 21*200/10000 = 0.42 tokens of interest.
	nearlyAll := new(big.Int).Mul(one, big.NewInt(21))
	if _, err := engine.SupplyFixedTerm(depositorA, fixedTermDepositID(3), 30, nearlyAll, lending.FixedTermDepositPayoutLumpSumAtMaturity); err != nil {
		t.Fatalf("first deposit within capacity should succeed: %v", err)
	}

	depositorB := makeAddress(crypto.NHBPrefix, 0x9B)
	state.accounts[state.key(depositorB)] = &types.Account{BalanceNHB: new(big.Int).Set(one)}
	_, err := engine.SupplyFixedTerm(depositorB, fixedTermDepositID(4), 30, one, lending.FixedTermDepositPayoutLumpSumAtMaturity)
	if err == nil || !strings.Contains(err.Error(), "capacity") {
		t.Fatalf("expected capacity-exceeded rejection once the receivable is exhausted, got %v", err)
	}
}

// TestSupplyFixedTermRejectsDisallowedTenure mirrors the borrow-side
// tenure-not-allowed test: a tenure outside the deposit schedule must be
// rejected rather than silently defaulting to some rate.
func TestSupplyFixedTermRejectsDisallowedTenure(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x9C)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x9D)
	borrower := makeAddress(crypto.NHBPrefix, 0x9E)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermDepositEngine(moduleAddr, collateralAddr, borrower, nil)
	engine.SetState(state)
	engine.SetBlockHeight(10)

	depositor := borrower
	state.accounts[state.key(depositor)].BalanceNHB = new(big.Int).Set(one)

	_, err := engine.SupplyFixedTerm(depositor, fixedTermDepositID(5), 60, one, lending.FixedTermDepositPayoutLumpSumAtMaturity)
	if err == nil || !strings.Contains(err.Error(), "tenure") {
		t.Fatalf("expected tenure-not-allowed rejection, got %v", err)
	}
}

// TestSupplyFixedTermRejectsInvalidPayoutPreference proves the payout
// preference is validated against the two known enum values rather than
// accepted as an arbitrary string.
func TestSupplyFixedTermRejectsInvalidPayoutPreference(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x9F)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0xA0)
	borrower := makeAddress(crypto.NHBPrefix, 0xA1)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermDepositEngine(moduleAddr, collateralAddr, borrower, nil)
	engine.SetState(state)
	engine.SetBlockHeight(10)

	depositor := borrower
	state.accounts[state.key(depositor)].BalanceNHB = new(big.Int).Set(one)

	_, err := engine.SupplyFixedTerm(depositor, fixedTermDepositID(6), 30, one, lending.FixedTermDepositPayout("bogus"))
	if err == nil || !strings.Contains(err.Error(), "payout") {
		t.Fatalf("expected invalid-payout rejection, got %v", err)
	}
}

// TestAvailableLiquidityCountsFixedTermDepositPrincipalAsLendableCash proves
// AvailableLiquidity treats fixed-term deposit principal the same as
// flexible-supplied principal: real cash the module actually holds and can
// lend back out, on top of (not instead of) TotalNHBSupplied.
func TestAvailableLiquidityCountsFixedTermDepositPrincipalAsLendableCash(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0xA2)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0xA3)
	borrower := makeAddress(crypto.NHBPrefix, 0xA4)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermDepositEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	loanPrincipal := new(big.Int).Mul(one, big.NewInt(7))
	if _, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(32), 90, loanPrincipal); err != nil {
		t.Fatalf("borrow: %v", err)
	}

	before := engine.AvailableLiquidity(state.market)

	depositor := makeAddress(crypto.NHBPrefix, 0xA5)
	state.accounts[state.key(depositor)] = &types.Account{BalanceNHB: new(big.Int).Set(one)}
	principal := new(big.Int).Set(one)
	if _, err := engine.SupplyFixedTerm(depositor, fixedTermDepositID(7), 30, principal, lending.FixedTermDepositPayoutLumpSumAtMaturity); err != nil {
		t.Fatalf("supply: %v", err)
	}

	after := engine.AvailableLiquidity(state.market)
	want := new(big.Int).Add(before, principal)
	if after.Cmp(want) != 0 {
		t.Fatalf("expected available liquidity to grow by exactly the deposit principal: got %s want %s", after, want)
	}
}

// TestPayFixedTermDepositPrincipalRejectsWhenAvailableLiquidityInsufficient
// is a regression test for a real bug found during adversarial review:
// payFixedTermDepositPrincipal used to check the module account's RAW NHB
// balance against the principal owed, rather than Engine.AvailableLiquidity
// -- but the raw balance also holds Market.FixedTermDepositReserveWei
// (earmarked for OTHER depositors' interest) and any uncollected protocol
// fees, neither of which a principal payout may spend. This test engineers
// a case where the raw balance is comfortably enough to cover the payout but
// AvailableLiquidity is not (simulating "this cash is already lent out or
// earmarked elsewhere"), and proves the payout is correctly rejected rather
// than silently draining money the ledger doesn't consider free.
func TestPayFixedTermDepositPrincipalRejectsWhenAvailableLiquidityInsufficient(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0xA6)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0xA7)
	borrower := makeAddress(crypto.NHBPrefix, 0xA8)
	one := mustBig("1000000000000000000")

	engine, state := setupFixedTermDepositEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
		p.BorrowCaps.PerBlock = nil
	})
	engine.SetState(state)
	engine.SetBlockHeight(10)

	// A 7-token, 90-day loan at 600bps owes 0.42 tokens of receivable --
	// comfortably covers a 20-token, 30-day deposit at 200bps (owes exactly
	// 0.4 tokens).
	loanPrincipal := new(big.Int).Mul(one, big.NewInt(7))
	if _, err := engine.BorrowFixedTerm(borrower, fixedTermLoanID(33), 90, loanPrincipal); err != nil {
		t.Fatalf("borrow: %v", err)
	}

	depositor := makeAddress(crypto.NHBPrefix, 0xA9)
	depositPrincipal := new(big.Int).Mul(one, big.NewInt(20))
	state.accounts[state.key(depositor)] = &types.Account{BalanceNHB: new(big.Int).Set(depositPrincipal)}
	deposit, err := engine.SupplyFixedTerm(depositor, fixedTermDepositID(8), 30, depositPrincipal, lending.FixedTermDepositPayoutLumpSumAtMaturity)
	if err != nil {
		t.Fatalf("supply: %v", err)
	}

	// setupCapsEngine's fixture seeds the module account with a generous
	// fixed raw balance (50 tokens) independent of TotalNHBSupplied -- a
	// convenient stand-in for "cash physically sitting in the module that
	// the ledger doesn't consider free" (in production this would be
	// FixedTermDepositReserveWei or ProtocolFeesWei; here it's simulated by
	// directly inflating TotalNHBBorrowed, as if another loan had already
	// lent out the rest of the pool's real headroom). After the loan+
	// deposit above, AvailableLiquidity = 20(supplied) + 20(deposit) -
	// 7(borrowed) = 33, well above the 20-token principal -- push borrowed
	// up so AvailableLiquidity drops below the principal while the raw
	// module balance (50 - 7 + 20 = 63) stays comfortably above it.
	state.market.TotalNHBBorrowed = new(big.Int).Add(state.market.TotalNHBBorrowed, new(big.Int).Mul(one, big.NewInt(20)))
	availableBefore := engine.AvailableLiquidity(state.market)
	if availableBefore.Cmp(depositPrincipal) >= 0 {
		t.Fatalf("test fixture sanity: expected available liquidity (%s) below the deposit principal (%s)", availableBefore, depositPrincipal)
	}
	moduleBalanceBefore := new(big.Int).Set(state.accounts[state.key(moduleAddr)].BalanceNHB)
	if moduleBalanceBefore.Cmp(depositPrincipal) < 0 {
		t.Fatalf("test fixture sanity: expected the module's raw balance (%s) to already cover the principal (%s) -- the old, buggy check would have wrongly allowed this payout", moduleBalanceBefore, depositPrincipal)
	}

	if err := engine.PayFixedTermDepositPrincipal(deposit, state.market); !errors.Is(err, lending.ErrInsufficientLiquidity) {
		t.Fatalf("expected ErrInsufficientLiquidity when AvailableLiquidity can't cover the principal even though the raw balance can, got %v", err)
	}
	if deposit.Status != lending.FixedTermDepositStatusActive {
		t.Fatalf("expected the deposit to remain active after a rejected payout, got %s", deposit.Status)
	}
	if state.accounts[state.key(moduleAddr)].BalanceNHB.Cmp(moduleBalanceBefore) != 0 {
		t.Fatalf("expected no balance movement on a rejected payout")
	}
	if state.accounts[state.key(depositor)].BalanceNHB.Sign() != 0 {
		t.Fatalf("expected the depositor to receive nothing on a rejected payout, got %s", state.accounts[state.key(depositor)].BalanceNHB)
	}

	// Once the pool's real headroom recovers (e.g. the other loan above is
	// repaid), the same payout must succeed.
	state.market.TotalNHBBorrowed = new(big.Int).Sub(state.market.TotalNHBBorrowed, new(big.Int).Mul(one, big.NewInt(20)))
	if err := engine.PayFixedTermDepositPrincipal(deposit, state.market); err != nil {
		t.Fatalf("expected the payout to succeed once available liquidity recovers: %v", err)
	}
	if deposit.Status != lending.FixedTermDepositStatusMatured {
		t.Fatalf("expected the deposit matured after a successful principal payout, got %s", deposit.Status)
	}
	if state.accounts[state.key(depositor)].BalanceNHB.Cmp(depositPrincipal) != 0 {
		t.Fatalf("expected the depositor to receive exactly the principal, got %s", state.accounts[state.key(depositor)].BalanceNHB)
	}
}
