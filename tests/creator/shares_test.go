package creator_test

import (
	"math/big"
	"testing"

	"nhbchain/native/creator"
)

func TestStakeRejectsTinyDeposit(t *testing.T) {
	state := newTestState()
	engine := creator.NewEngine()
	engine.SetState(state)
	vault := addr(0xAA)
	rewards := addr(0xBB)
	stakeVault := addr(0xAC)
	engine.SetPayoutVault(vault)
	engine.SetRewardsTreasury(rewards)
	engine.SetStakeVault(stakeVault)
	state.setAccount(vault, 0)
	state.setAccount(rewards, 0)
	state.setAccount(stakeVault, 0)

	fan := addr(0x01)
	creatorAddr := addr(0x02)
	state.setAccount(fan, 10_000)

	if _, _, err := engine.StakeCreator(fan, creatorAddr, big.NewInt(1)); err == nil {
		t.Fatalf("expected minimum deposit error")
	}
}

func FuzzShareRedemptionProRata(f *testing.F) {
	f.Add(int64(5_000), int64(7_500))
	f.Add(int64(20_000), int64(15_000))
	f.Fuzz(func(t *testing.T, a int64, b int64) {
		depositA := big.NewInt(1_000 + absInt64(a)%90_000)
		depositB := big.NewInt(1_000 + absInt64(b)%90_000)

		state := newTestState()
		engine := creator.NewEngine()
		engine.SetState(state)
		payoutVault := addr(0xA1)
		rewards := addr(0xB1)
		stakeVault := addr(0xA2)
		engine.SetPayoutVault(payoutVault)
		engine.SetRewardsTreasury(rewards)
		engine.SetStakeVault(stakeVault)

		fanA := addr(0x11)
		fanB := addr(0x12)
		creatorAddr := addr(0x20)

		state.setAccountBig(fanA, big.NewInt(500_000_000))
		state.setAccountBig(fanB, big.NewInt(500_000_000))
		state.setAccount(payoutVault, 0)
		state.setAccount(rewards, 0)
		state.setAccount(stakeVault, 0)

		totalBefore := new(big.Int).Add(state.account(fanA).BalanceNHB, state.account(fanB).BalanceNHB)

		stakeA, _, err := engine.StakeCreator(fanA, creatorAddr, depositA)
		if err != nil {
			t.Fatalf("stake A failed: %v", err)
		}
		if stakeA.Shares.Sign() == 0 {
			t.Fatalf("expected shares for stake A")
		}
		if _, _, err := engine.StakeCreator(fanB, creatorAddr, depositB); err != nil {
			t.Fatalf("stake B failed: %v", err)
		}

		// The vault must now actually hold what both fans put in -- it must
		// never exceed the sum of deposits (would mean assets appeared from
		// nowhere) and must never fall short of it either (would mean
		// principal vanished).
		vaultHeld := state.account(stakeVault).BalanceNHB
		wantHeld := new(big.Int).Add(depositA, depositB)
		if vaultHeld.Cmp(wantHeld) != 0 {
			t.Fatalf("stake vault does not hold both deposits: got %s want %s", vaultHeld, wantHeld)
		}

		balanceBefore := state.account(fanA).BalanceNHB
		redeemed := new(big.Int).Set(stakeA.Shares)
		if _, err := engine.UnstakeCreator(fanA, creatorAddr, redeemed); err != nil {
			t.Fatalf("unstake A failed: %v", err)
		}
		balanceAfter := state.account(fanA).BalanceNHB
		withdrawn := new(big.Int).Sub(balanceAfter, balanceBefore)
		if withdrawn.Cmp(depositA) > 0 {
			t.Fatalf("fan A withdrew more than deposit: got %s want <= %s", withdrawn, depositA)
		}

		// Total NHB across the fans and the vault must be exactly conserved:
		// nothing was created or destroyed by the stake/unstake round trip
		// (the payout vault and rewards treasury are untouched here since the
		// staking-yield reward path only engages above a bps-scaled minimum).
		totalAfter := new(big.Int).Add(state.account(fanA).BalanceNHB, state.account(fanB).BalanceNHB)
		totalAfter = new(big.Int).Add(totalAfter, state.account(stakeVault).BalanceNHB)
		totalBeforeWithVault := new(big.Int).Add(totalBefore, state.account(payoutVault).BalanceNHB)
		totalBeforeWithVault = new(big.Int).Add(totalBeforeWithVault, state.account(rewards).BalanceNHB)
		totalAfterWithTreasury := new(big.Int).Add(totalAfter, state.account(payoutVault).BalanceNHB)
		totalAfterWithTreasury = new(big.Int).Add(totalAfterWithTreasury, state.account(rewards).BalanceNHB)
		if totalBeforeWithVault.Cmp(totalAfterWithTreasury) != 0 {
			t.Fatalf("total NHB not conserved across stake/unstake: before %s after %s", totalBeforeWithVault, totalAfterWithTreasury)
		}
	})
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
