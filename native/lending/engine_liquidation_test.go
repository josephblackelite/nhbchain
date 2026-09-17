package lending

import (
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestLiquidateRoutesCollateral(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x10)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x11)
	liquidator := makeAddress(crypto.NHBPrefix, 0x20)
	borrower := makeAddress(crypto.NHBPrefix, 0x21)
	developer := makeAddress(crypto.ZNHBPrefix, 0x22)
	protocol := makeAddress(crypto.ZNHBPrefix, 0x23)

	engine := NewEngine(moduleAddr, collateralAddr, RiskParameters{
		LiquidationThreshold: 7500,
		LiquidationBonus:     1000,
	})
	engine.SetCollateralRouting(CollateralRouting{
		LiquidatorBps:   7000,
		DeveloperBps:    2000,
		DeveloperTarget: developer,
		ProtocolBps:     1000,
		ProtocolTarget:  protocol,
	})
	engine.SetPoolID("default")

	state := newMockEngineState()
	state.market = &Market{
		PoolID:           "default",
		TotalNHBSupplied: big.NewInt(10_000),
		TotalNHBBorrowed: big.NewInt(800),
		SupplyIndex:      new(big.Int).Set(ray),
		BorrowIndex:      new(big.Int).Set(ray),
	}
	state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(0)}
	state.accounts[state.key(collateralAddr)] = &types.Account{BalanceZNHB: big.NewInt(10_000)}
	state.accounts[state.key(liquidator)] = &types.Account{BalanceNHB: big.NewInt(5_000), BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(borrower)] = &types.Account{BalanceNHB: big.NewInt(0)}
	state.accounts[state.key(developer)] = &types.Account{BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(protocol)] = &types.Account{BalanceZNHB: big.NewInt(0)}

	borrowerAccount := &UserAccount{
		Address:        borrower,
		CollateralZNHB: big.NewInt(1_000),
		DebtNHB:        big.NewInt(800),
		ScaledDebt:     big.NewInt(800),
	}
	state.users[state.key(borrower)] = borrowerAccount

	engine.SetState(state)

	repaid, seized, err := engine.Liquidate(liquidator, borrower)
	if err != nil {
		t.Fatalf("liquidate: %v", err)
	}
	if repaid.Cmp(big.NewInt(800)) != 0 {
		t.Fatalf("unexpected repay amount: %s", repaid)
	}
	if seized.Cmp(big.NewInt(880)) != 0 {
		t.Fatalf("unexpected seized amount: %s", seized)
	}

	liquidatorAcc := state.accounts[state.key(liquidator)]
	if liquidatorAcc.BalanceZNHB.Cmp(big.NewInt(616)) != 0 {
		t.Fatalf("unexpected liquidator collateral: %s", liquidatorAcc.BalanceZNHB)
	}
	developerAcc := state.accounts[state.key(developer)]
	if developerAcc.BalanceZNHB.Cmp(big.NewInt(176)) != 0 {
		t.Fatalf("unexpected developer collateral: %s", developerAcc.BalanceZNHB)
	}
	protocolAcc := state.accounts[state.key(protocol)]
	if protocolAcc.BalanceZNHB.Cmp(big.NewInt(88)) != 0 {
		t.Fatalf("unexpected protocol collateral: %s", protocolAcc.BalanceZNHB)
	}

	borrowerUser := state.users[state.key(borrower)]
	if borrowerUser.DebtNHB.Sign() != 0 || borrowerUser.CollateralZNHB.Cmp(big.NewInt(120)) != 0 {
		t.Fatalf("unexpected borrower state: debt=%s collateral=%s", borrowerUser.DebtNHB, borrowerUser.CollateralZNHB)
	}
	if state.market.TotalNHBBorrowed.Sign() != 0 {
		t.Fatalf("expected borrowed total to reset, got %s", state.market.TotalNHBBorrowed)
	}
}

func TestLiquidateRoutingValidation(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x30)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x31)
	liquidator := makeAddress(crypto.NHBPrefix, 0x32)
	borrower := makeAddress(crypto.NHBPrefix, 0x33)
	developer := makeAddress(crypto.ZNHBPrefix, 0x34)

	setup := func() *Engine {
		engine := NewEngine(moduleAddr, collateralAddr, RiskParameters{
			LiquidationThreshold: 7000,
			LiquidationBonus:     500,
		})
		engine.SetPoolID("default")

		state := newMockEngineState()
		state.market = &Market{
			PoolID:           "default",
			TotalNHBSupplied: big.NewInt(1_000),
			TotalNHBBorrowed: big.NewInt(500),
			SupplyIndex:      new(big.Int).Set(ray),
			BorrowIndex:      new(big.Int).Set(ray),
		}
		state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(0)}
		state.accounts[state.key(collateralAddr)] = &types.Account{BalanceZNHB: big.NewInt(5_000)}
		state.accounts[state.key(liquidator)] = &types.Account{BalanceNHB: big.NewInt(600)}
		state.accounts[state.key(borrower)] = &types.Account{}
		state.accounts[state.key(developer)] = &types.Account{BalanceZNHB: big.NewInt(0)}
		state.users[state.key(borrower)] = &UserAccount{
			Address:        borrower,
			CollateralZNHB: big.NewInt(500),
			DebtNHB:        big.NewInt(500),
			ScaledDebt:     big.NewInt(500),
		}
		engine.SetState(state)
		return engine
	}

	engine := setup()
	engine.SetCollateralRouting(CollateralRouting{DeveloperBps: 3000, ProtocolBps: 8000, DeveloperTarget: developer})
	if _, _, err := engine.Liquidate(liquidator, borrower); err != errCollateralRoutingBps {
		t.Fatalf("expected collateral routing bps error, got %v", err)
	}

	engine = setup()
	engine.SetCollateralRouting(CollateralRouting{DeveloperBps: 1000})
	if _, _, err := engine.Liquidate(liquidator, borrower); err != errDeveloperCollateral {
		t.Fatalf("expected developer collateral error, got %v", err)
	}
}
