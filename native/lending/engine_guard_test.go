package lending

import (
	"errors"
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
)

type stubPauseView struct {
	modules map[string]bool
}

func (s stubPauseView) IsPaused(module string) bool {
	if s.modules == nil {
		return false
	}
	return s.modules[module]
}

func TestSupplyGuardBlocksMutation(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0xAA)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0xBB)
	supplier := makeAddress(crypto.NHBPrefix, 0xCC)

	engine := NewEngine(moduleAddr, collateralAddr, RiskParameters{})
	engine.SetPauses(stubPauseView{modules: map[string]bool{"lending": true}})

	state := newMockEngineState()
	state.market = &Market{
		TotalNHBSupplied:  big.NewInt(0),
		TotalSupplyShares: big.NewInt(0),
		TotalNHBBorrowed:  big.NewInt(0),
		SupplyIndex:       big.NewInt(1),
		BorrowIndex:       big.NewInt(1),
	}
	state.accounts[state.key(supplier)] = &types.Account{BalanceNHB: big.NewInt(500), BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(moduleAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}

	engine.SetState(state)
	engine.SetPoolID("default")

	if _, err := engine.Supply(supplier, big.NewInt(100)); !errors.Is(err, nativecommon.ErrModulePaused) {
		t.Fatalf("expected ErrModulePaused, got %v", err)
	}

	if balance := state.accounts[state.key(supplier)].BalanceNHB; balance.Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("expected supplier balance to remain 500, got %s", balance)
	}
	if supplied := state.market.TotalNHBSupplied; supplied.Sign() != 0 {
		t.Fatalf("expected market supply unchanged, got %s", supplied)
	}
}
