package lending

import (
	"errors"
	"math/big"
	"reflect"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// copyingAccountState mirrors the production state adapter, which hands out a
// fresh account object on every read: a stale copy persisted after a fresher
// one silently overwrites it.
type copyingAccountState struct {
	*mockEngineState
}

func copyAccount(acc *types.Account) *types.Account {
	return &types.Account{
		BalanceNHB:  new(big.Int).Set(acc.BalanceNHB),
		BalanceZNHB: new(big.Int).Set(acc.BalanceZNHB),
	}
}

func (s copyingAccountState) GetAccount(addr crypto.Address) (*types.Account, error) {
	acc, err := s.mockEngineState.GetAccount(addr)
	if err != nil {
		return nil, err
	}
	return copyAccount(acc), nil
}

func (s copyingAccountState) PutAccount(addr crypto.Address, acc *types.Account) error {
	return s.mockEngineState.PutAccount(addr, copyAccount(acc))
}

type liqFixture struct {
	engine     *Engine
	state      *mockEngineState
	moduleAddr crypto.Address
	vaultAddr  crypto.Address
	liquidator crypto.Address
	borrower   crypto.Address
}

// priceWei builds an OracleMedianWei from a num/denom NHB-per-ZNHB price.
func priceWei(num, denom int64) *big.Int {
	v := new(big.Int).Mul(big.NewInt(num), weiPerToken)
	return v.Quo(v, big.NewInt(denom))
}

func newLiqFixture(t *testing.T, price *big.Int, threshold, bonus uint64, collateral, debt int64, routing CollateralRouting) *liqFixture {
	t.Helper()
	f := &liqFixture{
		moduleAddr: makeAddress(crypto.NHBPrefix, 0x10),
		vaultAddr:  makeAddress(crypto.ZNHBPrefix, 0x11),
		liquidator: makeAddress(crypto.NHBPrefix, 0x20),
		borrower:   makeAddress(crypto.NHBPrefix, 0x21),
	}
	f.engine = NewEngine(f.moduleAddr, f.vaultAddr, RiskParameters{
		LiquidationThreshold: threshold,
		LiquidationBonus:     bonus,
	})
	f.engine.SetCollateralRouting(routing)
	f.engine.SetPoolID("default")

	f.state = newMockEngineState()
	f.state.market = &Market{
		PoolID:           "default",
		TotalNHBSupplied: big.NewInt(1_000_000),
		TotalNHBBorrowed: big.NewInt(debt),
		SupplyIndex:      new(big.Int).Set(ray),
		BorrowIndex:      new(big.Int).Set(ray),
	}
	if price != nil {
		f.state.market.OracleMedianWei = new(big.Int).Set(price)
	}
	f.state.accounts[f.state.key(f.moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(1_000_000)}
	f.state.accounts[f.state.key(f.vaultAddr)] = &types.Account{BalanceZNHB: big.NewInt(collateral)}
	f.state.accounts[f.state.key(f.liquidator)] = &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0)}
	f.state.accounts[f.state.key(f.borrower)] = &types.Account{BalanceNHB: big.NewInt(0)}
	f.state.users[f.state.key(f.borrower)] = &UserAccount{
		Address:        f.borrower,
		CollateralZNHB: big.NewInt(collateral),
		DebtNHB:        big.NewInt(debt),
		ScaledDebt:     big.NewInt(debt),
	}
	f.engine.SetState(f.state)
	return f
}

// supply sums both balances over the distinct addresses given.
func (f *liqFixture) supply(addrs ...crypto.Address) (nhb, znhb *big.Int) {
	nhb, znhb = big.NewInt(0), big.NewInt(0)
	seen := map[string]bool{}
	for _, a := range addrs {
		k := f.state.key(a)
		if seen[k] {
			continue
		}
		seen[k] = true
		acc := f.state.accounts[k]
		if acc == nil {
			continue
		}
		if acc.BalanceNHB != nil {
			nhb.Add(nhb, acc.BalanceNHB)
		}
		if acc.BalanceZNHB != nil {
			znhb.Add(znhb, acc.BalanceZNHB)
		}
	}
	return nhb, znhb
}

// balances snapshots the NHB and ZNHB balance of every distinct address given.
func (f *liqFixture) balances(addrs ...crypto.Address) map[string][2]string {
	out := map[string][2]string{}
	for _, a := range addrs {
		k := f.state.key(a)
		acc := f.state.accounts[k]
		if acc == nil {
			continue
		}
		nhb, znhb := "0", "0"
		if acc.BalanceNHB != nil {
			nhb = acc.BalanceNHB.String()
		}
		if acc.BalanceZNHB != nil {
			znhb = acc.BalanceZNHB.String()
		}
		out[k] = [2]string{nhb, znhb}
	}
	return out
}

