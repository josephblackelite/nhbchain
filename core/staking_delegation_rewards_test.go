package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/epoch"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// TestStakeDelegate_ThirdParty_MaintainsDelegatorIndex covers the new index
// maintenance added 2026-08-13 alongside the delegator-reward-attribution
// fix: delegating to a different validator must register the delegator in
// that validator's index and add to its delegated-in total, and fully
// undelegating must remove it and zero the total back out.
func TestStakeDelegate_ThirdParty_MaintainsDelegatorIndex(t *testing.T) {
	sp := newStakingStateProcessor(t)
	var delegator, validator [20]byte
	delegator[19] = 0x60
	validator[19] = 0x61

	writeAccount(t, sp, delegator, &types.Account{BalanceZNHB: big.NewInt(5_000)})
	writeAccount(t, sp, validator, &types.Account{BalanceZNHB: big.NewInt(0)})

	if _, err := sp.StakeDelegate(delegator[:], validator[:], big.NewInt(3_000)); err != nil {
		t.Fatalf("delegate: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	delegators, err := manager.StakeValidatorDelegators(validator)
	if err != nil {
		t.Fatalf("load delegators: %v", err)
	}
	if len(delegators) != 1 || delegators[0] != delegator {
		t.Fatalf("expected delegator indexed under validator, got %#v", delegators)
	}
	total, err := manager.StakeValidatorDelegatedInTotal(validator)
	if err != nil {
		t.Fatalf("load delegated-in total: %v", err)
	}
	if total.Cmp(big.NewInt(3_000)) != 0 {
		t.Fatalf("expected delegated-in total 3000, got %s", total)
	}

	// Delegating again (topping up) must not duplicate the index entry.
	if _, err := sp.StakeDelegate(delegator[:], validator[:], big.NewInt(500)); err != nil {
		t.Fatalf("top up delegate: %v", err)
	}
	delegators, err = manager.StakeValidatorDelegators(validator)
	if err != nil {
		t.Fatalf("load delegators after top up: %v", err)
	}
	if len(delegators) != 1 {
		t.Fatalf("expected exactly one indexed delegator after top up, got %d", len(delegators))
	}
	total, err = manager.StakeValidatorDelegatedInTotal(validator)
	if err != nil {
		t.Fatalf("load delegated-in total after top up: %v", err)
	}
	if total.Cmp(big.NewInt(3_500)) != 0 {
		t.Fatalf("expected delegated-in total 3500 after top up, got %s", total)
	}

	if _, err := sp.StakeUndelegate(delegator[:], big.NewInt(3_500)); err != nil {
		t.Fatalf("undelegate: %v", err)
	}
	delegators, err = manager.StakeValidatorDelegators(validator)
	if err != nil {
		t.Fatalf("load delegators after undelegate: %v", err)
	}
	if len(delegators) != 0 {
		t.Fatalf("expected delegator removed from index after full undelegate, got %#v", delegators)
	}
	total, err = manager.StakeValidatorDelegatedInTotal(validator)
	if err != nil {
		t.Fatalf("load delegated-in total after undelegate: %v", err)
	}
	if total.Sign() != 0 {
		t.Fatalf("expected delegated-in total 0 after full undelegate, got %s", total)
	}
}

// Test_AccrualOnThirdPartyDelegation_CreditsDelegatorNotValidator covers the
// APR-based accrual system (accrueStakeAccount / stakeRewardBasis, fixed
// 2026-08-13). Before the fix, a validator's own account.Stake (which
// silently included everyone's delegated amount) was used directly, so 100%
// of accrual went to the validator regardless of whose capital it was. A
// validator contributing zero self-stake of its own must now accrue zero,
// while the delegator who contributed everything accrues the real reward.
func Test_AccrualOnThirdPartyDelegation_CreditsDelegatorNotValidator(t *testing.T) {
	sp := newStakingStateProcessor(t)
	var delegator, validator [20]byte
	delegator[19] = 0x62
	validator[19] = 0x63

	start := time.Unix(1_700_000_000, 0)
	sp.BeginBlock(1, start)
	if err := sp.SetStakeRewardAPR(1_000); err != nil {
		t.Fatalf("set apr: %v", err)
	}

	writeAccount(t, sp, delegator, &types.Account{BalanceZNHB: big.NewInt(10_000)})
	writeAccount(t, sp, validator, &types.Account{BalanceZNHB: big.NewInt(0)})

	if _, err := sp.StakeDelegate(delegator[:], validator[:], big.NewInt(100)); err != nil {
		t.Fatalf("delegate: %v", err)
	}

	// Advance one year and nudge both accounts (a partial undelegate forces
	// accrual crystallization on the delegator AND the validator, matching
	// Test_AccrualOnStakeTopUpAndUnstake's existing pattern for the
	// self-delegation case) so StakeShares reflects a full year of accrual.
	sp.BeginBlock(2, start.Add(365*24*time.Hour))
	if _, err := sp.StakeUndelegate(delegator[:], big.NewInt(40)); err != nil {
		t.Fatalf("partial undelegate: %v", err)
	}

	delegatorAcc, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get delegator: %v", err)
	}
	// 1000bps (10%) APR on a basis of 100 for one year == 10 shares, exactly
	// mirroring Test_AccrualOnStakeTopUpAndUnstake's expectation for an
	// identical self-delegation setup -- confirming the delegator's basis
	// (LockedZNHB) drives accrual exactly the way Stake would have.
	if got, want := delegatorAcc.StakeShares, big.NewInt(10); got.Cmp(want) != 0 {
		t.Fatalf("unexpected delegator shares: got %s want %s", got, want)
	}

	validatorAcc, err := sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator: %v", err)
	}
	if validatorAcc.StakeShares.Sign() != 0 {
		t.Fatalf("expected validator to accrue zero shares (contributed no self-stake), got %s", validatorAcc.StakeShares)
	}
}

