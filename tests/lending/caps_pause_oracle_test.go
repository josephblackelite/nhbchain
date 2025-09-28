package lending_test

import (
	"math/big"
	"strings"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

func mustRay() *big.Int {
	ray, ok := new(big.Int).SetString("1000000000000000000000000000", 10)
	if !ok {
		panic("invalid ray constant")
	}
	return ray
}

func setupCapsEngine(moduleAddr, collateralAddr, borrower crypto.Address, modify func(*lending.RiskParameters)) (*lending.Engine, *mockEngineState) {
	one := mustBig("1000000000000000000")
	params := lending.RiskParameters{
		MaxLTV:               7500,
		LiquidationThreshold: 8000,
		BorrowCaps: lending.BorrowCaps{
			PerBlock: new(big.Int).Set(one),
			Total:    new(big.Int).Mul(one, big.NewInt(20)),
		},
		Oracle: lending.OracleConfig{MaxAgeBlocks: 10, MaxDeviationBps: 500},
	}
	if modify != nil {
		modify(&params)
	}
	engine := lending.NewEngine(moduleAddr, collateralAddr, params)
	engine.SetPoolID("default")

	state := newMockEngineState()
	state.market = &lending.Market{
		PoolID:              "default",
		TotalNHBSupplied:    new(big.Int).Mul(one, big.NewInt(20)),
		TotalSupplyShares:   new(big.Int).Mul(one, big.NewInt(20)),
		TotalNHBBorrowed:    big.NewInt(0),
		SupplyIndex:         mustRay(),
		BorrowIndex:         mustRay(),
		OracleMedianWei:     mustBig("100000000"),
		OraclePrevMedianWei: mustBig("100000000"),
		OracleUpdatedBlock:  5,
	}
	state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: new(big.Int).Mul(one, big.NewInt(50))}
	state.accounts[state.key(borrower)] = &types.Account{BalanceNHB: big.NewInt(0)}
	state.users[state.key(borrower)] = &lending.UserAccount{
		Address:        borrower,
		CollateralZNHB: new(big.Int).Mul(one, big.NewInt(10)),
		SupplyShares:   big.NewInt(0),
		DebtNHB:        big.NewInt(0),
		ScaledDebt:     big.NewInt(0),
	}
	return engine, state
}

func TestBorrowCapsAndOracleGuards(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x20)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x21)
	borrower := makeAddress(crypto.NHBPrefix, 0x22)
	one := mustBig("1000000000000000000")

	t.Run("per block cap", func(t *testing.T) {
		engine, state := setupCapsEngine(moduleAddr, collateralAddr, borrower, nil)
		engine.SetState(state)
		engine.SetBlockHeight(10)

		if _, err := engine.Borrow(borrower, new(big.Int).Set(one), crypto.Address{}, 0); err != nil {
			t.Fatalf("initial borrow within cap should succeed: %v", err)
		}
		if _, err := engine.Borrow(borrower, new(big.Int).Set(one), crypto.Address{}, 0); err == nil || !strings.Contains(err.Error(), "per-block cap") {
			t.Fatalf("expected per-block cap breach, got %v", err)
		}
	})

	t.Run("utilisation cap", func(t *testing.T) {
		engine, state := setupCapsEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
			p.BorrowCaps.PerBlock = nil
			p.BorrowCaps.UtilisationBps = 2000
		})
		engine.SetState(state)
		engine.SetBlockHeight(15)

		if _, err := engine.Borrow(borrower, new(big.Int).Mul(one, big.NewInt(6)), crypto.Address{}, 0); err == nil || !strings.Contains(err.Error(), "utilisation") {
			t.Fatalf("expected utilisation cap breach, got %v", err)
		}
	})

	t.Run("global cap", func(t *testing.T) {
		engine, state := setupCapsEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
			p.BorrowCaps.PerBlock = nil
			p.BorrowCaps.Total = new(big.Int).Mul(one, big.NewInt(2))
		})
		engine.SetState(state)
		engine.SetBlockHeight(12)

		if _, err := engine.Borrow(borrower, new(big.Int).Mul(one, big.NewInt(3)), crypto.Address{}, 0); err == nil || !strings.Contains(err.Error(), "global cap") {
			t.Fatalf("expected global cap breach, got %v", err)
		}
	})

	t.Run("oracle freshness blocks borrow but allows repay", func(t *testing.T) {
		engine, state := setupCapsEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
			p.BorrowCaps = lending.BorrowCaps{}
		})
		engine.SetState(state)
		engine.SetBlockHeight(10)

		if _, err := engine.Borrow(borrower, new(big.Int).Set(one), crypto.Address{}, 0); err != nil {
			t.Fatalf("fresh oracle borrow should succeed: %v", err)
		}

		engine.SetBlockHeight(30)
		if _, err := engine.Borrow(borrower, new(big.Int).Set(one), crypto.Address{}, 0); err == nil || !strings.Contains(err.Error(), "oracle") {
			t.Fatalf("expected oracle freshness breach, got %v", err)
		}

		if _, err := engine.Repay(borrower, new(big.Int).Set(one)); err != nil {
			t.Fatalf("repay should succeed despite stale oracle: %v", err)
		}
	})

	t.Run("oracle deviation", func(t *testing.T) {
		engine, state := setupCapsEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
			p.BorrowCaps = lending.BorrowCaps{}
		})
		state.market.OraclePrevMedianWei = mustBig("100000000")
		state.market.OracleMedianWei = mustBig("200000000")
		engine.SetState(state)
		engine.SetBlockHeight(12)

		if _, err := engine.Borrow(borrower, new(big.Int).Set(one), crypto.Address{}, 0); err == nil || !strings.Contains(err.Error(), "deviation") {
			t.Fatalf("expected oracle deviation breach, got %v", err)
		}
	})

	t.Run("borrow pause switch", func(t *testing.T) {
		engine, state := setupCapsEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
			p.Pauses.Borrow = true
		})
		engine.SetState(state)
		engine.SetBlockHeight(14)

		if _, err := engine.Borrow(borrower, new(big.Int).Set(one), crypto.Address{}, 0); err == nil || !strings.Contains(err.Error(), "paused") {
			t.Fatalf("expected borrow pause error, got %v", err)
		}
	})
}