func TestLiquidateSeizesAtOraclePrice(t *testing.T) {
	cases := []struct {
		name       string
		price      *big.Int
		threshold  uint64
		bonus      uint64
		collateral int64
		debt       int64
		wantSeize  int64
	}{
		// 1 ZNHB = 0.3 NHB: value 300, debt 260 is over the 75% line.
		// 260 NHB * 1.05 / 0.3 = 910 ZNHB (raw 1:1 would seize 273).
		{"price below one with bonus", priceWei(3, 10), 7500, 500, 1000, 260, 910},
		// 1 ZNHB = 4 NHB: value 400, debt 320 is over the 75% line.
		// 320 NHB * 1.05 / 4 = 84 ZNHB (raw 1:1 would seize 336).
		{"price above one with bonus", priceWei(4, 1), 7500, 500, 100, 320, 84},
		// The live reference price, no bonus: 1 ZNHB = 0.05 NHB.
		// value 50, debt 45 > 37.5. 45 / 0.05 = 900 ZNHB.
		{"live price no bonus", priceWei(1, 20), 7500, 0, 1000, 45, 900},
		// 601 * 1.05 / 0.7 = 901.5 rounds down.
		{"rounds down", priceWei(7, 10), 7500, 500, 1000, 601, 901},
		// Without an oracle submission the valuation falls back to 1:1, the
		// same as the eligibility check.
		{"no price is one to one", nil, 7500, 500, 1000, 800, 840},
		{"zero price is one to one", big.NewInt(0), 7500, 500, 1000, 800, 840},
		// Bonus-inclusive value exceeds the collateral: seizure is capped.
		{"capped at collateral", priceWei(3, 10), 7500, 500, 1000, 290, 1000},
		{"collateral smaller than seizure", priceWei(1, 20), 7500, 0, 1000, 60, 1000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLiqFixture(t, tc.price, tc.threshold, tc.bonus, tc.collateral, tc.debt, CollateralRouting{})
			nhb0, znhb0 := f.supply(f.moduleAddr, f.vaultAddr, f.liquidator, f.borrower)

			repaid, seized, err := f.engine.Liquidate(f.liquidator, f.borrower)
			if err != nil {
				t.Fatalf("liquidate: %v", err)
			}
			if repaid.Cmp(big.NewInt(tc.debt)) != 0 {
				t.Fatalf("repaid = %s, want %d", repaid, tc.debt)
			}
			if seized.Cmp(big.NewInt(tc.wantSeize)) != 0 {
				t.Fatalf("seized = %s, want %d", seized, tc.wantSeize)
			}
			liq := f.state.accounts[f.state.key(f.liquidator)]
			if liq.BalanceZNHB.Cmp(big.NewInt(tc.wantSeize)) != 0 {
				t.Fatalf("liquidator ZNHB = %s, want %d", liq.BalanceZNHB, tc.wantSeize)
			}
			user := f.state.users[f.state.key(f.borrower)]
			if user.DebtNHB.Sign() != 0 || user.CollateralZNHB.Cmp(big.NewInt(tc.collateral-tc.wantSeize)) != 0 {
				t.Fatalf("borrower debt=%s collateral=%s", user.DebtNHB, user.CollateralZNHB)
			}
			nhb1, znhb1 := f.supply(f.moduleAddr, f.vaultAddr, f.liquidator, f.borrower)
			if nhb0.Cmp(nhb1) != 0 || znhb0.Cmp(znhb1) != 0 {
				t.Fatalf("supply changed: NHB %s -> %s, ZNHB %s -> %s", nhb0, nhb1, znhb0, znhb1)
			}
		})
	}
}

