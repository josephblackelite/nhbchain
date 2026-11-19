package lending

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-R5T-52: a borrow that carries a developer fee paid the fee to the collector
// at once (the borrower's debt includes it) and ALSO added the same amount to the
// pool's withdrawable developer-fee counter. Withdrawing that counter paid the
// fee a second time out of the shared module balance and lowered the suppliers'
// total. From the accounting-fixes height the borrow-time payment is the only one.

type devFeeFixture struct {
	engine    *Engine
	state     *mockEngineState
	module    crypto.Address
	borrower  crypto.Address
	collector crypto.Address
	recipient crypto.Address
}

func newDevFeeFixture(t *testing.T, fixes bool) *devFeeFixture {
	t.Helper()
	f := &devFeeFixture{
		module:    makeAddress(crypto.NHBPrefix, 0x31),
		borrower:  makeAddress(crypto.NHBPrefix, 0x32),
		collector: makeAddress(crypto.NHBPrefix, 0x33),
		recipient: makeAddress(crypto.NHBPrefix, 0x34),
	}
	collateral := makeAddress(crypto.ZNHBPrefix, 0x35)
	f.engine = NewEngine(f.module, collateral, RiskParameters{MaxLTV: 6_000, LiquidationThreshold: 8_500, DeveloperFeeCapBps: 500})
	f.engine.SetAccountingFixes(fixes)
	f.engine.SetBlockHeight(10)
	f.engine.SetBlockTimestamp(1_700_000_000)
	f.state = newMockEngineState()
	f.state.market = &Market{
		TotalNHBSupplied:    big.NewInt(1_000_000),
		TotalSupplyShares:   big.NewInt(1_000_000),
		TotalNHBBorrowed:    big.NewInt(0),
		SupplyIndex:         new(big.Int).Set(ray),
		BorrowIndex:         new(big.Int).Set(ray),
		LastUpdateBlock:     10,
		LastUpdateTimestamp: 1_700_000_000,
	}
	for _, a := range []struct {
		addr crypto.Address
		nhb  int64
	}{{f.module, 1_000_000}, {f.borrower, 0}, {f.collector, 0}, {f.recipient, 0}} {
		f.state.accounts[f.state.key(a.addr)] = &types.Account{BalanceNHB: big.NewInt(a.nhb), BalanceZNHB: big.NewInt(0)}
	}
	f.state.users[f.state.key(f.borrower)] = &UserAccount{
		Address:        f.borrower,
		CollateralZNHB: big.NewInt(2_000_000),
		SupplyShares:   big.NewInt(0),
		DebtNHB:        big.NewInt(0),
		ScaledDebt:     big.NewInt(0),
	}
	f.engine.SetState(f.state)
	f.engine.SetPoolID("default")
	return f
}

func (f *devFeeFixture) balance(addr crypto.Address) *big.Int {
	return new(big.Int).Set(f.state.accounts[f.state.key(addr)].BalanceNHB)
}

// borrowWithFee draws 10,000 with a 100 basis point fee: a fee of 100.
func (f *devFeeFixture) borrowWithFee(t *testing.T) {
	t.Helper()
	fee, err := f.engine.Borrow(f.borrower, big.NewInt(10_000), f.collector, 100)
	if err != nil {
		t.Fatalf("borrow with a developer fee: %v", err)
	}
	if fee.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("the borrow reported a fee of %s, want 100", fee)
	}
	if got := f.balance(f.collector); got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("the collector holds %s after the borrow, want the 100 fee", got)
	}
	if got := f.balance(f.borrower); got.Cmp(big.NewInt(10_000)) != 0 {
		t.Fatalf("the borrower holds %s after the borrow, want 10,000", got)
	}
}

func TestDeveloperFeeIsPaidOnceFromTheGate(t *testing.T) {
	f := newDevFeeFixture(t, true)
	f.borrowWithFee(t)

	if f.state.fees != nil && f.state.fees.DeveloperFeesWei != nil && f.state.fees.DeveloperFeesWei.Sign() != 0 {
		t.Fatalf("the fee already paid to the collector was also counted as withdrawable: %s", f.state.fees.DeveloperFeesWei)
	}
	suppliedBefore := new(big.Int).Set(f.state.market.TotalNHBSupplied)
	moduleBefore := f.balance(f.module)

	if _, err := f.engine.WithdrawDeveloperFees(f.recipient, big.NewInt(100)); !errors.Is(err, errInsufficientLiquidity) {
		t.Fatalf("a second payment of the same fee must be refused (nothing is withdrawable), got %v", err)
	}
	if got := f.balance(f.recipient); got.Sign() != 0 {
		t.Fatalf("the recipient was paid %s", got)
	}
	if f.balance(f.module).Cmp(moduleBefore) != 0 || f.state.market.TotalNHBSupplied.Cmp(suppliedBefore) != 0 {
		t.Fatalf("a refused withdrawal changed the module balance or the suppliers' total")
	}
}

