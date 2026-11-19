package lending

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-R5T-50: BorrowFixedTerm added the new principal to TotalNHBBorrowed without
// first accruing the interest of the window the market had been idle. The next
// accrual of any kind then charged that whole idle window at a utilisation that
// already included the new loan. From the accounting-fixes height the market is
// accrued first, as Borrow, Repay, Supply, Withdraw, Liquidate and RepayFixedTerm
// have always done.

const (
	ftIdleStart  = int64(1_700_000_000)
	ftIdleWindow = int64(30 * 86_400)
	ftNextStep   = int64(3_600)
)

// ftEngine returns an engine over a market that was last refreshed at ftIdleStart,
// holding 1,000,000,000 wei of supply and no borrow, with one borrower who holds
// 2,000,000,000 wei of collateral. Rates: 50% a year at 50% utilisation (the model
// the accrual tests use), a 30-day fixed term at 12%, no reserve cut.
func ftEngine(t *testing.T, fixes bool) (*Engine, *mockEngineState, crypto.Address) {
	t.Helper()
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x21)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x22)
	borrower := makeAddress(crypto.NHBPrefix, 0x23)
	engine := NewEngine(moduleAddr, collateralAddr, RiskParameters{MaxLTV: 6_000, LiquidationThreshold: 8_500})
	engine.SetInterestModel(NewInterestModel(0, 1, 0, 1))
	engine.SetFixedTermRateSchedule(TenureRateSchedule{30: 1_200})
	engine.SetAccountingFixes(fixes)
	state := newMockEngineState()
	state.market = newAccrualMarket(100, uint64(ftIdleStart))
	state.market.TotalNHBBorrowed = big.NewInt(0)
	state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(1_000_000_000), BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(borrower)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
	state.users[state.key(borrower)] = &UserAccount{
		Address:        borrower,
		CollateralZNHB: big.NewInt(2_000_000_000),
		SupplyShares:   big.NewInt(0),
		DebtNHB:        big.NewInt(0),
		ScaledDebt:     big.NewInt(0),
	}
	engine.SetState(state)
	engine.SetPoolID("default")
	return engine, state, borrower
}

func ftLoanID(b byte) [32]byte {
	var id [32]byte
	id[0] = b
	return id
}

// referenceAccrual is what a market that held `borrowed` since `stamp` accrues by
// `at`: the figure both behaviours are measured against.
func referenceAccrual(t *testing.T, borrowed *big.Int, stamp, at int64) *Market {
	t.Helper()
	engine, state := accrualEngine(t)
	market := newAccrualMarket(100, uint64(stamp))
	market.TotalNHBBorrowed = new(big.Int).Set(borrowed)
	accrueAt(t, engine, state, market, 200, at)
	return market
}

func TestBorrowFixedTermAccruesTheIdleWindowFirstFromTheGate(t *testing.T) {
	principal := big.NewInt(500_000_000)
	t1 := ftIdleStart + ftIdleWindow
	t2 := t1 + ftNextStep

	engine, state, borrower := ftEngine(t, true)
	engine.SetBlockHeight(200)
	engine.SetBlockTimestamp(t1)
	if _, err := engine.BorrowFixedTerm(borrower, ftLoanID(1), 30, principal); err != nil {
		t.Fatalf("borrow fixed term: %v", err)
	}
	if got := state.market.LastUpdateTimestamp; got != uint64(t1) {
		t.Fatalf("the market was stamped at %d after the borrow, want %d (accrued first)", got, t1)
	}
	if got := state.market.LastUpdateBlock; got != 200 {
		t.Fatalf("the market was stamped at block %d after the borrow, want 200", got)
	}

	// The next accrual charges only the time since the loan, on the loan.
	engine.SetBlockHeight(201)
	engine.SetBlockTimestamp(t2)
	if _, _, err := engine.accrueInterest(state.market); err != nil {
		t.Fatalf("accrue after the loan: %v", err)
	}
	want := referenceAccrual(t, principal, t1, t2)
	if state.market.BorrowIndex.Cmp(want.BorrowIndex) != 0 || state.market.SupplyIndex.Cmp(want.SupplyIndex) != 0 ||
		state.market.TotalNHBBorrowed.Cmp(want.TotalNHBBorrowed) != 0 || state.market.TotalNHBSupplied.Cmp(want.TotalNHBSupplied) != 0 {
		t.Fatalf("after the loan the market holds borrow index %s, supply index %s, borrowed %s, supplied %s; want %s, %s, %s, %s (one hour on the loan only)",
			state.market.BorrowIndex, state.market.SupplyIndex, state.market.TotalNHBBorrowed, state.market.TotalNHBSupplied,
			want.BorrowIndex, want.SupplyIndex, want.TotalNHBBorrowed, want.TotalNHBSupplied)
	}
}

