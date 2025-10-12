package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/rewards"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

func TestClaim_MidCycleTopUp(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var delegator [20]byte
	delegator[19] = 0x90

	aprBps := uint64(1_200)
	period := time.Duration(stakePayoutPeriodSeconds) * time.Second
	start := time.Unix(1_800_000_000, 0).UTC()
	mid := start.Add(period / 2)
	end := start.Add(period)

	engine := rewards.NewEngine()
	engine.UpdateGlobalIndex(start, aprBps)
	engine.UpdateGlobalIndex(mid, aprBps)
	indexMid := engine.Index()
	engine.UpdateGlobalIndex(end, aprBps)
	indexEnd := engine.Index()

	baseStake := big.NewInt(1_500)
	topUp := big.NewInt(500)

	shares := new(big.Int).Mul(baseStake, big.NewInt(2))
	shares.Add(shares, topUp)

	account := &types.Account{
		StakeShares:       new(big.Int).Set(shares),
		StakeLastIndex:    new(big.Int).Set(indexMid),
		StakeLastPayoutTs: uint64(start.Unix()),
	}
	writeAccount(t, sp, delegator, account)

	sp.nowFunc = func() time.Time { return end }
	sp.stakeRewardAPR = aprBps

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.PutAccountMetadata(delegator[:], account); err != nil {
		t.Fatalf("put account metadata: %v", err)
	}
	if err := manager.SetStakingGlobalIndex(indexEnd); err != nil {
		t.Fatalf("set global index: %v", err)
	}

	before, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if before.StakeShares.Cmp(shares) != 0 {
		t.Fatalf("unexpected shares: got %s want %s", before.StakeShares, shares)
	}
	if before.StakeLastIndex.Cmp(indexMid) != 0 {
		t.Fatalf("unexpected last index: got %s want %s", before.StakeLastIndex, indexMid)
	}

	minted, err := sp.StakeClaimRewards(delegator[:])
	if err != nil {
		t.Fatalf("claim rewards: %v", err)
	}
	if minted.Sign() <= 0 {
		t.Fatalf("expected positive mint, got %s", minted)
	}

	monthlyRate := new(big.Rat).SetFrac(new(big.Int).SetUint64(aprBps), big.NewInt(12*10_000))
	avgStake := new(big.Rat).SetInt(baseStake)
	halfTopUp := new(big.Rat).SetInt(topUp)
	halfTopUp.Mul(halfTopUp, big.NewRat(1, 2))
	avgStake.Add(avgStake, halfTopUp)
	expectedRat := new(big.Rat).Mul(monthlyRate, avgStake)
	expectedWei := new(big.Rat).Mul(expectedRat, new(big.Rat).SetInt(rewards.IndexUnit()))

	expected := new(big.Int).Quo(expectedWei.Num(), expectedWei.Denom())
	diff := new(big.Int).Sub(minted, expected)
	tolerance := rewards.IndexUnit()
	if diff.Abs(diff); diff.Cmp(tolerance) > 0 {
		t.Fatalf("minted mismatch: got %s want %s (|diff|=%s)", minted, expected, diff)
	}
}

func TestClaim_TwoPeriods(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var delegator [20]byte
	delegator[19] = 0x91

	aprBps := uint64(1_200)
	period := time.Duration(stakePayoutPeriodSeconds) * time.Second
	start := time.Unix(1_800_100_000, 0).UTC()
	end := start.Add(2 * period)

	engine := rewards.NewEngine()
	engine.UpdateGlobalIndex(start, aprBps)
	indexStart := engine.Index()
	engine.UpdateGlobalIndex(end, aprBps)
	indexEnd := engine.Index()

	stake := big.NewInt(2_000)

	account := &types.Account{
		StakeShares:       new(big.Int).Set(stake),
		StakeLastIndex:    new(big.Int).Set(indexStart),
		StakeLastPayoutTs: uint64(start.Unix()),
	}
	writeAccount(t, sp, delegator, account)

	sp.nowFunc = func() time.Time { return end }
	sp.stakeRewardAPR = aprBps

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.PutAccountMetadata(delegator[:], account); err != nil {
		t.Fatalf("put account metadata: %v", err)
	}
	if err := manager.SetStakingGlobalIndex(indexEnd); err != nil {
		t.Fatalf("set global index: %v", err)
	}

	minted, err := sp.StakeClaimRewards(delegator[:])
	if err != nil {
		t.Fatalf("claim rewards: %v", err)
	}
	if minted.Sign() <= 0 {
		t.Fatalf("expected positive mint, got %s", minted)
	}

	monthlyRate := new(big.Rat).SetFrac(new(big.Int).SetUint64(aprBps), big.NewInt(12*10_000))
	monthlyRat := new(big.Rat).Mul(monthlyRate, new(big.Rat).SetInt(stake))
	totalRat := new(big.Rat).Mul(monthlyRat, big.NewRat(2, 1))
	expectedWei := new(big.Rat).Mul(totalRat, new(big.Rat).SetInt(rewards.IndexUnit()))
	expected := new(big.Int).Quo(expectedWei.Num(), expectedWei.Denom())
	diff := new(big.Int).Sub(minted, expected)
	tolerance := rewards.IndexUnit()
	if diff.Abs(diff); diff.Cmp(tolerance) > 0 {
		t.Fatalf("minted mismatch: got %s want %s (|diff|=%s)", minted, expected, diff)
	}
}