// TestDistributeStakerRewards_SplitsBetweenValidatorAndDelegator covers the
// halving-schedule reward system (distributeStakerRewards / splitStakerReward,
// fixed 2026-08-13). A validator whose entire Stake is a single third-party
// delegation must receive none of the staker-pool share for that stake --
// it all goes to the delegator who actually contributed it.
func TestDistributeStakerRewards_SplitsBetweenValidatorAndDelegator(t *testing.T) {
	sp := newStakingStateProcessor(t)
	var delegator, validator [20]byte
	delegator[19] = 0x64
	validator[19] = 0x65

	writeAccount(t, sp, delegator, &types.Account{BalanceZNHB: big.NewInt(3_000)})
	writeAccount(t, sp, validator, &types.Account{BalanceZNHB: big.NewInt(0)})

	if _, err := sp.StakeDelegate(delegator[:], validator[:], big.NewInt(3_000)); err != nil {
		t.Fatalf("delegate: %v", err)
	}

	validatorAcc, err := sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator: %v", err)
	}
	weights := []epoch.Weight{{Address: append([]byte(nil), validator[:]...), Stake: new(big.Int).Set(validatorAcc.Stake)}}
	rewardMap := make(map[string]*accountReward)

	paid := sp.distributeStakerRewards(big.NewInt(1_000), weights, rewardMap)
	if paid.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected full 1000 distributed, got %s", paid)
	}
	if reward, ok := rewardMap[string(validator[:])]; ok {
		t.Fatalf("expected validator to receive nothing (zero own basis), got %s", reward.stakers)
	}
	delegatorReward, ok := rewardMap[string(delegator[:])]
	if !ok {
		t.Fatalf("expected delegator to receive a reward entry")
	}
	if delegatorReward.stakers.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected delegator to receive all 1000, got %s", delegatorReward.stakers)
	}
}

