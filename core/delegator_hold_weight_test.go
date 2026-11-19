package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/config"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/pos"
)

// A pending POS hold moves ZNHB into the payer's locked balance, and a delegator's
// share of its validator's staker pool was weighed by that whole balance. These
// tests cover DelegatorHoldWeightActivationHeight (ledger PL-EXT-25): from the
// height the weight is the locked balance less what the delegator's own pending
// holds keep. They use the fixture of the unstake guard's tests: the validator is
// funded by an honest delegator (5000) and by a holder that delegated 100 and put
// 6000 into a hold, so its locked balance is 6100.

const delegatorHoldWeightActivation = uint64(200)

func newDelegatorHoldWeightFixture(t *testing.T, activation uint64) *posGuardFixture {
	t.Helper()
	f := newPOSGuardFixture(t, 0, 100)
	f.sp.SetValidatorRules(ValidatorRules{DelegatorHoldWeightActivationHeight: activation})
	return f
}

// split pays amount through the validator's staker pool at height and returns what
// each wallet was credited.
func (f *posGuardFixture) split(height uint64, amount int64) map[[20]byte]int64 {
	f.t.Helper()
	f.atHeight(height, time.Second)
	rewardMap := make(map[string]*accountReward)
	f.sp.splitStakerReward(nhbstate.NewManager(f.sp.Trie), f.validator[:], big.NewInt(amount), rewardMap)
	got := make(map[[20]byte]int64)
	var total int64
	for key, reward := range rewardMap {
		var addr [20]byte
		copy(addr[:], key)
		got[addr] = reward.stakers.Int64()
		total += reward.stakers.Int64()
	}
	if total != amount {
		f.t.Fatalf("the split paid %d of %d: %v", total, amount, got)
	}
	return got
}

func (f *posGuardFixture) requireShares(got map[[20]byte]int64, honest, holder int64) {
	f.t.Helper()
	if got[f.honest] != honest || got[f.holder] != holder {
		f.t.Fatalf("honest delegator %d (want %d), holder %d (want %d): %v", got[f.honest], honest, got[f.holder], holder, got)
	}
}

// Off, by default and below the height, the weight is the whole locked balance, so
// the holder's 6000 hold counts as stake.
func TestDelegatorHoldWeightOffKeepsTheWholeLockedBalance(t *testing.T) {
	for _, tc := range []struct {
		name       string
		activation uint64
		height     uint64
	}{
		{"default", 0, 1_000_000},
		{"below the height", delegatorHoldWeightActivation, delegatorHoldWeightActivation - 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDelegatorHoldWeightFixture(t, tc.activation)
			f.requireLocked(f.holder, 6_100)
			f.requireShares(f.split(tc.height, 11_100), 5_000, 6_100)
		})
	}
}

// From the height the hold no longer counts: the holder is weighed at the 100 it
// delegated, and the honest delegator takes the rest.
func TestDelegatorHoldWeightOnExcludesThePendingHold(t *testing.T) {
	for _, height := range []uint64{delegatorHoldWeightActivation, delegatorHoldWeightActivation + 1, 5_000_000} {
		f := newDelegatorHoldWeightFixture(t, delegatorHoldWeightActivation)
		f.requireLocked(f.holder, 6_100)
		f.requireShares(f.split(height, 5_100), 5_000, 100)
	}
}

// Each delegator loses only what its own holds keep: another delegator's hold is
// not taken off its weight.
func TestDelegatorHoldWeightIsPerPayer(t *testing.T) {
	// other holds 1000 and then delegates 200, so its locked balance is 1200 and its
	// stake is 200.
	f := newDelegatorHoldWeightFixture(t, delegatorHoldWeightActivation)
	f.atHeight(150, time.Second)
	if _, err := f.sp.StakeDelegate(f.other[:], f.validator[:], big.NewInt(200)); err != nil {
		t.Fatalf("other delegates: %v", err)
	}
	f.requireLocked(f.other, 1_200)

	off := f.split(delegatorHoldWeightActivation-1, 12_300)
	if off[f.honest] != 5_000 || off[f.holder] != 6_100 || off[f.other] != 1_200 {
		t.Fatalf("before the height: %v, want 5000, 6100, 1200", off)
	}
	on := f.split(delegatorHoldWeightActivation, 5_300)
	if on[f.honest] != 5_000 || on[f.holder] != 100 || on[f.other] != 200 {
		t.Fatalf("from the height: %v, want 5000, 100, 200", on)
	}
}

// A delegator whose locked balance is no more than what its holds keep counts for
// nothing; it is never given a negative or a zero weight that takes from the others.
func TestDelegatorHoldWeightDelegatorWhoseBalanceIsAllHeld(t *testing.T) {
	for _, locked := range []int64{6_000, 5_000} {
		f := newDelegatorHoldWeightFixture(t, delegatorHoldWeightActivation)
		holder := f.account(f.holder)
		holder.LockedZNHB = big.NewInt(locked)
		writeAccount(t, f.sp, f.holder, holder)

		got := f.split(delegatorHoldWeightActivation, 5_000)
		f.requireShares(got, 5_000, 0)
		if _, ok := got[f.holder]; ok {
			t.Fatalf("a delegator with nothing but holds was credited (locked %d): %v", locked, got)
		}
	}
}

