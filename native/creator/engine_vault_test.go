package creator

import (
	"errors"
	"math/big"
	"testing"
)

// PL-DC-39: an engine without a stake vault refuses to take or pay principal rather
// than falling back to the old burn-and-mint behaviour, and a vault that cannot cover
// a payout refuses it without changing anything.

func vaultEngine(t *testing.T, withVault bool) (*Engine, *mockState, [20]byte, [20]byte, [20]byte) {
	t.Helper()
	state := newMockState()
	engine := NewEngine()
	engine.SetState(state)
	payoutVault, rewards, stakeVault := addr(0xE0), addr(0xE1), addr(0xE2)
	engine.SetPayoutVault(payoutVault)
	engine.SetRewardsTreasury(rewards)
	if withVault {
		engine.SetStakeVault(stakeVault)
	}
	fan, creatorAddr := addr(0x60), addr(0x61)
	state.setAccount(fan, 50_000)
	state.setAccount(payoutVault, 0)
	state.setAccount(rewards, 0)
	state.setAccount(stakeVault, 0)
	return engine, state, fan, creatorAddr, stakeVault
}

func TestStakingWithoutAStakeVaultIsRefusedAndChangesNothing(t *testing.T) {
	engine, state, fan, creatorAddr, _ := vaultEngine(t, false)
	before := new(big.Int).Set(state.account(fan).BalanceNHB)
	if _, _, err := engine.StakeCreator(fan, creatorAddr, big.NewInt(5_000)); !errors.Is(err, errStakeVaultNotSet) {
		t.Fatalf("got %v, want errStakeVaultNotSet", err)
	}
	if state.account(fan).BalanceNHB.Cmp(before) != 0 {
		t.Fatalf("a refused stake changed the fan's balance")
	}
}

func TestUnstakingWithoutAStakeVaultIsRefused(t *testing.T) {
	engine, _, fan, creatorAddr, _ := vaultEngine(t, false)
	if _, err := engine.UnstakeCreator(fan, creatorAddr, big.NewInt(1)); !errors.Is(err, errStakeVaultNotSet) {
		t.Fatalf("got %v, want errStakeVaultNotSet", err)
	}
}

func TestAnUnderfundedStakeVaultRefusesThePayoutAndChangesNothing(t *testing.T) {
	engine, state, fan, creatorAddr, stakeVault := vaultEngine(t, true)
	stake, _, err := engine.StakeCreator(fan, creatorAddr, big.NewInt(10_000))
	if err != nil {
		t.Fatalf("stake: %v", err)
	}
	// Drain the vault below what the position redeems for.
	state.setAccount(stakeVault, 1)
	fanBefore := new(big.Int).Set(state.account(fan).BalanceNHB)
	if _, err := engine.UnstakeCreator(fan, creatorAddr, stake.Shares); !errors.Is(err, errStakeVaultUnderfunded) {
		t.Fatalf("got %v, want errStakeVaultUnderfunded", err)
	}
	if state.account(fan).BalanceNHB.Cmp(fanBefore) != 0 {
		t.Fatalf("a refused unstake paid the fan")
	}
	if state.account(stakeVault).BalanceNHB.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("a refused unstake changed the vault")
	}
}
