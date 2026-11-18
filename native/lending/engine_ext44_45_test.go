package lending

import (
	"errors"
	"math"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// Ledger PL-EXT-44 and PL-EXT-45 (bug-bounty reports of 2026-11-18, found on the lending line, both dormant: no pool holds
// funds, the fee withdrawal transaction and the borrow caps are off at every height the chain has run).
//
// PL-EXT-44: a fixed-term loan's interest that lands in the protocol fees because the pool has no flexible supplier was
// credited to the fees without being counted in the suppliers' total, while a fee withdrawal always takes the amount out of
// that total. The withdrawal then took the total below zero, which makes the borrow rate negative and shrinks the indexes.
// PL-EXT-45: the utilisation cap measured borrowing against the flexible supply alone and read an empty supply as an idle
// pool, so a pool funded by fixed-term deposits could be borrowed down to the last unit under a 1 percent cap.

func ext44Unit(n int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(n), new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil))
}

type ext44Fixture struct {
	engine *Engine
	state  *mockEngineState
	addr   map[string]crypto.Address
}

func newExt44Fixture(t *testing.T, params RiskParameters, fixes bool) *ext44Fixture {
	t.Helper()
	module := makeAddress(crypto.NHBPrefix, 0x61)
	vault := makeAddress(crypto.ZNHBPrefix, 0x62)
	addr := map[string]crypto.Address{
		"module":    module,
		"supplier":  makeAddress(crypto.NHBPrefix, 0x63),
		"borrower":  makeAddress(crypto.NHBPrefix, 0x64),
		"depositor": makeAddress(crypto.NHBPrefix, 0x65),
		"fee":       makeAddress(crypto.NHBPrefix, 0x66),
		"flexible":  makeAddress(crypto.NHBPrefix, 0x67),
	}
	if params.MaxLTV == 0 {
		params.MaxLTV = 6_000
	}
	if params.LiquidationThreshold == 0 {
		params.LiquidationThreshold = 8_500
	}
	engine := NewEngine(module, vault, params)
	engine.SetFixedTermRateSchedule(TenureRateSchedule{30: 1_200})
	engine.SetFixedTermDepositRateSchedule(TenureRateSchedule{30: 500})
	engine.SetAccountingFixes(fixes)
	engine.SetBlockHeight(50)
	engine.SetBlockTimestamp(1_700_000_000)
	state := newMockEngineState()
	state.market = &Market{
		TotalNHBSupplied:    big.NewInt(0),
		TotalSupplyShares:   big.NewInt(0),
		TotalNHBBorrowed:    big.NewInt(0),
		SupplyIndex:         new(big.Int).Set(ray),
		BorrowIndex:         new(big.Int).Set(ray),
		LastUpdateBlock:     50,
		LastUpdateTimestamp: 1_700_000_000,
	}
	state.accounts[state.key(module)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(vault)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: ext44Unit(10_000)}
	for _, name := range []string{"supplier", "depositor", "fee", "flexible"} {
		state.accounts[state.key(addr[name])] = &types.Account{BalanceNHB: ext44Unit(1_000), BalanceZNHB: big.NewInt(0)}
	}
	state.accounts[state.key(addr["borrower"])] = &types.Account{BalanceNHB: ext44Unit(1_000), BalanceZNHB: ext44Unit(10_000)}
	for _, name := range []string{"borrower", "flexible"} {
		state.users[state.key(addr[name])] = &UserAccount{
			Address: addr[name], CollateralZNHB: ext44Unit(5_000), SupplyShares: big.NewInt(0), DebtNHB: big.NewInt(0), ScaledDebt: big.NewInt(0),
		}
	}
	engine.SetState(state)
	engine.SetPoolID("default")
	return &ext44Fixture{engine: engine, state: state, addr: addr}
}

func ext44ID(b byte) [32]byte {
	var id [32]byte
	id[0] = b
	return id
}