func TestDeveloperFeeBeforeTheGateIsStillCountedTwice(t *testing.T) {
	f := newDevFeeFixture(t, false)
	f.borrowWithFee(t)

	if f.state.fees == nil || f.state.fees.DeveloperFeesWei == nil || f.state.fees.DeveloperFeesWei.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("before the gate the borrow counts the fee as withdrawable (the old behaviour is pinned), got %+v", f.state.fees)
	}
	suppliedBefore := new(big.Int).Set(f.state.market.TotalNHBSupplied)
	if _, err := f.engine.WithdrawDeveloperFees(f.recipient, big.NewInt(100)); err != nil {
		t.Fatalf("withdraw developer fees: %v", err)
	}
	// The old behaviour: a second payment out of the shared module balance, at the
	// suppliers' expense.
	if got := f.balance(f.recipient); got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("recipient holds %s, the old behaviour pays the fee a second time", got)
	}
	if got := new(big.Int).Sub(suppliedBefore, f.state.market.TotalNHBSupplied); got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("the suppliers' total fell by %s, the old behaviour lowers it by the fee", got)
	}
}

// With the fix the pool is whole again once the loan is repaid: the module holds
// what it started with plus nothing it did not collect, and the only party paid a
// fee is the collector, once.
func TestDeveloperFeeBorrowAndRepayLeavesThePoolWhole(t *testing.T) {
	f := newDevFeeFixture(t, true)
	f.borrowWithFee(t)
	// The borrower owes 10,100 and holds 10,000; the fee is paid out of other funds.
	f.state.accounts[f.state.key(f.borrower)].BalanceNHB = big.NewInt(10_100)
	if _, err := f.engine.Repay(f.borrower, big.NewInt(10_100)); err != nil {
		t.Fatalf("repay: %v", err)
	}
	if got := f.balance(f.module); got.Cmp(big.NewInt(1_000_000)) != 0 {
		t.Fatalf("the module holds %s after the loan and the repayment, want its original 1,000,000", got)
	}
	if got := f.state.market.TotalNHBBorrowed; got.Sign() != 0 {
		t.Fatalf("the pool still shows %s borrowed", got)
	}
	if f.state.market.TotalNHBSupplied.Cmp(big.NewInt(1_000_000)) != 0 {
		t.Fatalf("the suppliers' total is %s, want 1,000,000", f.state.market.TotalNHBSupplied)
	}
}

// The fee is part of what the borrower owes, so the limit checks count it: a draw
// that fits the collateral on its own is refused when its fee takes it over.
func TestDeveloperFeeCountsTowardTheBorrowLimit(t *testing.T) {
	for _, fixes := range []bool{false, true} {
		f := newDevFeeFixture(t, fixes)
		// 1,650,000 of collateral at a 60% cap: room for exactly 990,000 of debt.
		f.state.users[f.state.key(f.borrower)].CollateralZNHB = big.NewInt(1_650_000)
		if _, err := f.engine.Borrow(f.borrower, big.NewInt(990_000), f.collector, 100); !errors.Is(err, errMaxLTVExceeded) && !errors.Is(err, errHealthCheckFailed) {
			t.Fatalf("fixes=%v: a draw of 990,000 with its 9,900 fee is over the cap and must be refused, got %v", fixes, err)
		}
		g := newDevFeeFixture(t, fixes)
		g.state.users[g.state.key(g.borrower)].CollateralZNHB = big.NewInt(1_650_000)
		if _, err := g.engine.Borrow(g.borrower, big.NewInt(990_000), crypto.Address{}, 0); err != nil {
			t.Fatalf("fixes=%v: the same draw without a fee is exactly at the cap and must be accepted, got %v", fixes, err)
		}
	}
}
