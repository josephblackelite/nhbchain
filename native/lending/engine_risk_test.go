package lending

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-DC-19: the engine reads borrow caps, per-action pauses, a circuit breaker, a
// liquidation bonus and (new) supply and collateral caps, but the node never
// handed them over, so none could be switched on. These tests pin what the engine
// does once it has them, on every path that moves a pool's money.

type riskFixture struct {
	engine     *Engine
	state      *mockEngineState
	module     crypto.Address
	vault      crypto.Address
	supplier   crypto.Address
	borrower   crypto.Address
	liquidator crypto.Address
}

func newRiskFixture(t *testing.T, params RiskParameters) *riskFixture {
	t.Helper()
	f := &riskFixture{
		module:     makeAddress(crypto.NHBPrefix, 0x41),
		vault:      makeAddress(crypto.ZNHBPrefix, 0x42),
		supplier:   makeAddress(crypto.NHBPrefix, 0x43),
		borrower:   makeAddress(crypto.NHBPrefix, 0x44),
		liquidator: makeAddress(crypto.NHBPrefix, 0x45),
	}
	if params.MaxLTV == 0 {
		params.MaxLTV = 6_000
	}
	if params.LiquidationThreshold == 0 {
		params.LiquidationThreshold = 8_500
	}
	f.engine = NewEngine(f.module, f.vault, params)
	f.engine.SetFixedTermRateSchedule(TenureRateSchedule{30: 1_200})
	f.engine.SetFixedTermDepositRateSchedule(TenureRateSchedule{30: 1_000})
	f.engine.SetBlockHeight(50)
	f.engine.SetBlockTimestamp(1_700_000_000)
	f.state = newMockEngineState()
	f.state.market = &Market{
		TotalNHBSupplied:    big.NewInt(1_000_000),
		TotalSupplyShares:   big.NewInt(1_000_000),
		TotalNHBBorrowed:    big.NewInt(0),
		SupplyIndex:         new(big.Int).Set(ray),
		BorrowIndex:         new(big.Int).Set(ray),
		LastUpdateBlock:     50,
		LastUpdateTimestamp: 1_700_000_000,
	}
	f.state.accounts[f.state.key(f.module)] = &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0)}
	f.state.accounts[f.state.key(f.vault)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(2_000_000)}
	f.state.accounts[f.state.key(f.supplier)] = &types.Account{BalanceNHB: big.NewInt(10_000_000), BalanceZNHB: big.NewInt(0)}
	f.state.accounts[f.state.key(f.borrower)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(10_000_000)}
	f.state.accounts[f.state.key(f.liquidator)] = &types.Account{BalanceNHB: big.NewInt(10_000_000), BalanceZNHB: big.NewInt(0)}
	f.state.users[f.state.key(f.borrower)] = &UserAccount{
		Address: f.borrower, CollateralZNHB: big.NewInt(2_000_000), SupplyShares: big.NewInt(0), DebtNHB: big.NewInt(0), ScaledDebt: big.NewInt(0),
	}
	f.engine.SetState(f.state)
	f.engine.SetPoolID("default")
	return f
}

func bi(v int64) *big.Int { return big.NewInt(v) }

// ---- caps on borrowing: flexible and fixed-term alike -----------------------

