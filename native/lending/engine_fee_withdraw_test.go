package lending

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// PL-DC-19: the module balance is shared by every pool, so a pool's fee withdrawal
// must be bounded by the pool's own free liquidity, not by what the module holds.
func TestProtocolFeesAreOnlyPaidOutOfThePoolsOwnFreeLiquidity(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x51)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x52)
	recipient := makeAddress(crypto.NHBPrefix, 0x53)

	build := func(borrowed int64) (*Engine, *mockEngineState) {
		engine := NewEngine(moduleAddr, collateralAddr, RiskParameters{})
		state := newMockEngineState()
		state.market = &Market{
			TotalNHBSupplied:  big.NewInt(1_000),
			TotalSupplyShares: big.NewInt(1_000),
			TotalNHBBorrowed:  big.NewInt(borrowed),
			SupplyIndex:       new(big.Int).Set(ray),
			BorrowIndex:       new(big.Int).Set(ray),
		}
		// Other pools' cash sits in the same module balance.
		state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(5_000), BalanceZNHB: big.NewInt(0)}
		state.accounts[state.key(recipient)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
		state.fees = &FeeAccrual{ProtocolFeesWei: big.NewInt(100), DeveloperFeesWei: big.NewInt(0)}
		engine.SetState(state)
		engine.SetPoolID("default")
		return engine, state
	}

	// Fully lent out: nothing is free, however much the module holds.
	engine, state := build(1_000)
	if _, err := engine.WithdrawProtocolFees(recipient, big.NewInt(50)); !errors.Is(err, errInsufficientLiquidity) {
		t.Fatalf("a pool with no free liquidity paid fees out of the shared balance: %v", err)
	}
	if state.accounts[state.key(moduleAddr)].BalanceNHB.Cmp(big.NewInt(5_000)) != 0 || state.fees.ProtocolFeesWei.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("a refused withdrawal changed state")
	}

	// 100 free: exactly that much can be paid, one wei more cannot.
	engine, state = build(900)
	if _, err := engine.WithdrawProtocolFees(recipient, big.NewInt(101)); !errors.Is(err, errInsufficientLiquidity) {
		t.Fatalf("101 against 100 free liquidity and a 100 counter: got %v", err)
	}
	if _, err := engine.WithdrawProtocolFees(recipient, big.NewInt(100)); err != nil {
		t.Fatalf("exactly the free liquidity: %v", err)
	}
	if got := state.market.TotalNHBSupplied; got.Cmp(big.NewInt(900)) != 0 {
		t.Fatalf("the suppliers' total is %s after the payment, want 900", got)
	}

	// The free liquidity is the cash the pool holds: what lenders supplied plus the principal
	// of fixed-term deposits, less what is lent out.
	engine, state = build(1_400)
	state.market.TotalFixedTermDepositPrincipalWei = big.NewInt(450)
	// supplied 1,000 + deposits 450 - borrowed 1,400 = 50 free.
	if _, err := engine.WithdrawProtocolFees(recipient, big.NewInt(51)); !errors.Is(err, errInsufficientLiquidity) {
		t.Fatalf("51 against 50 free: got %v", err)
	}
	if _, err := engine.WithdrawProtocolFees(recipient, big.NewInt(50)); err != nil {
		t.Fatalf("exactly the 50 free, counting the fixed-term deposit principal: %v", err)
	}
}