// TestDistributeStakerRewards_MixedSelfStakeAndDelegation covers the
// proportional split when a validator has both real self-stake and a
// third-party delegation -- each side must receive its own pro-rata share
// of the pool, not an all-or-nothing split.
func TestDistributeStakerRewards_MixedSelfStakeAndDelegation(t *testing.T) {
	sp := newStakingStateProcessor(t)
	var delegator, validator [20]byte
	delegator[19] = 0x66
	validator[19] = 0x67

	writeAccount(t, sp, delegator, &types.Account{BalanceZNHB: big.NewInt(2_500)})
	writeAccount(t, sp, validator, &types.Account{BalanceZNHB: big.NewInt(7_500)})

	// Validator self-stakes 7500, then receives a 2500 third-party
	// delegation -- an even-ish 3:1 split for easy proportional checking.
	if _, err := sp.StakeDelegate(validator[:], nil, big.NewInt(7_500)); err != nil {
		t.Fatalf("self stake: %v", err)
	}
	if _, err := sp.StakeDelegate(delegator[:], validator[:], big.NewInt(2_500)); err != nil {
		t.Fatalf("delegate: %v", err)
	}

	validatorAcc, err := sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator: %v", err)
	}
	if validatorAcc.Stake.Cmp(big.NewInt(10_000)) != 0 {
		t.Fatalf("expected combined validator stake 10000, got %s", validatorAcc.Stake)
	}

	weights := []epoch.Weight{{Address: append([]byte(nil), validator[:]...), Stake: new(big.Int).Set(validatorAcc.Stake)}}
	rewardMap := make(map[string]*accountReward)

	paid := sp.distributeStakerRewards(big.NewInt(10_000), weights, rewardMap)
	if paid.Cmp(big.NewInt(10_000)) != 0 {
		t.Fatalf("expected full 10000 distributed, got %s", paid)
	}
	validatorReward, ok := rewardMap[string(validator[:])]
	if !ok {
		t.Fatalf("expected validator to receive its own share")
	}
	if validatorReward.stakers.Cmp(big.NewInt(7_500)) != 0 {
		t.Fatalf("expected validator's own share 7500 (75%% of 10000), got %s", validatorReward.stakers)
	}
	delegatorReward, ok := rewardMap[string(delegator[:])]
	if !ok {
		t.Fatalf("expected delegator to receive its share")
	}
	if delegatorReward.stakers.Cmp(big.NewInt(2_500)) != 0 {
		t.Fatalf("expected delegator's share 2500 (25%% of 10000), got %s", delegatorReward.stakers)
	}
}

