package lending_test

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

// TestWithdrawSameBlockAsSupplyBlocked confirms the anti-MEV guard added
// alongside RepayFixedTerm's pool-routing change (native/lending/engine.go's
// Withdraw): a supplier who withdraws in the very same block as their own
// Supply call is rejected, closing the atomic, zero-real-duration round trip
// that could otherwise snipe a disproportionate share of a same-block
// lump-sum SupplyIndex bump.
func TestWithdrawSameBlockAsSupplyBlocked(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x30)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x31)
	supplier := makeAddress(crypto.NHBPrefix, 0x32)

	engine := lending.NewEngine(moduleAddr, collateralAddr, lending.RiskParameters{})
	engine.SetPoolID("default")
	engine.SetBlockHeight(100)

	state := newMockEngineState()
	ray := mustBig("1000000000000000000000000000")
	state.market = &lending.Market{
		PoolID:            "default",
		TotalNHBSupplied:  big.NewInt(0),
		TotalSupplyShares: big.NewInt(0),
		TotalNHBBorrowed:  big.NewInt(0),
		SupplyIndex:       new(big.Int).Set(ray),
		BorrowIndex:       new(big.Int).Set(ray),
	}
	deposit := mustBig("1000000000000000000")
	state.accounts[state.key(supplier)] = &types.Account{BalanceNHB: new(big.Int).Set(deposit), BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}

	engine.SetState(state)

	minted, err := engine.Supply(supplier, deposit)
	if err != nil {
		t.Fatalf("supply failed: %v", err)
	}

	if _, err := engine.Withdraw(supplier, minted); !errors.Is(err, lending.ErrWithdrawSameBlockAsSupply) {
		t.Fatalf("expected ErrWithdrawSameBlockAsSupply, got %v", err)
	}

	// The rejected withdrawal must not have mutated any state.
	if state.market.TotalSupplyShares.Cmp(minted) != 0 {
		t.Fatalf("expected total shares to remain %s, got %s", minted, state.market.TotalSupplyShares)
	}
	if balance := state.accounts[state.key(supplier)].BalanceNHB; balance.Sign() != 0 {
		t.Fatalf("expected supplier NHB balance to remain 0 after supply, got %s", balance)
	}
}

// TestWithdrawAfterSubsequentBlockSucceeds confirms the guard is scoped to
// the exact supply block only -- a withdrawal in any later block proceeds
// normally, so genuine suppliers are never blocked from ever exiting.
func TestWithdrawAfterSubsequentBlockSucceeds(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x33)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x34)
	supplier := makeAddress(crypto.NHBPrefix, 0x35)

	engine := lending.NewEngine(moduleAddr, collateralAddr, lending.RiskParameters{})
	engine.SetPoolID("default")
	engine.SetBlockHeight(100)

	state := newMockEngineState()
	ray := mustBig("1000000000000000000000000000")
	state.market = &lending.Market{
		PoolID:            "default",
		TotalNHBSupplied:  big.NewInt(0),
		TotalSupplyShares: big.NewInt(0),
		TotalNHBBorrowed:  big.NewInt(0),
		SupplyIndex:       new(big.Int).Set(ray),
		BorrowIndex:       new(big.Int).Set(ray),
	}
	deposit := mustBig("1000000000000000000")
	state.accounts[state.key(supplier)] = &types.Account{BalanceNHB: new(big.Int).Set(deposit), BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}

	engine.SetState(state)

	minted, err := engine.Supply(supplier, deposit)
	if err != nil {
		t.Fatalf("supply failed: %v", err)
	}

	engine.SetBlockHeight(101)

	redeemed, err := engine.Withdraw(supplier, minted)
	if err != nil {
		t.Fatalf("expected withdrawal in a later block to succeed, got %v", err)
	}
	if redeemed.Cmp(deposit) != 0 {
		t.Fatalf("expected redeemed amount %s, got %s", deposit, redeemed)
	}
}