func TestBorrowFixedTermBeforeTheGateKeepsTheOldWindow(t *testing.T) {
	principal := big.NewInt(500_000_000)
	t1 := ftIdleStart + ftIdleWindow
	t2 := t1 + ftNextStep

	engine, state, borrower := ftEngine(t, false)
	engine.SetBlockHeight(200)
	engine.SetBlockTimestamp(t1)
	if _, err := engine.BorrowFixedTerm(borrower, ftLoanID(1), 30, principal); err != nil {
		t.Fatalf("borrow fixed term: %v", err)
	}
	if got := state.market.LastUpdateTimestamp; got != uint64(ftIdleStart) {
		t.Fatalf("before the gate the borrow moved the market stamp to %d, want it left at %d", got, ftIdleStart)
	}
	engine.SetBlockHeight(201)
	engine.SetBlockTimestamp(t2)
	if _, _, err := engine.accrueInterest(state.market); err != nil {
		t.Fatalf("accrue after the loan: %v", err)
	}
	// The old behaviour, pinned: the whole 30 days and the hour are charged on the loan.
	want := referenceAccrual(t, principal, ftIdleStart, t2)
	if state.market.BorrowIndex.Cmp(want.BorrowIndex) != 0 || state.market.TotalNHBSupplied.Cmp(want.TotalNHBSupplied) != 0 {
		t.Fatalf("before the gate the market holds borrow index %s, supplied %s; the old behaviour is %s and %s",
			state.market.BorrowIndex, state.market.TotalNHBSupplied, want.BorrowIndex, want.TotalNHBSupplied)
	}
	fixed := referenceAccrual(t, principal, t1, t2)
	if state.market.TotalNHBSupplied.Cmp(fixed.TotalNHBSupplied) <= 0 {
		t.Fatalf("the characterisation is vacuous: the old behaviour (%s) does not charge more than the fixed one (%s)",
			state.market.TotalNHBSupplied, fixed.TotalNHBSupplied)
	}
}

// A flexible borrower's debt is read at the index of the moment, so the health
// check of a fixed-term borrow sees what that borrower really owes.
func TestBorrowFixedTermHealthCheckSeesTheFreshIndex(t *testing.T) {
	// One year idle. The flexible debt of 500,000,000 grows by about half; with
	// collateral of 1,400,000,000 and a 60% cap the room is 840,000,000, so a
	// 200,000,000 loan (224,000,000 with its 12%) fits on the stale debt (724) and
	// not on the real one (about 750 + 224).
	build := func(fixes bool) (*Engine, *mockEngineState, crypto.Address) {
		engine, state, borrower := ftEngine(t, fixes)
		state.market = newAccrualMarket(100, uint64(ftIdleStart))
		user := state.users[state.key(borrower)]
		user.CollateralZNHB = big.NewInt(1_400_000_000)
		user.ScaledDebt = big.NewInt(500_000_000)
		user.DebtNHB = big.NewInt(500_000_000)
		return engine, state, borrower
	}
	at := ftIdleStart + secondsPerYear

	engine, _, borrower := build(true)
	engine.SetBlockHeight(200)
	engine.SetBlockTimestamp(at)
	if _, err := engine.BorrowFixedTerm(borrower, ftLoanID(2), 30, big.NewInt(200_000_000)); !errors.Is(err, errMaxLTVExceeded) {
		t.Fatalf("from the gate a loan the borrower's real debt cannot support must be refused with the LTV error, got %v", err)
	}

	engine, _, borrower = build(false)
	engine.SetBlockHeight(200)
	engine.SetBlockTimestamp(at)
	if _, err := engine.BorrowFixedTerm(borrower, ftLoanID(2), 30, big.NewInt(200_000_000)); err != nil {
		t.Fatalf("before the gate the same loan is accepted (the old behaviour is pinned), got %v", err)
	}
}

// The accrual the borrow now performs can produce a protocol fee; it must be saved.
func TestBorrowFixedTermPersistsTheFeeItAccrues(t *testing.T) {
	build := func(fixes bool) (*Engine, *mockEngineState, crypto.Address) {
		engine, state, borrower := ftEngine(t, fixes)
		engine.SetReserveFactor(1_000)
		// Some other flexible debt is outstanding, so the idle window earns interest.
		state.market = newAccrualMarket(100, uint64(ftIdleStart))
		return engine, state, borrower
	}
	at := ftIdleStart + ftIdleWindow

	engine, state, borrower := build(true)
	engine.SetBlockHeight(200)
	engine.SetBlockTimestamp(at)
	if _, err := engine.BorrowFixedTerm(borrower, ftLoanID(3), 30, big.NewInt(100_000_000)); err != nil {
		t.Fatalf("borrow fixed term: %v", err)
	}
	if state.fees == nil || state.fees.ProtocolFeesWei == nil || state.fees.ProtocolFeesWei.Sign() <= 0 {
		t.Fatalf("the reserve share of the idle window's interest was not saved: %+v", state.fees)
	}

	engine, state, borrower = build(false)
	engine.SetBlockHeight(200)
	engine.SetBlockTimestamp(at)
	if _, err := engine.BorrowFixedTerm(borrower, ftLoanID(3), 30, big.NewInt(100_000_000)); err != nil {
		t.Fatalf("borrow fixed term: %v", err)
	}
	if state.fees != nil {
		t.Fatalf("before the gate the borrow saved a fee record: %+v", state.fees)
	}
}