func TestLiquidateSeizedValueTracksEligibilityValue(t *testing.T) {
	// The seized collateral, valued with the same oracle conversion the
	// eligibility check uses, must never exceed the bonus-inclusive debt and
	// must be within rounding of it when the collateral is not capped.
	prices := []*big.Int{priceWei(3, 10), priceWei(4, 1), priceWei(1, 20), priceWei(7, 3), priceWei(123456789, 1000)}
	for _, price := range prices {
		f := newLiqFixture(t, price, 9000, 300, 5_000_000, 1, CollateralRouting{})
		market := f.state.market
		value := OracleAdjustedCollateralValue(market, big.NewInt(5_000_000))
		// 95% of the collateral value: above the 90% line, below value/1.03.
		debt := new(big.Int).Mul(value, big.NewInt(9500))
		debt.Quo(debt, big.NewInt(10_000))
		if debt.Sign() == 0 {
			t.Fatalf("degenerate debt for price %s", price)
		}
		user := f.state.users[f.state.key(f.borrower)]
		user.DebtNHB = new(big.Int).Set(debt)
		user.ScaledDebt = new(big.Int).Set(debt)
		market.TotalNHBBorrowed = new(big.Int).Set(debt)
		f.state.accounts[f.state.key(f.liquidator)].BalanceNHB = new(big.Int).Mul(debt, big.NewInt(2))

		_, seized, err := f.engine.Liquidate(f.liquidator, f.borrower)
		if err != nil {
			t.Fatalf("price %s: liquidate: %v", price, err)
		}
		if seized.Cmp(big.NewInt(5_000_000)) >= 0 {
			t.Fatalf("price %s: seizure was capped, test premise broken", price)
		}
		got := new(big.Int).Mul(seized, price)
		got.Quo(got, weiPerToken)
		want := new(big.Int).Mul(debt, big.NewInt(10_300))
		want.Quo(want, big.NewInt(10_000))
		if got.Cmp(want) > 0 {
			t.Fatalf("price %s: seized value %s exceeds bonus-inclusive debt %s", price, got, want)
		}
		// One ZNHB wei is worth price/1e18 NHB wei; allow that plus rounding.
		slack := new(big.Int).Quo(price, weiPerToken)
		slack.Add(slack, big.NewInt(2))
		if new(big.Int).Sub(want, got).Cmp(slack) > 0 {
			t.Fatalf("price %s: seized value %s far below bonus-inclusive debt %s", price, got, want)
		}
	}
}

func TestLiquidateHealthThresholdBoundary(t *testing.T) {
	// Price 0.5, 1000 ZNHB: value 500. At an 80% threshold a debt of 400 is
	// exactly on the line and healthy; 401 is liquidatable.
	f := newLiqFixture(t, priceWei(1, 2), 8000, 0, 1000, 400, CollateralRouting{})
	if _, _, err := f.engine.Liquidate(f.liquidator, f.borrower); err != errNotLiquidatable {
		t.Fatalf("debt exactly at threshold: err = %v, want %v", err, errNotLiquidatable)
	}

	f = newLiqFixture(t, priceWei(1, 2), 8000, 0, 1000, 401, CollateralRouting{})
	repaid, seized, err := f.engine.Liquidate(f.liquidator, f.borrower)
	if err != nil {
		t.Fatalf("debt one over threshold: %v", err)
	}
	// 401 NHB / 0.5 = 802 ZNHB.
	if repaid.Cmp(big.NewInt(401)) != 0 || seized.Cmp(big.NewInt(802)) != 0 {
		t.Fatalf("repaid=%s seized=%s, want 401 and 802", repaid, seized)
	}
}

func TestLiquidateRoutingConvertsBeforeSplit(t *testing.T) {
	developer := makeAddress(crypto.ZNHBPrefix, 0x22)
	protocol := makeAddress(crypto.ZNHBPrefix, 0x23)
	routing := CollateralRouting{
		LiquidatorBps: 7000, DeveloperBps: 2000, DeveloperTarget: developer,
		ProtocolBps: 1000, ProtocolTarget: protocol,
	}
	// 1 ZNHB = 0.5 NHB, debt 401 > 400: seize 401*1.1/0.5 = 882.2 -> 882.
	f := newLiqFixture(t, priceWei(1, 2), 8000, 1000, 1000, 401, routing)
	f.state.accounts[f.state.key(developer)] = &types.Account{BalanceZNHB: big.NewInt(0)}
	f.state.accounts[f.state.key(protocol)] = &types.Account{BalanceZNHB: big.NewInt(0)}
	nhb0, znhb0 := f.supply(f.moduleAddr, f.vaultAddr, f.liquidator, f.borrower, developer, protocol)

	_, seized, err := f.engine.Liquidate(f.liquidator, f.borrower)
	if err != nil {
		t.Fatalf("liquidate: %v", err)
	}
	if seized.Cmp(big.NewInt(882)) != 0 {
		t.Fatalf("seized = %s, want 882", seized)
	}
	// developer 176, protocol 88, liquidator the remainder 618.
	want := map[string]int64{
		f.state.key(f.liquidator): 618,
		f.state.key(developer):    176,
		f.state.key(protocol):     88,
	}
	for key, w := range want {
		if got := f.state.accounts[key].BalanceZNHB; got.Cmp(big.NewInt(w)) != 0 {
			t.Fatalf("ZNHB balance = %s, want %d", got, w)
		}
	}
	nhb1, znhb1 := f.supply(f.moduleAddr, f.vaultAddr, f.liquidator, f.borrower, developer, protocol)
	if nhb0.Cmp(nhb1) != 0 || znhb0.Cmp(znhb1) != 0 {
		t.Fatalf("supply changed: NHB %s -> %s, ZNHB %s -> %s", nhb0, nhb1, znhb0, znhb1)
	}
}

