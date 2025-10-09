package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/rewards"
	"nhbchain/core/types"
)

func Test_AccrualOnStakeTopUpAndUnstake(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var delegator [20]byte
	delegator[19] = 0x01

	start := time.Unix(1_700_000_000, 0)
	sp.BeginBlock(1, start)
	if err := sp.SetStakeRewardAPR(1_000); err != nil {
		t.Fatalf("set apr: %v", err)
	}

	initial := &types.Account{BalanceZNHB: big.NewInt(10_000)}
	writeAccount(t, sp, delegator, initial)

	if _, err := sp.StakeDelegate(delegator[:], nil, big.NewInt(100)); err != nil {
		t.Fatalf("initial stake: %v", err)
	}

	// Advance one year and top up to trigger accrual of the first period.
	sp.BeginBlock(2, start.Add(365*24*time.Hour))
	if _, err := sp.StakeDelegate(delegator[:], nil, big.NewInt(50)); err != nil {
		t.Fatalf("top up stake: %v", err)
	}

	account, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if got, want := account.StakeShares, big.NewInt(10); got.Cmp(want) != 0 {
		t.Fatalf("unexpected shares after top up: got %s want %s", got, want)
	}

	// Advance another year and partially unstake.
	sp.BeginBlock(3, start.Add(2*365*24*time.Hour))
	if _, err := sp.StakeUndelegate(delegator[:], big.NewInt(60)); err != nil {
		t.Fatalf("unstake: %v", err)
	}

	account, err = sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account after unstake: %v", err)
	}
	if got, want := account.StakeShares, big.NewInt(25); got.Cmp(want) != 0 {
		t.Fatalf("unexpected shares after unstake: got %s want %s", got, want)
	}
	expectedIndex := new(big.Int).Mul(big.NewInt(12), rewards.IndexUnit())
	expectedIndex.Quo(expectedIndex, big.NewInt(10)) // 1.2 * index unit
	if account.StakeLastIndex.Cmp(expectedIndex) != 0 {
		t.Fatalf("unexpected last index: got %s want %s", account.StakeLastIndex, expectedIndex)
	}
}

func Test_ParamChangeRollsIndexFirst(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var delegator [20]byte
	delegator[19] = 0x02

	start := time.Unix(1_700_000_000, 0)
	sp.BeginBlock(1, start)
	if err := sp.SetStakeRewardAPR(1_000); err != nil {
		t.Fatalf("set apr: %v", err)
	}

	writeAccount(t, sp, delegator, &types.Account{BalanceZNHB: big.NewInt(10_000)})

	if _, err := sp.StakeDelegate(delegator[:], nil, big.NewInt(100)); err != nil {
		t.Fatalf("initial stake: %v", err)
	}

	// Move one year forward and change APR.
	blockTwo := start.Add(365 * 24 * time.Hour)
	sp.BeginBlock(2, blockTwo)
	if err := sp.SetStakeRewardAPR(2_000); err != nil {
		t.Fatalf("update apr: %v", err)
	}
	if sp.stakeRewardEngine.LastUpdateTs() != uint64(blockTwo.Unix()) {
		t.Fatalf("last update not recorded")
	}

	if _, err := sp.StakeDelegate(delegator[:], nil, big.NewInt(10)); err != nil {
		t.Fatalf("top up stake: %v", err)
	}
	account, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if got, want := account.StakeShares, big.NewInt(10); got.Cmp(want) != 0 {
		t.Fatalf("unexpected shares after apr change: got %s want %s", got, want)
	}

	// Advance another year at the new rate and trigger accrual.
	sp.BeginBlock(3, start.Add(2*365*24*time.Hour))
	if _, err := sp.StakeUndelegate(delegator[:], big.NewInt(10)); err != nil {
		t.Fatalf("unstake: %v", err)
	}
	account, err = sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account after new apr: %v", err)
	}
	if got, want := account.StakeShares, big.NewInt(32); got.Cmp(want) != 0 {
		t.Fatalf("unexpected shares after new apr period: got %s want %s", got, want)
	}
	expectedIndex := new(big.Int).Mul(big.NewInt(13), rewards.IndexUnit())
	expectedIndex.Quo(expectedIndex, big.NewInt(10)) // 1.3 * index unit
	if account.StakeLastIndex.Cmp(expectedIndex) != 0 {
		t.Fatalf("unexpected last index after apr change: got %s want %s", account.StakeLastIndex, expectedIndex)
	}
}
