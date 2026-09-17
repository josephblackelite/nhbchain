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
	engine.SetPayoutVault(vault)
	engine.SetRewardsTreasury(rewards)
	state.setAccount(vault, 0)
	state.setAccount(rewards, 0)

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
		engine.SetPayoutVault(payoutVault)
		engine.SetRewardsTreasury(rewards)

		fanA := addr(0x11)
		fanB := addr(0x12)
		creatorAddr := addr(0x20)

		state.setAccountBig(fanA, big.NewInt(500_000_000))
		state.setAccountBig(fanB, big.NewInt(500_000_000))
		state.setAccount(payoutVault, 0)
		state.setAccount(rewards, 0)

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
	})
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