// TestLiquidateSharedAddressesConserveSupply covers the ways the roles in a
// liquidation can resolve to one address, against a state adapter that returns
// copies like the production one.
func TestLiquidateSharedAddressesConserveSupply(t *testing.T) {
	fresh1 := makeAddress(crypto.ZNHBPrefix, 0x22)
	fresh2 := makeAddress(crypto.ZNHBPrefix, 0x23)
	cases := []struct {
		name          string
		dev, prot     func(f *liqFixture) crypto.Address
		liquidatorIsB bool
	}{
		{"developer is liquidator", func(f *liqFixture) crypto.Address { return f.liquidator }, func(f *liqFixture) crypto.Address { return fresh2 }, false},
		{"protocol is liquidator", func(f *liqFixture) crypto.Address { return fresh1 }, func(f *liqFixture) crypto.Address { return f.liquidator }, false},
		{"developer is protocol", func(f *liqFixture) crypto.Address { return fresh1 }, func(f *liqFixture) crypto.Address { return fresh1 }, false},
		{"all three are the liquidator", func(f *liqFixture) crypto.Address { return f.liquidator }, func(f *liqFixture) crypto.Address { return f.liquidator }, false},
		{"developer is borrower", func(f *liqFixture) crypto.Address { return f.borrower }, func(f *liqFixture) crypto.Address { return fresh2 }, false},
		{"protocol is module", func(f *liqFixture) crypto.Address { return fresh1 }, func(f *liqFixture) crypto.Address { return f.moduleAddr }, false},
		{"developer is collateral vault", func(f *liqFixture) crypto.Address { return f.vaultAddr }, func(f *liqFixture) crypto.Address { return fresh2 }, false},
		{"liquidator is borrower", func(f *liqFixture) crypto.Address { return fresh1 }, func(f *liqFixture) crypto.Address { return fresh2 }, true},
		{"liquidator is borrower and developer", func(f *liqFixture) crypto.Address { return f.borrower }, func(f *liqFixture) crypto.Address { return fresh2 }, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLiqFixture(t, priceWei(1, 2), 8000, 1000, 1000, 401, CollateralRouting{})
			developer, protocol := tc.dev(f), tc.prot(f)
			f.engine.SetCollateralRouting(CollateralRouting{
				LiquidatorBps: 7000, DeveloperBps: 2000, DeveloperTarget: developer,
				ProtocolBps: 1000, ProtocolTarget: protocol,
			})
			for _, a := range []crypto.Address{fresh1, fresh2} {
				f.state.accounts[f.state.key(a)] = &types.Account{BalanceZNHB: big.NewInt(0)}
			}
			liquidator := f.liquidator
			if tc.liquidatorIsB {
				liquidator = f.borrower
				f.state.accounts[f.state.key(f.borrower)].BalanceNHB = big.NewInt(1_000)
			}
			f.engine.SetState(copyingAccountState{f.state})

			all := []crypto.Address{f.moduleAddr, f.vaultAddr, f.liquidator, f.borrower, fresh1, fresh2}
			nhb0, znhb0 := f.supply(all...)
			before := f.balances(all...)
			_, seized, err := f.engine.Liquidate(liquidator, f.borrower)
			if tc.liquidatorIsB {
				// A liquidator that is the borrower is refused before anything
				// is loaded, so that aliasing can never reach the ledger.
				if !errors.Is(err, ErrSelfLiquidation) {
					t.Fatalf("a borrower liquidating its own position must be refused with ErrSelfLiquidation, got %v", err)
				}
				if after := f.balances(all...); !reflect.DeepEqual(before, after) {
					t.Fatalf("a refused self-liquidation changed balances: %v -> %v", before, after)
				}
				return
			}
			if err != nil {
				t.Fatalf("liquidate: %v", err)
			}
			if seized.Cmp(big.NewInt(882)) != 0 {
				t.Fatalf("seized = %s, want 882", seized)
			}
			nhb1, znhb1 := f.supply(all...)
			if nhb0.Cmp(nhb1) != 0 {
				t.Fatalf("NHB supply changed: %s -> %s", nhb0, nhb1)
			}
			if znhb0.Cmp(znhb1) != 0 {
				t.Fatalf("ZNHB supply changed: %s -> %s", znhb0, znhb1)
			}
		})
	}
}
