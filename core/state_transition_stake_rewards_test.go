package core

import (
	"math/big"
	"strconv"
	"testing"
	"time"

	"nhbchain/core/events"
	"nhbchain/core/rewards"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/governance"
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

func Test_EmissionCap_PartialMintAndEvent(t *testing.T) {
	sp := newStakingStateProcessor(t)

	current := time.Unix(1_700_000_000, 0).UTC()
	sp.nowFunc = func() time.Time { return current }

	var delegator [20]byte
	delegator[19] = 0x55

	account := &types.Account{
		BalanceZNHB:       big.NewInt(0),
		StakeShares:       big.NewInt(5),
		StakeLastIndex:    rewards.IndexUnit(),
		StakeLastPayoutTs: uint64(current.Add(-time.Duration(stakePayoutPeriodSeconds) * time.Second).Unix()),
	}
	writeAccount(t, sp, delegator, account)
	if err := sp.setAccount(delegator[:], account); err != nil {
		t.Fatalf("set account: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	delta := big.NewInt(400)
	globalIndex := new(big.Int).Add(rewards.IndexUnit(), delta)
	if err := manager.SetStakingGlobalIndex(globalIndex); err != nil {
		t.Fatalf("set global index: %v", err)
	}
	if err := manager.ParamStoreSet(governance.ParamKeyStakingMaxEmissionPerYearWei, []byte("750")); err != nil {
		t.Fatalf("set emission cap: %v", err)
	}

	minted, err := sp.StakeClaimRewards(delegator[:])
	if err != nil {
		t.Fatalf("claim rewards: %v", err)
	}

	if got, want := minted.String(), "750"; got != want {
		t.Fatalf("unexpected minted amount: got %s want %s", got, want)
	}

	updated, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	expectedIndex := new(big.Int).Add(rewards.IndexUnit(), big.NewInt(150))
	if updated.StakeLastIndex.Cmp(expectedIndex) != 0 {
		t.Fatalf("stake index mismatch: got %s want %s", updated.StakeLastIndex, expectedIndex)
	}
	if updated.BalanceZNHB.Cmp(big.NewInt(750)) != 0 {
		t.Fatalf("balance mismatch: got %s", updated.BalanceZNHB)
	}

	emissionTotal, err := manager.StakingEmissionYTD(uint32(current.Year()))
	if err != nil {
		t.Fatalf("emission total: %v", err)
	}
	if emissionTotal.Cmp(big.NewInt(750)) != 0 {
		t.Fatalf("unexpected emission total: got %s", emissionTotal)
	}

	evts := sp.Events()
	if len(evts) != 3 {
		t.Fatalf("unexpected event count: %d", len(evts))
	}
	capEvt := evts[0]
	if capEvt.Type != events.TypeStakeEmissionCapHit {
		t.Fatalf("expected emission cap event, got %s", capEvt.Type)
	}
	attempted := new(big.Int).Mul(delta, account.StakeShares)
	if got := capEvt.Attributes["attemptedZNHB"]; got != attempted.String() {
		t.Fatalf("cap attempted mismatch: got %s want %s", got, attempted.String())
	}
	if got := capEvt.Attributes["ytd"]; got != "750" {
		t.Fatalf("cap ytd mismatch: got %s", got)
	}
	if got := capEvt.Attributes["cap"]; got != "750" {
		t.Fatalf("cap limit mismatch: got %s", got)
	}

	rewardsEvt := evts[1]
	if rewardsEvt.Type != events.TypeStakeRewardsClaimed {
		t.Fatalf("expected rewards event, got %s", rewardsEvt.Type)
	}
	if got := rewardsEvt.Attributes["paidZNHB"]; got != "750" {
		t.Fatalf("rewards paid mismatch: got %s", got)
	}
	expectedNext := strconv.FormatUint(updated.StakeLastPayoutTs+stakePayoutPeriodSeconds, 10)
	if got := rewardsEvt.Attributes["nextEligibleUnix"]; got != expectedNext {
		t.Fatalf("rewards nextEligible mismatch: got %s want %s", got, expectedNext)
	}
}

func Test_YTD_RolloverNewYear(t *testing.T) {
	sp := newStakingStateProcessor(t)

	current := time.Date(2023, time.December, 31, 12, 0, 0, 0, time.UTC)
	now := current
	sp.nowFunc = func() time.Time { return now }

	var delegator [20]byte
	delegator[19] = 0x56

	account := &types.Account{
		BalanceZNHB:       big.NewInt(0),
		StakeShares:       big.NewInt(2),
		StakeLastIndex:    rewards.IndexUnit(),
		StakeLastPayoutTs: uint64(current.Add(-time.Duration(stakePayoutPeriodSeconds) * time.Second).Unix()),
	}
	writeAccount(t, sp, delegator, account)
	if err := sp.setAccount(delegator[:], account); err != nil {
		t.Fatalf("set account: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	deltaOne := big.NewInt(200)
	if err := manager.SetStakingGlobalIndex(new(big.Int).Add(rewards.IndexUnit(), deltaOne)); err != nil {
		t.Fatalf("set global index: %v", err)
	}

	mintedFirst, err := sp.StakeClaimRewards(delegator[:])
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if mintedFirst.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("unexpected first mint: %s", mintedFirst)
	}

	afterFirst, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account after first claim: %v", err)
	}

	emission2023, err := manager.StakingEmissionYTD(2023)
	if err != nil {
		t.Fatalf("emission 2023: %v", err)
	}
	if emission2023.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("unexpected 2023 emission: %s", emission2023)
	}

	now = time.Date(2024, time.February, 1, 12, 0, 0, 0, time.UTC)
	deltaTwo := big.NewInt(300)
	totalDelta := new(big.Int).Add(new(big.Int).Set(deltaOne), deltaTwo)
	globalIndex := new(big.Int).Add(rewards.IndexUnit(), totalDelta)
	if err := manager.SetStakingGlobalIndex(globalIndex); err != nil {
		t.Fatalf("update global index: %v", err)
	}

	elapsed := uint64(now.Unix()) - afterFirst.StakeLastPayoutTs
	eligibleSeconds := uint64(stakePayoutPeriodSeconds)
	deltaIndex := new(big.Int).Sub(globalIndex, afterFirst.StakeLastIndex)
	eligibleIndexDelta := new(big.Int).Mul(deltaIndex, new(big.Int).SetUint64(eligibleSeconds))
	eligibleIndexDelta.Quo(eligibleIndexDelta, new(big.Int).SetUint64(elapsed))
	expectedSecond := new(big.Int).Mul(eligibleIndexDelta, afterFirst.StakeShares)

	mintedSecond, err := sp.StakeClaimRewards(delegator[:])
	if err != nil {
		t.Fatalf("second claim: %v", err)
	}
	if mintedSecond.Cmp(expectedSecond) != 0 {
		t.Fatalf("unexpected second mint: got %s want %s", mintedSecond, expectedSecond)
	}

	emission2024, err := manager.StakingEmissionYTD(2024)
	if err != nil {
		t.Fatalf("emission 2024: %v", err)
	}
	if emission2024.Cmp(expectedSecond) != 0 {
		t.Fatalf("unexpected 2024 emission: %s", emission2024)
	}
	emission2023, err = manager.StakingEmissionYTD(2023)
	if err != nil {
		t.Fatalf("emission 2023 reload: %v", err)
	}
	if emission2023.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("2023 emission changed: %s", emission2023)
	}

	evts := sp.Events()
	if len(evts) != 4 {
		t.Fatalf("unexpected event count: %d", len(evts))
	}
	last := evts[len(evts)-1]
	if last.Type != events.TypeStakeRewardsClaimedLegacy {
		t.Fatalf("unexpected last event: %s", last.Type)
	}
	if got := last.Attributes["minted"]; got != expectedSecond.String() {
		t.Fatalf("legacy minted mismatch: got %s want %s", got, expectedSecond)
	}
}
