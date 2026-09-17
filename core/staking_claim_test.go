package core

import (
	"math/big"
	"strconv"
	"testing"
	"time"

	"nhbchain/core/rewards"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/governance"
)

const (
	testBasisPointsDenom = 10_000
	testSecondsPerYear   = 365 * 24 * 60 * 60
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

	aprRat := new(big.Rat).SetFrac(new(big.Int).SetUint64(aprBps), big.NewInt(testBasisPointsDenom))
	firstDuration := new(big.Rat).SetFrac(big.NewInt(mid.Unix()-start.Unix()), big.NewInt(testSecondsPerYear))
	secondDuration := new(big.Rat).SetFrac(big.NewInt(end.Unix()-mid.Unix()), big.NewInt(testSecondsPerYear))

	baseContribution := new(big.Rat).Mul(new(big.Rat).Set(aprRat), new(big.Rat).SetInt(baseStake))
	baseContribution.Mul(baseContribution, firstDuration)

	postTopUpStake := new(big.Int).Add(baseStake, topUp)
	topUpContribution := new(big.Rat).Mul(new(big.Rat).Set(aprRat), new(big.Rat).SetInt(postTopUpStake))
	topUpContribution.Mul(topUpContribution, secondDuration)

	expectedTokens := new(big.Rat).Add(baseContribution, topUpContribution)
	expectedWei := new(big.Rat).Mul(expectedTokens, new(big.Rat).SetInt(rewards.IndexUnit()))

	expected := new(big.Int).Quo(expectedWei.Num(), expectedWei.Denom())
	diff := new(big.Int).Sub(minted, expected)
	tolerance := rewards.IndexUnit()
	if diff.Abs(diff); diff.Cmp(tolerance) > 0 {
		t.Fatalf("minted mismatch: got %s want %s (|diff|=%s)", minted, expected, diff)
	}
}

func TestClaim_TwoPeriods(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		periods int64
	}{
		{name: "single", periods: 1},
		{name: "double", periods: 2},
		{name: "triple", periods: 3},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			sp := newStakingStateProcessor(t)

			var delegator [20]byte
			delegator[19] = 0x91

			aprBps := uint64(1_200)
			period := time.Duration(stakePayoutPeriodSeconds) * time.Second
			start := time.Unix(1_800_100_000, 0).UTC()
			end := start.Add(time.Duration(tc.periods) * period)

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

			elapsedSeconds := new(big.Int).Mul(big.NewInt(tc.periods), big.NewInt(int64(period/time.Second)))
			aprRat := new(big.Rat).SetFrac(new(big.Int).SetUint64(aprBps), big.NewInt(testBasisPointsDenom))
			stakeRat := new(big.Rat).SetInt(stake)
			durationRat := new(big.Rat).SetFrac(elapsedSeconds, big.NewInt(testSecondsPerYear))

			expectedTokens := new(big.Rat).Mul(aprRat, stakeRat)
			expectedTokens.Mul(expectedTokens, durationRat)

			expectedWei := new(big.Rat).Mul(expectedTokens, new(big.Rat).SetInt(rewards.IndexUnit()))
			expected := new(big.Int).Quo(expectedWei.Num(), expectedWei.Denom())

			diff := new(big.Int).Sub(minted, expected)
			tolerance := rewards.IndexUnit()
			if diff.Abs(diff); diff.Cmp(tolerance) > 0 {
				t.Fatalf("minted mismatch: got %s want %s (|diff|=%s)", minted, expected, diff)
			}
		})
	}
}

func TestClaim_CustomPayoutPeriod(t *testing.T) {
	t.Parallel()

	sp := newStakingStateProcessor(t)

	var delegator [20]byte
	delegator[19] = 0x92

	aprBps := uint64(1_050)
	customDays := uint64(14)
	period := time.Duration(customDays) * 24 * time.Hour

	start := time.Unix(1_801_200_000, 0).UTC()
	end := start.Add(period)

	engine := rewards.NewEngine()
	engine.UpdateGlobalIndex(start, aprBps)
	indexStart := engine.Index()
	engine.UpdateGlobalIndex(end, aprBps)
	indexEnd := engine.Index()

	stake := big.NewInt(4_000)
	account := &types.Account{
		StakeShares:       new(big.Int).Set(stake),
		StakeLastIndex:    new(big.Int).Set(indexStart),
		StakeLastPayoutTs: uint64(start.Unix()),
	}
	writeAccount(t, sp, delegator, account)

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.ParamStoreSet(governance.ParamKeyStakingPayoutPeriodDays, []byte(strconv.FormatUint(customDays, 10))); err != nil {
		t.Fatalf("set payout period: %v", err)
	}
	if err := manager.PutAccountMetadata(delegator[:], account); err != nil {
		t.Fatalf("put account metadata: %v", err)
	}
	if err := manager.SetStakingGlobalIndex(indexEnd); err != nil {
		t.Fatalf("set global index: %v", err)
	}

	sp.stakeRewardAPR = aprBps

	sp.nowFunc = func() time.Time { return end.Add(-time.Second) }
	if _, err := sp.StakeClaimRewards(delegator[:]); err == nil {
		t.Fatalf("expected error before payout window elapses")
	}

	sp.nowFunc = func() time.Time { return end }
	minted, err := sp.StakeClaimRewards(delegator[:])
	if err != nil {
		t.Fatalf("claim rewards: %v", err)
	}
	if minted.Sign() <= 0 {
		t.Fatalf("expected positive mint, got %s", minted)
	}

	elapsedSeconds := big.NewInt(int64(period / time.Second))
	aprRat := new(big.Rat).SetFrac(new(big.Int).SetUint64(aprBps), big.NewInt(testBasisPointsDenom))
	stakeRat := new(big.Rat).SetInt(stake)
	durationRat := new(big.Rat).SetFrac(elapsedSeconds, big.NewInt(testSecondsPerYear))

	expectedTokens := new(big.Rat).Mul(aprRat, stakeRat)
	expectedTokens.Mul(expectedTokens, durationRat)

	expectedWei := new(big.Rat).Mul(expectedTokens, new(big.Rat).SetInt(rewards.IndexUnit()))
	expected := new(big.Int).Quo(expectedWei.Num(), expectedWei.Denom())

	diff := new(big.Int).Sub(minted, expected)
	tolerance := rewards.IndexUnit()
	if diff.Abs(diff); diff.Cmp(tolerance) > 0 {
		t.Fatalf("minted mismatch: got %s want %s (|diff|=%s)", minted, expected, diff)
	}
}