func TestBorrowCapsApplyToFlexibleAndFixedTermBorrowing(t *testing.T) {
	type attempt func(f *riskFixture, amount int64) error
	flexible := func(f *riskFixture, amount int64) error {
		_, err := f.engine.Borrow(f.borrower, bi(amount), crypto.Address{}, 0)
		return err
	}
	fixedTerm := func(f *riskFixture, amount int64) error {
		_, err := f.engine.BorrowFixedTerm(f.borrower, [32]byte{byte(amount)}, 30, bi(amount))
		return err
	}
	for _, kind := range []struct {
		name string
		do   attempt
	}{{"flexible", flexible}, {"fixed term", fixedTerm}} {
		t.Run(kind.name+"/per block", func(t *testing.T) {
			f := newRiskFixture(t, RiskParameters{BorrowCaps: BorrowCaps{PerBlock: bi(100_000)}})
			if err := kind.do(f, 100_000); err != nil {
				t.Fatalf("a borrow exactly at the per-block cap: %v", err)
			}
			// The next borrow in the same block is over the cap. A fixed-term borrower may
			// hold one loan, so the second one is a flexible borrow by the same account.
			if _, err := f.engine.Borrow(f.borrower, bi(1), crypto.Address{}, 0); !errors.Is(err, ErrBorrowCapPerBlock) {
				t.Fatalf("a second borrow in the block over the cap: got %v, want ErrBorrowCapPerBlock", err)
			}
			f.engine.SetBlockHeight(51)
			if _, err := f.engine.Borrow(f.borrower, bi(10), crypto.Address{}, 0); err != nil {
				t.Fatalf("the cap starts again with the next block: %v", err)
			}
		})
		t.Run(kind.name+"/total", func(t *testing.T) {
			f := newRiskFixture(t, RiskParameters{BorrowCaps: BorrowCaps{Total: bi(150_000)}})
			if err := kind.do(f, 150_000); err != nil {
				t.Fatalf("a borrow exactly at the total cap: %v", err)
			}
			f.engine.SetBlockHeight(60)
			if _, err := f.engine.Borrow(f.borrower, bi(1), crypto.Address{}, 0); !errors.Is(err, ErrBorrowCapGlobal) {
				t.Fatalf("a borrow over the total cap: got %v, want ErrBorrowCapGlobal", err)
			}
		})
		t.Run(kind.name+"/utilisation", func(t *testing.T) {
			// 20% of 1,000,000 supplied.
			f := newRiskFixture(t, RiskParameters{BorrowCaps: BorrowCaps{UtilisationBps: 2_000}})
			if err := kind.do(f, 200_000); err != nil {
				t.Fatalf("a borrow exactly at the utilisation cap: %v", err)
			}
			f.engine.SetBlockHeight(60)
			if _, err := f.engine.Borrow(f.borrower, bi(10_000), crypto.Address{}, 0); !errors.Is(err, ErrBorrowCapUtilisation) {
				t.Fatalf("a borrow over the utilisation cap: got %v, want ErrBorrowCapUtilisation", err)
			}
		})
		t.Run(kind.name+"/no caps set", func(t *testing.T) {
			f := newRiskFixture(t, RiskParameters{})
			if err := kind.do(f, 400_000); err != nil {
				t.Fatalf("with no cap set the borrow stands: %v", err)
			}
		})
	}
}

// ---- pauses and the circuit breaker on every path -----------------------------