// TestBackfillStakeDelegationIndexOnce covers the one-time migration that
// populates the delegator index/total for delegations that predate the
// index existing at all (see stakeDelegationBackfillSeed's doc comment).
func TestBackfillStakeDelegationIndexOnce(t *testing.T) {
	sp := newStakingStateProcessor(t)
	var delegator, validator [20]byte
	delegator[19] = 0x68
	validator[19] = 0x69

	delegatorAddrStr := crypto.MustNewAddress(crypto.NHBPrefix, delegator[:]).String()
	validatorAddrStr := crypto.MustNewAddress(crypto.NHBPrefix, validator[:]).String()

	original := stakeDelegationBackfillSeed
	stakeDelegationBackfillSeed = []stakeDelegationBackfillEntry{
		{Delegator: delegatorAddrStr, Validator: validatorAddrStr},
	}
	t.Cleanup(func() { stakeDelegationBackfillSeed = original })

	// Simulate pre-fix committed state directly: the delegation itself is
	// already correctly recorded (DelegatedValidator/LockedZNHB on the
	// delegator, Stake already including it on the validator) -- exactly
	// what StakeDelegate produced before this fix -- but the new index/
	// total don't exist yet, since nothing ever wrote them.
	writeAccount(t, sp, delegator, &types.Account{
		BalanceZNHB:        big.NewInt(0),
		LockedZNHB:         big.NewInt(500),
		DelegatedValidator: append([]byte(nil), validator[:]...),
	})
	writeAccount(t, sp, validator, &types.Account{
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(500),
	})

	if err := sp.BackfillStakeDelegationIndexOnce(); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	delegators, err := manager.StakeValidatorDelegators(validator)
	if err != nil {
		t.Fatalf("load delegators: %v", err)
	}
	if len(delegators) != 1 || delegators[0] != delegator {
		t.Fatalf("expected delegator backfilled into index, got %#v", delegators)
	}
	total, err := manager.StakeValidatorDelegatedInTotal(validator)
	if err != nil {
		t.Fatalf("load delegated-in total: %v", err)
	}
	if total.Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("expected backfilled delegated-in total 500, got %s", total)
	}

	// Backfilling makes distributeStakerRewards correctly exclude the
	// validator's own (zero) basis immediately, no further action needed.
	weights := []epoch.Weight{{Address: append([]byte(nil), validator[:]...), Stake: big.NewInt(500)}}
	rewardMap := make(map[string]*accountReward)
	sp.distributeStakerRewards(big.NewInt(100), weights, rewardMap)
	if _, ok := rewardMap[string(validator[:])]; ok {
		t.Fatalf("expected validator to receive nothing post-backfill (zero own basis)")
	}
	if reward, ok := rewardMap[string(delegator[:])]; !ok || reward.stakers.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("expected delegator to receive full 100 post-backfill, got %#v", rewardMap[string(delegator[:])])
	}

	// Idempotency: running again must not duplicate the index entry or
	// double the total, since the guard flag is now set.
	if err := sp.BackfillStakeDelegationIndexOnce(); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	delegators, err = manager.StakeValidatorDelegators(validator)
	if err != nil {
		t.Fatalf("load delegators after second backfill: %v", err)
	}
	if len(delegators) != 1 {
		t.Fatalf("expected backfill to remain idempotent, got %d delegators", len(delegators))
	}
	total, err = manager.StakeValidatorDelegatedInTotal(validator)
	if err != nil {
		t.Fatalf("load delegated-in total after second backfill: %v", err)
	}
	if total.Cmp(big.NewInt(500)) != 0 {
		t.Fatalf("expected delegated-in total to remain 500 after second backfill, got %s", total)
	}
}

// TestBackfillStakeDelegationIndexOnce_SkipsChangedDelegation confirms the
// migration re-validates each seed entry against live state and skips it
// (rather than force-applying stale data) if the delegation has since
// changed or ended.
func TestBackfillStakeDelegationIndexOnce_SkipsChangedDelegation(t *testing.T) {
	sp := newStakingStateProcessor(t)
	var delegator, validator, otherValidator [20]byte
	delegator[19] = 0x6A
	validator[19] = 0x6B
	otherValidator[19] = 0x6C

	delegatorAddrStr := crypto.MustNewAddress(crypto.NHBPrefix, delegator[:]).String()
	validatorAddrStr := crypto.MustNewAddress(crypto.NHBPrefix, validator[:]).String()

	original := stakeDelegationBackfillSeed
	stakeDelegationBackfillSeed = []stakeDelegationBackfillEntry{
		{Delegator: delegatorAddrStr, Validator: validatorAddrStr},
	}
	t.Cleanup(func() { stakeDelegationBackfillSeed = original })

	// The seed claims delegator -> validator, but live state now shows the
	// delegation has since moved to otherValidator entirely.
	writeAccount(t, sp, delegator, &types.Account{
		BalanceZNHB:        big.NewInt(0),
		LockedZNHB:         big.NewInt(300),
		DelegatedValidator: append([]byte(nil), otherValidator[:]...),
	})

	if err := sp.BackfillStakeDelegationIndexOnce(); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	delegators, err := manager.StakeValidatorDelegators(validator)
	if err != nil {
		t.Fatalf("load delegators: %v", err)
	}
	if len(delegators) != 0 {
		t.Fatalf("expected stale seed entry to be skipped, got %#v", delegators)
	}
}