// When nobody in the pool has a weight left (every indexed delegator is held in full
// or has nothing locked, and the validator staked nothing itself) the pool is paid to
// the validator, as it always was for a validator without weights. It must not be
// left unpaid: the epoch's emission is accounted for to the last unit.
func TestDelegatorHoldWeightNobodyLeftWithAWeightPaysTheValidator(t *testing.T) {
	for _, height := range []uint64{delegatorHoldWeightActivation - 1, delegatorHoldWeightActivation} {
		f := newDelegatorHoldWeightFixture(t, delegatorHoldWeightActivation)
		holder := f.account(f.holder)
		holder.LockedZNHB = big.NewInt(6_000) // all of it held
		writeAccount(t, f.sp, f.holder, holder)
		honest := f.account(f.honest)
		honest.LockedZNHB = big.NewInt(0)
		writeAccount(t, f.sp, f.honest, honest)

		got := f.split(height, 1_000)
		if height < delegatorHoldWeightActivation {
			// Below the height the holder still counts for 6000 of it.
			if got[f.holder] != 1_000 {
				t.Fatalf("before the height the holder is the only weight: %v", got)
			}
			continue
		}
		if got[f.validator] != 1_000 || len(got) != 1 {
			t.Fatalf("from the height the validator is paid in full: %v", got)
		}
	}
}

// The holds are read only once the rule is on, and a read that fails pays the
// validator in full, as a failed lookup in the same function does. Every node reads
// the same state, so every node does the same.
func TestDelegatorHoldWeightReadFailure(t *testing.T) {
	f := newDelegatorHoldWeightFixture(t, delegatorHoldWeightActivation)
	// The pending index is a list; a number in its place cannot be read.
	if err := nhbstate.NewManager(f.sp.Trie).KVPut([]byte("pos/auth/pending"), uint64(7)); err != nil {
		t.Fatalf("corrupt the index: %v", err)
	}
	if _, err := pos.NewLifecycle(nhbstate.NewManager(f.sp.Trie)).PendingHeldByPayer(); err == nil {
		t.Fatalf("the corrupted index can still be read: the test would prove nothing")
	}

	// Below the height nothing is read, so the split is the old one.
	f.requireShares(f.split(delegatorHoldWeightActivation-1, 11_100), 5_000, 6_100)

	got := f.split(delegatorHoldWeightActivation, 1_000)
	if got[f.validator] != 1_000 || len(got) != 1 {
		t.Fatalf("a failed read must pay the validator in full: %v", got)
	}
}

// A hold that has been captured or voided is no longer pending and takes nothing
// off the weight; a delegator without a hold is untouched.
func TestDelegatorHoldWeightIgnoresSettledHolds(t *testing.T) {
	f := newDelegatorHoldWeightFixture(t, delegatorHoldWeightActivation)
	f.atHeight(150, time.Second)
	if _, err := f.lifecycle.Void(f.holderHold, "test", f.holder); err != nil {
		t.Fatalf("void: %v", err)
	}
	f.requireLocked(f.holder, 100)
	// Nothing is pending for the holder now, and the honest delegator never had a hold.
	f.requireShares(f.split(delegatorHoldWeightActivation, 5_100), 5_000, 100)
}

// With the validator's own stake in the pool (nobody else delegated to it) the rule
// changes nothing: there is no delegator to weigh.
func TestDelegatorHoldWeightLeavesAValidatorWithoutDelegatorsAlone(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetValidatorRules(ValidatorRules{DelegatorHoldWeightActivationHeight: delegatorHoldWeightActivation})
	var validator [20]byte
	validator[19] = 0xd1
	writeAccount(t, sp, validator, &types.Account{BalanceZNHB: big.NewInt(0)})
	sp.BeginBlock(delegatorHoldWeightActivation, time.Unix(1_700_000_000, 0).UTC())

	rewardMap := make(map[string]*accountReward)
	sp.splitStakerReward(nhbstate.NewManager(sp.Trie), validator[:], big.NewInt(777), rewardMap)
	if reward := rewardMap[string(validator[:])]; reward == nil || reward.stakers.Cmp(big.NewInt(777)) != 0 {
		t.Fatalf("a validator nobody delegated to was not paid in full: %v", rewardMap)
	}
}

// The rule is read from the setting of the same name, off when the setting is not
// there, and the processor reports what it was given.
func TestDelegatorHoldWeightRuleIsReadFromTheConfig(t *testing.T) {
	if got := ValidatorRulesFromConfig(&config.Config{}).DelegatorHoldWeightActivationHeight; got != 0 {
		t.Fatalf("rule with no setting = %d, want 0 (off)", got)
	}
	cfg := &config.Config{DelegatorHoldWeightActivationHeight: delegatorHoldWeightActivation}
	if got := ValidatorRulesFromConfig(cfg).DelegatorHoldWeightActivationHeight; got != delegatorHoldWeightActivation {
		t.Fatalf("rule read from the config = %d, want %d", got, delegatorHoldWeightActivation)
	}
	f := newDelegatorHoldWeightFixture(t, delegatorHoldWeightActivation)
	if got := f.sp.ValidatorRulesInForce().DelegatorHoldWeightActivationHeight; got != delegatorHoldWeightActivation {
		t.Fatalf("rule in force = %d, want %d", got, delegatorHoldWeightActivation)
	}
}