// feeWithNoFlexibleSupplier reaches the state of the report: 100 units supplied, a 100-unit fixed-term loan, a 200-unit
// fixed-term deposit (5 percent, owed 10), the flexible supplier withdraws everything, the loan is repaid with its 12 units
// of interest: 10 top up the deposit reserve and the remaining 2 land in the protocol fees with nobody to credit.
func feeWithNoFlexibleSupplier(t *testing.T, fixes bool) *ext44Fixture {
	t.Helper()
	f := newExt44Fixture(t, RiskParameters{}, fixes)
	shares, err := f.engine.Supply(f.addr["supplier"], ext44Unit(100))
	if err != nil {
		t.Fatalf("supply: %v", err)
	}
	if _, err := f.engine.BorrowFixedTerm(f.addr["borrower"], ext44ID(1), 30, ext44Unit(100)); err != nil {
		t.Fatalf("fixed-term borrow: %v", err)
	}
	if _, err := f.engine.SupplyFixedTerm(f.addr["depositor"], ext44ID(2), 30, ext44Unit(200), FixedTermDepositPayoutLumpSumAtMaturity); err != nil {
		t.Fatalf("fixed-term deposit: %v", err)
	}
	f.engine.SetBlockHeight(60)
	if _, err := f.engine.Withdraw(f.addr["supplier"], shares); err != nil {
		t.Fatalf("the flexible supplier withdraws: %v", err)
	}
	if _, err := f.engine.RepayFixedTerm(f.addr["borrower"], ext44Unit(112)); err != nil {
		t.Fatalf("repay the fixed-term loan: %v", err)
	}
	if got, want := f.state.fees.ProtocolFeesWei, ext44Unit(2); got == nil || got.Cmp(want) != 0 {
		t.Fatalf("the interest left over after the deposit reserve is %v, want %s", got, want)
	}
	return f
}

func TestAProtocolFeeWithdrawalNeverTakesTheSuppliedTotalBelowZeroFromTheGate(t *testing.T) {
	f := feeWithNoFlexibleSupplier(t, true)
	if got := f.state.market.TotalNHBSupplied; got.Cmp(ext44Unit(2)) != 0 {
		t.Fatalf("the fee is cash the pool holds and is counted in the suppliers' total: got %s, want 2 units", got)
	}
	if _, err := f.engine.WithdrawProtocolFees(f.addr["fee"], ext44Unit(2)); err != nil {
		t.Fatalf("withdraw the fee: %v", err)
	}
	if got := f.state.market.TotalNHBSupplied; got.Sign() != 0 {
		t.Fatalf("the suppliers' total after the fee left is %s, want 0", got)
	}
	if got := f.state.accounts[f.state.key(f.addr["fee"])].BalanceNHB; got.Cmp(ext44Unit(1_002)) != 0 {
		t.Fatalf("the recipient holds %s, want 1002 units", got)
	}
}

func TestBeforeTheGateAProtocolFeeWithdrawalStillTakesTheSuppliedTotalBelowZero(t *testing.T) {
	// The recorded behaviour of every earlier release, pinned so a change to it is deliberate.
	f := feeWithNoFlexibleSupplier(t, false)
	if got := f.state.market.TotalNHBSupplied; got.Sign() != 0 {
		t.Fatalf("before the gate the fallback fee is not counted in the total: got %s, want 0", got)
	}
	if _, err := f.engine.WithdrawProtocolFees(f.addr["fee"], ext44Unit(2)); err != nil {
		t.Fatalf("withdraw the fee: %v", err)
	}
	if got := f.state.market.TotalNHBSupplied; got.Cmp(new(big.Int).Neg(ext44Unit(2))) != 0 {
		t.Fatalf("before the gate the total goes to -2 units, got %s", got)
	}
}

func TestAFeeNeverCountedInTheTotalCannotTakeItBelowZeroFromTheGate(t *testing.T) {
	// A fee credited before the gate has no matching amount in the total; the withdrawal after the gate floors at zero.
	for _, fixes := range []bool{true, false} {
		f := newExt44Fixture(t, RiskParameters{}, fixes)
		f.state.market.TotalFixedTermDepositPrincipalWei = ext44Unit(100)
		f.state.accounts[f.state.key(f.addr["module"])].BalanceNHB = ext44Unit(100)
		f.state.fees = &FeeAccrual{ProtocolFeesWei: ext44Unit(5), DeveloperFeesWei: big.NewInt(0)}
		if _, err := f.engine.WithdrawProtocolFees(f.addr["fee"], ext44Unit(5)); err != nil {
			t.Fatalf("fixes=%v: withdraw: %v", fixes, err)
		}
		got := f.state.market.TotalNHBSupplied
		if fixes && got.Sign() != 0 {
			t.Fatalf("from the gate the total floors at zero: got %s", got)
		}
		if !fixes && got.Cmp(new(big.Int).Neg(ext44Unit(5))) != 0 {
			t.Fatalf("before the gate the total is -5 units: got %s", got)
		}
	}
}