func TestActionPausesAndTheCircuitBreakerStopTheirActionsOnEveryPath(t *testing.T) {
	cases := []struct {
		name   string
		params RiskParameters
		do     func(f *riskFixture) error
		want   error
	}{
		{"supply paused: flexible supply", RiskParameters{Pauses: ActionPauses{Supply: true}},
			func(f *riskFixture) error { _, err := f.engine.Supply(f.supplier, bi(1_000)); return err }, ErrSupplyPaused},
		{"supply paused: fixed-term supply", RiskParameters{Pauses: ActionPauses{Supply: true}},
			func(f *riskFixture) error {
				_, err := f.engine.SupplyFixedTerm(f.supplier, [32]byte{1}, 30, bi(1_000), FixedTermDepositPayoutLumpSumAtMaturity)
				return err
			}, ErrSupplyPaused},
		{"borrow paused: flexible", RiskParameters{Pauses: ActionPauses{Borrow: true}},
			func(f *riskFixture) error {
				_, err := f.engine.Borrow(f.borrower, bi(10), crypto.Address{}, 0)
				return err
			}, ErrBorrowPaused},
		{"borrow paused: fixed term", RiskParameters{Pauses: ActionPauses{Borrow: true}},
			func(f *riskFixture) error {
				_, err := f.engine.BorrowFixedTerm(f.borrower, [32]byte{2}, 30, bi(10))
				return err
			}, ErrBorrowPaused},
		{"circuit breaker: flexible", RiskParameters{CircuitBreakerActive: true},
			func(f *riskFixture) error {
				_, err := f.engine.Borrow(f.borrower, bi(10), crypto.Address{}, 0)
				return err
			}, ErrBorrowPaused},
		{"circuit breaker: fixed term", RiskParameters{CircuitBreakerActive: true},
			func(f *riskFixture) error {
				_, err := f.engine.BorrowFixedTerm(f.borrower, [32]byte{3}, 30, bi(10))
				return err
			}, ErrBorrowPaused},
		{"repay paused: flexible", RiskParameters{Pauses: ActionPauses{Repay: true}},
			func(f *riskFixture) error { _, err := f.engine.Repay(f.borrower, bi(10)); return err }, ErrRepayPaused},
		{"liquidations paused", RiskParameters{Pauses: ActionPauses{Liquidate: true}},
			func(f *riskFixture) error { _, _, err := f.engine.Liquidate(f.liquidator, f.borrower); return err }, ErrLiquidationsPaused},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRiskFixture(t, tc.params)
			if err := tc.do(f); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// What a pause does not stop: repaying and withdrawing stay open while borrowing is
// stopped, so a pause never traps a borrower or a lender.
func TestABorrowPauseDoesNotTrapRepaymentOrWithdrawal(t *testing.T) {
	f := newRiskFixture(t, RiskParameters{})
	if _, err := f.engine.Borrow(f.borrower, bi(10_000), crypto.Address{}, 0); err != nil {
		t.Fatalf("borrow: %v", err)
	}
	f.state.accounts[f.state.key(f.borrower)].BalanceNHB = bi(50_000)
	f.engine.params.Pauses.Borrow = true
	f.engine.params.CircuitBreakerActive = true
	f.engine.SetBlockHeight(60)
	if _, err := f.engine.Repay(f.borrower, bi(10_000)); err != nil {
		t.Fatalf("repay while borrowing is paused: %v", err)
	}
	if err := f.engine.WithdrawCollateral(f.borrower, bi(1_000)); err != nil {
		t.Fatalf("collateral withdrawal while borrowing is paused: %v", err)
	}
}

// ---- supply and collateral caps ------------------------------------------------

func TestSupplyCapCountsFlexibleSupplyAndFixedTermPrincipal(t *testing.T) {
	// 1,000,000 supplied already; the cap leaves room for 500,000 more.
	f := newRiskFixture(t, RiskParameters{MaxTotalSupply: bi(1_500_000)})
	f.state.market.TotalFixedTermLoanInterestReceivableWei = bi(10_000_000)
	if _, err := f.engine.Supply(f.supplier, bi(300_000)); err != nil {
		t.Fatalf("a supply inside the cap: %v", err)
	}
	if _, err := f.engine.SupplyFixedTerm(f.supplier, [32]byte{5}, 30, bi(200_000), FixedTermDepositPayoutLumpSumAtMaturity); err != nil {
		t.Fatalf("a fixed-term deposit exactly filling the cap: %v", err)
	}
	if _, err := f.engine.Supply(f.supplier, bi(1)); !errors.Is(err, ErrSupplyCapExceeded) {
		t.Fatalf("flexible supply over the cap, counting the fixed-term principal: got %v, want ErrSupplyCapExceeded", err)
	}
	if _, err := f.engine.SupplyFixedTerm(f.supplier, [32]byte{6}, 30, bi(1), FixedTermDepositPayoutLumpSumAtMaturity); !errors.Is(err, ErrSupplyCapExceeded) {
		t.Fatalf("fixed-term deposit over the cap: got %v, want ErrSupplyCapExceeded", err)
	}
}

func TestCollateralCapCountsTheWholeVault(t *testing.T) {
	// The vault holds 2,000,000 already.
	f := newRiskFixture(t, RiskParameters{MaxTotalCollateral: bi(2_500_000)})
	if err := f.engine.DepositCollateral(f.borrower, bi(500_000)); err != nil {
		t.Fatalf("a deposit exactly filling the cap: %v", err)
	}
	if err := f.engine.DepositCollateral(f.borrower, bi(1)); !errors.Is(err, ErrCollateralCapExceeded) {
		t.Fatalf("a deposit over the cap: got %v, want ErrCollateralCapExceeded", err)
	}
	// Withdrawing is never blocked by the cap.
	if err := f.engine.WithdrawCollateral(f.borrower, bi(100_000)); err != nil {
		t.Fatalf("withdrawal: %v", err)
	}
}

func TestSupplyAndCollateralWithNoCapAreUnbounded(t *testing.T) {
	f := newRiskFixture(t, RiskParameters{})
	if _, err := f.engine.Supply(f.supplier, bi(9_000_000)); err != nil {
		t.Fatalf("unbounded supply: %v", err)
	}
	if err := f.engine.DepositCollateral(f.borrower, bi(9_000_000)); err != nil {
		t.Fatalf("unbounded collateral: %v", err)
	}
}

// ---- the liquidation bonus ------------------------------------------------------

func TestTheLiquidationBonusIsPaidInCollateralAndNothingWithoutIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		bonus uint64
		seize int64
	}{
		{"no bonus", 0, 1_800_000},
		{"ten percent", 1_000, 1_980_000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRiskFixture(t, RiskParameters{LiquidationBonus: tc.bonus})
			// 900,000 of debt against 2,000,000 units of collateral at 0.5 NHB per ZNHB: the
			// collateral is worth 1,000,000, a 90% loan to value, over the 85% threshold.
			f.state.market.OracleMedianWei = big.NewInt(500_000_000_000_000_000)
			f.state.market.OraclePrevMedianWei = big.NewInt(500_000_000_000_000_000)
			f.state.market.OracleUpdatedBlock = 50
			f.state.market.TotalNHBBorrowed = bi(900_000)
			u := f.state.users[f.state.key(f.borrower)]
			u.DebtNHB, u.ScaledDebt = bi(900_000), bi(900_000)
			f.state.accounts[f.state.key(f.liquidator)].BalanceNHB = bi(5_000_000)
			repaid, seized, err := f.engine.Liquidate(f.liquidator, f.borrower)
			if err != nil {
				t.Fatalf("liquidate: %v", err)
			}
			if repaid.Cmp(bi(900_000)) != 0 || seized.Cmp(bi(tc.seize)) != 0 {
				t.Fatalf("repaid %s seized %s, want 900000 and %d", repaid, seized, tc.seize)
			}
			if got := f.state.accounts[f.state.key(f.liquidator)].BalanceZNHB; got.Cmp(bi(tc.seize)) != 0 {
				t.Fatalf("the liquidator holds %s ZNHB, want the %d seized", got, tc.seize)
			}
		})
	}
}

// A loan of the fixed-term product must not slip past a cap that the flexible borrow
// honours: the fixed-term borrow itself is refused when it is over.
func TestAFixedTermBorrowOverACapIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		caps   BorrowCaps
		amount int64
		want   error
	}{
		{"per block", BorrowCaps{PerBlock: bi(100_000)}, 100_001, ErrBorrowCapPerBlock},
		{"total", BorrowCaps{Total: bi(150_000)}, 150_001, ErrBorrowCapGlobal},
		{"utilisation", BorrowCaps{UtilisationBps: 2_000}, 210_000, ErrBorrowCapUtilisation},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRiskFixture(t, RiskParameters{BorrowCaps: tc.caps})
			if _, err := f.engine.BorrowFixedTerm(f.borrower, [32]byte{9}, 30, bi(tc.amount)); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
			if f.state.market.TotalNHBBorrowed.Sign() != 0 {
				t.Fatalf("a refused loan changed the pool: borrowed %s", f.state.market.TotalNHBBorrowed)
			}
		})
	}
}