func TestAWithdrawalOfTheWholeTotalStillLeavesExactlyZero(t *testing.T) {
	// The floor changes nothing when the total covers the amount.
	f := newExt44Fixture(t, RiskParameters{}, true)
	f.state.market.TotalNHBSupplied = ext44Unit(5)
	f.state.accounts[f.state.key(f.addr["module"])].BalanceNHB = ext44Unit(100)
	f.state.fees = &FeeAccrual{ProtocolFeesWei: ext44Unit(3), DeveloperFeesWei: big.NewInt(0)}
	if _, err := f.engine.WithdrawProtocolFees(f.addr["fee"], ext44Unit(3)); err != nil {
		t.Fatalf("withdraw: %v", err)
	}
	if got := f.state.market.TotalNHBSupplied; got.Cmp(ext44Unit(2)) != 0 {
		t.Fatalf("a total that covers the amount is reduced by it: got %s, want 2 units", got)
	}
}

func TestTheUtilisationCapCountsFixedTermDepositPrincipal(t *testing.T) {
	// 1 percent cap, no flexible supply, 5 units held as fixed-term deposits.
	f := newExt44Fixture(t, RiskParameters{BorrowCaps: BorrowCaps{UtilisationBps: 100}}, true)
	f.state.market.TotalFixedTermDepositPrincipalWei = ext44Unit(5)
	f.state.accounts[f.state.key(f.addr["module"])].BalanceNHB = ext44Unit(5)
	if _, err := f.engine.Borrow(f.addr["flexible"], ext44Unit(4), crypto.Address{}, 0); !errors.Is(err, errBorrowCapUtilisation) {
		t.Fatalf("a borrow of 80 percent of the deposits under a 1 percent cap: got %v, want the utilisation cap", err)
	}
	// 0.04 units is 0.8 percent of the deposits: inside the cap.
	if _, err := f.engine.Borrow(f.addr["flexible"], new(big.Int).Div(ext44Unit(4), big.NewInt(100)), crypto.Address{}, 0); err != nil {
		t.Fatalf("a borrow inside the cap was refused: %v", err)
	}
}

func TestTheUtilisationCapCountsFlexibleSupplyAndDepositsTogether(t *testing.T) {
	// 5 flexible + 5 deposits = 10 held; a 50 percent cap allows 5 and refuses more than 5.
	f := newExt44Fixture(t, RiskParameters{BorrowCaps: BorrowCaps{UtilisationBps: 5_000}}, true)
	f.state.market.TotalNHBSupplied = ext44Unit(5)
	f.state.market.TotalSupplyShares = ext44Unit(5)
	f.state.market.TotalFixedTermDepositPrincipalWei = ext44Unit(5)
	f.state.accounts[f.state.key(f.addr["module"])].BalanceNHB = ext44Unit(10)
	// 5.01 units is 50.1 percent of 10: over the cap by 10 basis points (the cap compares whole basis points).
	over := new(big.Int).Add(ext44Unit(5), new(big.Int).Div(ext44Unit(1), big.NewInt(100)))
	if _, err := f.engine.Borrow(f.addr["flexible"], over, crypto.Address{}, 0); !errors.Is(err, errBorrowCapUtilisation) {
		t.Fatalf("10 basis points over half of what lenders hold: got %v, want the utilisation cap", err)
	}
	if _, err := f.engine.Borrow(f.addr["flexible"], ext44Unit(5), crypto.Address{}, 0); err != nil {
		t.Fatalf("exactly half of what lenders hold was refused: %v", err)
	}
}

func TestCapUtilisationTable(t *testing.T) {
	cases := []struct {
		name                     string
		borrowed, supplied, deps *big.Int
		want                     uint64
	}{
		{"no debt", big.NewInt(0), big.NewInt(100), nil, 0},
		{"nil debt", nil, big.NewInt(100), nil, 0},
		{"supply only", big.NewInt(50), big.NewInt(100), nil, 5_000},
		{"deposits only", big.NewInt(50), big.NewInt(0), big.NewInt(100), 5_000},
		{"both", big.NewInt(50), big.NewInt(100), big.NewInt(100), 2_500},
		{"debt with nothing held", big.NewInt(1), big.NewInt(0), big.NewInt(0), math.MaxUint64},
		{"debt with a negative total", big.NewInt(1), big.NewInt(-2), nil, math.MaxUint64},
		{"a negative total next to deposits", big.NewInt(50), big.NewInt(-2), big.NewInt(100), 5_000},
	}
	for _, c := range cases {
		if got := capUtilisation(c.borrowed, c.supplied, c.deps); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}
