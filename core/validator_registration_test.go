package core

import (
	"bytes"
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/epoch"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/native/potso"
)

func mustEncodeStakePayload(t *testing.T, payload stakePayload) []byte {
	t.Helper()
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode stake payload: %v", err)
	}
	return data
}

func mustEncodeUnstakePayload(t *testing.T, payload unstakePayload) []byte {
	t.Helper()
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode unstake payload: %v", err)
	}
	return data
}

// (a) TestApplyStake_PlainSelfStake_DoesNotRegisterValidator proves the
// core regression this whole redesign fixes (task #94's incident class): a
// plain TxTypeStake self-stake -- exactly nhbportal's /stake page flow,
// which sends no `data` field at all -- must NOT make the account
// validator-eligible even though its stake clears minimumValidatorStake().
func TestApplyStake_PlainSelfStake_DoesNotRegisterValidator(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(5_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	var staker [20]byte
	staker[19] = 0x80
	writeAccount(t, sp, staker, &types.Account{BalanceZNHB: big.NewInt(20_000)})

	tx := &types.Transaction{Value: big.NewInt(10_000)}
	if err := sp.applyStake(tx, staker[:]); err != nil {
		t.Fatalf("apply stake: %v", err)
	}

	acct, err := sp.getAccount(staker[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if acct.Stake.Cmp(big.NewInt(10_000)) != 0 {
		t.Fatalf("expected stake 10000, got %s", acct.Stake)
	}
	if acct.ValidatorRegistered {
		t.Fatalf("plain self-stake must not set ValidatorRegistered")
	}
	if _, ok := sp.EligibleValidators[string(staker[:])]; ok {
		t.Fatalf("unregistered account with qualifying stake must NOT appear in EligibleValidators")
	}
	if _, ok := sp.ValidatorSet[string(staker[:])]; ok {
		t.Fatalf("unregistered account with qualifying stake must NOT appear in ValidatorSet")
	}

	// Re-staking further (e.g. topping up) must still not confer eligibility.
	tx2 := &types.Transaction{Value: big.NewInt(5_000)}
	if err := sp.applyStake(tx2, staker[:]); err != nil {
		t.Fatalf("apply second stake: %v", err)
	}
	if _, ok := sp.EligibleValidators[string(staker[:])]; ok {
		t.Fatalf("topping up an unregistered self-stake must not confer eligibility")
	}
}

// TestApplyStake_RegisterValidator_CombinedWithSelfStake covers the common
// real-world path: registering AND self-staking the initial threshold
// amount in a single TxTypeStake transaction.
func TestApplyStake_RegisterValidator_CombinedWithSelfStake(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(5_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	var staker [20]byte
	staker[19] = 0x81
	writeAccount(t, sp, staker, &types.Account{BalanceZNHB: big.NewInt(20_000)})

	tx := &types.Transaction{
		Value: big.NewInt(6_000),
		Data:  mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
	}
	if err := sp.applyStake(tx, staker[:]); err != nil {
		t.Fatalf("apply stake+register: %v", err)
	}

	acct, err := sp.getAccount(staker[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if !acct.ValidatorRegistered {
		t.Fatalf("expected ValidatorRegistered=true")
	}
	if acct.ValidatorRegisteredAt == 0 {
		t.Fatalf("expected ValidatorRegisteredAt to be set")
	}
	basis, ok := sp.EligibleValidators[string(staker[:])]
	if !ok || basis.Cmp(big.NewInt(6_000)) != 0 {
		t.Fatalf("expected eligibility basis 6000, got %v (present=%v)", basis, ok)
	}
}

// TestApplyStake_RegisterValidator_PureRegistrationNoValue covers the
// "already sufficiently self-staked, just flip the flag" case: a
// RegisterValidator=true call with zero tx.Value must not require (or
// reject) a stake delta, and an account that already carries sufficient
// self-stake becomes eligible immediately.
func TestApplyStake_RegisterValidator_PureRegistrationNoValue(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(5_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	var staker [20]byte
	staker[19] = 0x82
	// Already self-staked via a plain (unregistered) stake tx first.
	writeAccount(t, sp, staker, &types.Account{BalanceZNHB: big.NewInt(20_000)})
	if err := sp.applyStake(&types.Transaction{Value: big.NewInt(8_000)}, staker[:]); err != nil {
		t.Fatalf("initial plain stake: %v", err)
	}
	if _, ok := sp.EligibleValidators[string(staker[:])]; ok {
		t.Fatalf("precondition failed: should not be eligible before registering")
	}

	pureRegisterTx := &types.Transaction{
		Value: big.NewInt(0),
		Data:  mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
	}
	if err := sp.applyStake(pureRegisterTx, staker[:]); err != nil {
		t.Fatalf("pure register: %v", err)
	}

	acct, err := sp.getAccount(staker[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if !acct.ValidatorRegistered {
		t.Fatalf("expected ValidatorRegistered=true after pure-registration call")
	}
	if acct.Stake.Cmp(big.NewInt(8_000)) != 0 {
		t.Fatalf("pure registration must not alter stake: got %s want 8000", acct.Stake)
	}
	basis, ok := sp.EligibleValidators[string(staker[:])]
	if !ok || basis.Cmp(big.NewInt(8_000)) != 0 {
		t.Fatalf("expected eligibility basis 8000 after pure registration, got %v (present=%v)", basis, ok)
	}
}

// TestApplyStake_RegisterValidator_RejectsThirdPartyTarget proves
// registerValidator is only valid for self-stake -- a delegator can never
// smuggle validator eligibility for someone else's address onto themself or
// vice versa.
func TestApplyStake_RegisterValidator_RejectsThirdPartyTarget(t *testing.T) {
	sp := newStakingStateProcessor(t)
	var delegator, validator [20]byte
	delegator[19] = 0x83
	validator[19] = 0x84
	writeAccount(t, sp, delegator, &types.Account{BalanceZNHB: big.NewInt(20_000)})

	tx := &types.Transaction{
		Value: big.NewInt(6_000),
		Data:  mustEncodeStakePayload(t, stakePayload{Validator: append([]byte(nil), validator[:]...), RegisterValidator: true}),
	}
	if err := sp.applyStake(tx, delegator[:]); err == nil {
		t.Fatalf("expected error registering validator status via a third-party delegation target")
	}
}

// TestApplyUnstake_DeregisterValidator_PureZeroValue is the "no way out"
// regression test: an account that has already fully unstaked (no active
// delegation, zero LockedZNHB) could never submit a value-bearing
// TxTypeUnstake again -- the zero-value pure-deregister path must still let
// it flip ValidatorRegistered back off.
func TestApplyUnstake_DeregisterValidator_PureZeroValue(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(5_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	var staker [20]byte
	staker[19] = 0x85
	writeAccount(t, sp, staker, &types.Account{BalanceZNHB: big.NewInt(20_000)})
	if err := sp.applyStake(&types.Transaction{
		Value: big.NewInt(6_000),
		Data:  mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
	}, staker[:]); err != nil {
		t.Fatalf("register+stake: %v", err)
	}

	// Fully unstake everything (ordinary value-bearing unstake, no
	// deregister flag) -- this clears DelegatedValidator/LockedZNHB,
	// matching StakeUndelegate's documented zero-LockedZNHB behavior.
	if err := sp.applyUnstake(&types.Transaction{Value: big.NewInt(6_000)}, staker[:]); err != nil {
		t.Fatalf("full unstake: %v", err)
	}
	acctAfterUnstake, err := sp.getAccount(staker[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if !acctAfterUnstake.ValidatorRegistered {
		t.Fatalf("precondition failed: unstaking alone must not clear ValidatorRegistered")
	}
	if len(acctAfterUnstake.DelegatedValidator) != 0 {
		t.Fatalf("precondition failed: expected no active delegation after full unstake")
	}

	// A value-bearing unstake now must fail (nothing left to unstake) --
	// confirming the pure zero-value path is the ONLY way out from here.
	if err := sp.applyUnstake(&types.Transaction{Value: big.NewInt(1)}, staker[:]); err == nil {
		t.Fatalf("expected value-bearing unstake to fail with nothing left staked")
	}

	pureDeregisterTx := &types.Transaction{
		Value: big.NewInt(0),
		Data:  mustEncodeUnstakePayload(t, unstakePayload{DeregisterValidator: true}),
	}
	if err := sp.applyUnstake(pureDeregisterTx, staker[:]); err != nil {
		t.Fatalf("pure deregister: %v", err)
	}

	finalAcct, err := sp.getAccount(staker[:])
	if err != nil {
		t.Fatalf("get final account: %v", err)
	}
	if finalAcct.ValidatorRegistered {
		t.Fatalf("expected ValidatorRegistered=false after pure deregistration")
	}
	if _, ok := sp.EligibleValidators[string(staker[:])]; ok {
		t.Fatalf("deregistered account must not remain in EligibleValidators")
	}
}

// (b) TestValidatorVotingPower_ExcludesDelegatedIn is the crux test for
// item 2: a validator's eligibility basis AND its live BFT ValidatorSet
// voting-power entry must equal its OWN stake basis (self-stake minus
// delegated-in), not the combined total inflated by a real third-party
// delegation -- with the delegator's reward-share attribution (untouched)
// deliberately not asserted here since that is covered by
// core/staking_delegation_rewards_test.go.
func TestValidatorVotingPower_ExcludesDelegatedIn(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(5_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}
	cfg := sp.EpochConfig()
	cfg.Length = 1
	cfg.StakeWeight = 1
	cfg.EngagementWeight = 0
	cfg.SnapshotHistory = 4
	if err := sp.SetEpochConfig(cfg); err != nil {
		t.Fatalf("set epoch config: %v", err)
	}

	var validator, delegator [20]byte
	validator[19] = 0x70
	delegator[19] = 0x71

	writeAccount(t, sp, validator, &types.Account{BalanceZNHB: big.NewInt(10_000)})
	writeAccount(t, sp, delegator, &types.Account{BalanceZNHB: big.NewInt(50_000)})

	// Validator registers AND self-stakes 6000 in a single TxTypeStake --
	// the common real-world path.
	if err := sp.applyStake(&types.Transaction{
		Value: big.NewInt(6_000),
		Data:  mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
	}, validator[:]); err != nil {
		t.Fatalf("register+self-stake: %v", err)
	}

	basisBefore, ok := sp.EligibleValidators[string(validator[:])]
	if !ok || basisBefore.Cmp(big.NewInt(6_000)) != 0 {
		t.Fatalf("expected own-basis-only eligibility of 6000 before delegation, got %v (present=%v)", basisBefore, ok)
	}

	// A real third-party delegation of 40000 into the validator.
	if _, err := sp.StakeDelegate(delegator[:], validator[:], big.NewInt(40_000)); err != nil {
		t.Fatalf("delegate: %v", err)
	}

	updatedValidator, err := sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator: %v", err)
	}
	if updatedValidator.Stake.Cmp(big.NewInt(46_000)) != 0 {
		t.Fatalf("expected combined validator stake 46000, got %s", updatedValidator.Stake)
	}

	basisAfter, ok := sp.EligibleValidators[string(validator[:])]
	if !ok {
		t.Fatalf("expected validator to remain eligible after delegation")
	}
	if basisAfter.Cmp(big.NewInt(6_000)) != 0 {
		t.Fatalf("eligibility basis must equal own stake only (6000), not combined total (46000): got %s", basisAfter)
	}

	// The delegator itself must never become validator-eligible.
	if _, ok := sp.EligibleValidators[string(delegator[:])]; ok {
		t.Fatalf("delegator must never become validator-eligible merely by delegating")
	}

	// Give the validator a heartbeat matching the block timestamp, finalize
	// an epoch, and assert the LIVE BFT ValidatorSet entry -- the literal
	// quorum weight consensus/bft/bft.go's recalculateVotingPowerLocked
	// sums via core/node.go's GetValidatorSet -- is also exactly the own
	// basis, not the combined total.
	const epochTimestamp = 1_700_900_000
	heartbeatAcct, err := sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator for heartbeat: %v", err)
	}
	heartbeatAcct.EngagementLastHeartbeat = uint64(epochTimestamp)
	if err := sp.setAccount(validator[:], heartbeatAcct); err != nil {
		t.Fatalf("set validator heartbeat: %v", err)
	}

	if err := sp.ProcessBlockLifecycle(1, epochTimestamp); err != nil {
		t.Fatalf("process block lifecycle: %v", err)
	}

	votingPower, ok := sp.ValidatorSet[string(validator[:])]
	if !ok {
		t.Fatalf("expected validator in active ValidatorSet after epoch finalization")
	}
	if votingPower.Cmp(big.NewInt(6_000)) != 0 {
		t.Fatalf("BFT voting power must equal own basis 6000, not combined total 46000: got %s", votingPower)
	}

	if _, err := manager.StakeValidatorDelegatedInTotal(validator); err != nil {
		t.Fatalf("sanity-check delegated-in total: %v", err)
	}
}

// (c) TestBackfillValidatorRegistrationOnce_GrandfathersActiveSetAndIsIdempotent
// covers item 5's migration: on first post-deploy run, every address
// CURRENTLY in ValidatorSet (real, live consensus membership) must be
// grandfathered as ValidatorRegistered, while entries that only ever sat in
// the broader EligibleValidators map (potential phantom-eligibility, e.g.
// task #94's incident class) must NOT be grandfathered. A second run must
// be a true no-op.
func TestBackfillValidatorRegistrationOnce_GrandfathersActiveSetAndIsIdempotent(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(1_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	var active, phantomEligible [20]byte
	active[19] = 0x90
	phantomEligible[19] = 0x91

	// Simulate the pre-fix world: `active` is a real, live validator
	// sitting in ValidatorSet (as the old automatic-by-stake mechanism
	// would have put it there), with ValidatorRegistered still false since
	// that field is brand new. `phantomEligible` sits only in the broader
	// EligibleValidators map, never selected into live consensus -- exactly
	// the class of entry item 5 says must NOT be grandfathered.
	writeAccount(t, sp, active, &types.Account{BalanceZNHB: big.NewInt(0), Stake: big.NewInt(20_000)})
	writeAccount(t, sp, phantomEligible, &types.Account{BalanceZNHB: big.NewInt(0), Stake: big.NewInt(20_000)})
	sp.ValidatorSet = map[string]*big.Int{string(active[:]): big.NewInt(20_000)}
	sp.EligibleValidators = map[string]*big.Int{
		string(active[:]):          big.NewInt(20_000),
		string(phantomEligible[:]): big.NewInt(20_000),
	}

	if err := sp.BackfillValidatorRegistrationOnce(); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	activeAcct, err := sp.getAccount(active[:])
	if err != nil {
		t.Fatalf("get active account: %v", err)
	}
	if !activeAcct.ValidatorRegistered {
		t.Fatalf("expected active ValidatorSet member to be grandfathered as registered")
	}
	if activeAcct.ValidatorRegisteredAt == 0 {
		t.Fatalf("expected ValidatorRegisteredAt to be set on grandfather")
	}
	if _, ok := sp.ValidatorSet[string(active[:])]; !ok {
		t.Fatalf("expected active validator to remain in ValidatorSet after grandfathering (no chain halt)")
	}

	phantomAcct, err := sp.getAccount(phantomEligible[:])
	if err != nil {
		t.Fatalf("get phantom account: %v", err)
	}
	if phantomAcct.ValidatorRegistered {
		t.Fatalf("expected EligibleValidators-only phantom entry to NOT be grandfathered")
	}

	backfilled, err := manager.ValidatorRegistrationBackfilled()
	if err != nil {
		t.Fatalf("check backfill flag: %v", err)
	}
	if !backfilled {
		t.Fatalf("expected guard flag set after first run")
	}

	firstRegisteredAt := activeAcct.ValidatorRegisteredAt

	// Idempotency: running again must be a true no-op.
	if err := sp.BackfillValidatorRegistrationOnce(); err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	activeAcctAgain, err := sp.getAccount(active[:])
	if err != nil {
		t.Fatalf("get active account after second run: %v", err)
	}
	if activeAcctAgain.ValidatorRegisteredAt != firstRegisteredAt {
		t.Fatalf("expected idempotent second run to leave ValidatorRegisteredAt unchanged: got %d want %d", activeAcctAgain.ValidatorRegisteredAt, firstRegisteredAt)
	}
	phantomAcctAgain, err := sp.getAccount(phantomEligible[:])
	if err != nil {
		t.Fatalf("get phantom account after second run: %v", err)
	}
	if phantomAcctAgain.ValidatorRegistered {
		t.Fatalf("second run must not retroactively grandfather the phantom entry either")
	}
}

// TestSetAccount_TransitionalGrandfather_SameBlockOrdering covers the
// same-block ordering hazard called out in the migration design: a
// not-yet-migrated ValidatorSet member touched by an ordinary setAccount
// call (e.g. its own heartbeat) BEFORE BackfillValidatorRegistrationOnce
// has run this block must still be protected immediately, not evicted.
func TestSetAccount_TransitionalGrandfather_SameBlockOrdering(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(1_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	var active [20]byte
	active[19] = 0x92
	writeAccount(t, sp, active, &types.Account{BalanceZNHB: big.NewInt(0), Stake: big.NewInt(20_000)})
	sp.ValidatorSet = map[string]*big.Int{string(active[:]): big.NewInt(20_000)}
	sp.EligibleValidators = map[string]*big.Int{string(active[:]): big.NewInt(20_000)}

	// Migration guard flag deliberately NOT set yet -- simulates the very
	// first block after deploy, before BackfillValidatorRegistrationOnce
	// has run this block.
	backfilled, err := manager.ValidatorRegistrationBackfilled()
	if err != nil || backfilled {
		t.Fatalf("precondition failed: expected migration not yet run, ok=%v err=%v", !backfilled, err)
	}

	// A heartbeat touches the account via ordinary setAccount, exactly what
	// would happen from an earlier transaction in the same block.
	acct, err := sp.getAccount(active[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	acct.EngagementLastHeartbeat = 12345
	if err := sp.setAccount(active[:], acct); err != nil {
		t.Fatalf("set account (simulated heartbeat): %v", err)
	}

	if _, ok := sp.ValidatorSet[string(active[:])]; !ok {
		t.Fatalf("active validator must not be evicted by an ordinary setAccount call before the migration runs this block")
	}
	reloaded, err := sp.getAccount(active[:])
	if err != nil {
		t.Fatalf("reload account: %v", err)
	}
	if !reloaded.ValidatorRegistered {
		t.Fatalf("expected setAccount's transitional grandfather to self-heal the registration flag immediately")
	}

	// The later migration pass this block must now be a no-op for it.
	if err := sp.BackfillValidatorRegistrationOnce(); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if _, ok := sp.ValidatorSet[string(active[:])]; !ok {
		t.Fatalf("active validator must remain in ValidatorSet after the migration's later pass")
	}
}

// TestValidatorReadyForActivation_RequiresRegistration covers item 4: an
// unregistered account's heartbeat must have zero effect on validator
// liveness/readiness, closing the "generic engagement field accidentally
// serves as validator-liveness proof" gap via the new registration gate
// itself.
func TestValidatorReadyForActivation_RequiresRegistration(t *testing.T) {
	sp := newStakingStateProcessor(t)
	now := time.Unix(1_700_950_000, 0).UTC()

	unregistered := &types.Account{
		EngagementLastHeartbeat: uint64(now.Unix()),
		ValidatorRegistered:     false,
	}
	if sp.validatorReadyForActivation(unregistered, now) {
		t.Fatalf("expected unregistered account with a fresh heartbeat to NOT be ready for activation")
	}

	registered := &types.Account{
		EngagementLastHeartbeat: uint64(now.Unix()),
		ValidatorRegistered:     true,
	}
	if !sp.validatorReadyForActivation(registered, now) {
		t.Fatalf("expected registered account with a fresh heartbeat to be ready for activation")
	}
}

// (d) TestGovernanceCastVote_RegisteredValidatorVotesWithoutSeparateRegistration
// proves item 3: a registered validator (per item 1, meeting the own-basis
// threshold) is an instant, automatic governance voting member -- no
// separate governance-specific registration or threshold -- by folding into
// the POTSO weight snapshot governance's CastVote actually reads, and then
// successfully casting a real vote through the governance engine.
func TestGovernanceCastVote_RegisteredValidatorVotesWithoutSeparateRegistration(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(1_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	rewardCfg := potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(900),
		TreasuryAddress:    sp.potsoRewardConfig.TreasuryAddress,
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
	}
	if err := sp.SetPotsoRewardConfig(rewardCfg); err != nil {
		t.Fatalf("set potso reward config: %v", err)
	}
	treasuryAcc, err := manager.GetAccount(rewardCfg.TreasuryAddress[:])
	if err != nil {
		t.Fatalf("load treasury: %v", err)
	}
	treasuryAcc.BalanceZNHB = big.NewInt(900)
	if err := manager.PutAccount(rewardCfg.TreasuryAddress[:], treasuryAcc); err != nil {
		t.Fatalf("fund treasury: %v", err)
	}

	// A registered validator with own-basis stake -- deliberately NO POTSO
	// stake-lock (TxTypePotsoStakeLock) and NO governance-specific
	// registration of any kind.
	var validator [20]byte
	validator[19] = 0xA1
	writeAccount(t, sp, validator, &types.Account{BalanceZNHB: big.NewInt(5_000)})
	if err := sp.applyStake(&types.Transaction{
		Value: big.NewInt(2_000),
		Data:  mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
	}, validator[:]); err != nil {
		t.Fatalf("register validator: %v", err)
	}

	now := time.Unix(1_700_600_000, 0).UTC()
	sp.nowFunc = func() time.Time { return now }

	if err := sp.ProcessBlockLifecycle(1, now.Add(-time.Second).Unix()); err != nil {
		t.Fatalf("process block 1: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(2, now.Unix()); err != nil {
		t.Fatalf("process block 2: %v", err)
	}

	lastProcessed, ok, err := manager.PotsoRewardsLastProcessedEpoch()
	if err != nil {
		t.Fatalf("last processed epoch: %v", err)
	}
	if !ok {
		t.Fatalf("expected a processed potso epoch")
	}

	govSnapshot, ok, err := manager.SnapshotPotsoWeights(lastProcessed)
	if err != nil {
		t.Fatalf("snapshot potso weights: %v", err)
	}
	if !ok || govSnapshot == nil {
		t.Fatalf("expected a governance-facing potso weight snapshot")
	}
	var sawValidator bool
	for _, entry := range govSnapshot.Entries {
		if entry.Address == validator {
			sawValidator = true
			if entry.WeightBps == 0 {
				t.Fatalf("expected nonzero governance voting weight for the registered validator")
			}
		}
	}
	if !sawValidator {
		t.Fatalf("expected the registered validator to appear in the governance weight snapshot with no separate registration")
	}

	// Now actually cast a vote through the real governance engine.
	sp.SetGovernancePolicy(governance.ProposalPolicy{
		MinDepositWei:       big.NewInt(0),
		VotingPeriodSeconds: 1000,
		TimelockSeconds:     1000,
		QuorumBps:           1,
		PassThresholdBps:    5000,
	})

	id, err := manager.GovernanceNextProposalID()
	if err != nil {
		t.Fatalf("next proposal id: %v", err)
	}
	proposal := &governance.Proposal{
		ID:          id,
		Submitter:   crypto.MustNewAddress(crypto.NHBPrefix, validator[:]),
		Status:      governance.ProposalStatusVotingPeriod,
		Deposit:     big.NewInt(0),
		SubmitTime:  now.Add(-time.Hour),
		VotingStart: now.Add(-time.Hour),
		VotingEnd:   now.Add(time.Hour),
		TimelockEnd: now.Add(2 * time.Hour),
		Target:      governance.ProposalKindParamUpdate,
	}
	if err := manager.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}

	engine := sp.governanceEngine()
	if err := engine.CastVote(id, validator, "yes"); err != nil {
		t.Fatalf("expected registered validator to vote with no separate governance registration, got error: %v", err)
	}

	vote, ok, err := manager.GovernanceGetVote(id, validator[:])
	if err != nil {
		t.Fatalf("get vote: %v", err)
	}
	if !ok || vote == nil {
		t.Fatalf("expected the cast vote to be persisted")
	}
	if vote.PowerBps == 0 {
		t.Fatalf("expected nonzero recorded voting power")
	}
}

// (e) TestValidatorEligibility_ExcludesAddressDelegatedAwayToDifferentValidator
// reproduces the self-delegation eligibility gap end to end: an address that
// (1) registers and self-stakes as a validator, (2) fully self-unstakes via
// an ordinary TxTypeUnstake -- no DeregisterValidator flag, explicitly
// expected/encouraged under this design (see
// TestApplyUnstake_DeregisterValidator_PureZeroValue's doc comment: "unstaking
// alone must not clear ValidatorRegistered") -- and (3) then does something
// completely ordinary and unrelated to being a validator, delegating to a
// DIFFERENT validator via a plain TxTypeStake (available to any account),
// must NOT thereby become validator-eligible or gain BFT voting power backed
// by money it has delegated AWAY, not staked on its own behalf. Without the
// selfDelegated gate, stakeRewardBasis(A, A) reads back A.LockedZNHB (the
// amount delegated to B) once A.DelegatedValidator != A, and setAccount's
// meetsStake check (registered && basis>=minStake) is satisfied by that
// misattributed figure.
func TestValidatorEligibility_ExcludesAddressDelegatedAwayToDifferentValidator(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(5_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}
	cfg := sp.EpochConfig()
	cfg.Length = 1
	cfg.StakeWeight = 1
	cfg.EngagementWeight = 0
	cfg.SnapshotHistory = 4
	if err := sp.SetEpochConfig(cfg); err != nil {
		t.Fatalf("set epoch config: %v", err)
	}

	var a, b [20]byte
	a[19] = 0xB1
	b[19] = 0xB2
	writeAccount(t, sp, a, &types.Account{BalanceZNHB: big.NewInt(30_000)})
	writeAccount(t, sp, b, &types.Account{BalanceZNHB: big.NewInt(10_000)})

	// Step 1: A registers and self-stakes 6000.
	if err := sp.applyStake(&types.Transaction{
		Value: big.NewInt(6_000),
		Data:  mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
	}, a[:]); err != nil {
		t.Fatalf("register+self-stake: %v", err)
	}
	if _, ok := sp.EligibleValidators[string(a[:])]; !ok {
		t.Fatalf("precondition failed: expected A eligible after self-stake+register")
	}

	// Step 2: A fully self-unstakes via an ordinary TxTypeUnstake (no
	// DeregisterValidator flag) -- explicitly expected/encouraged.
	if err := sp.applyUnstake(&types.Transaction{Value: big.NewInt(6_000)}, a[:]); err != nil {
		t.Fatalf("full self-unstake: %v", err)
	}
	acctAfterUnstake, err := sp.getAccount(a[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if !acctAfterUnstake.ValidatorRegistered {
		t.Fatalf("precondition failed: unstaking alone must not clear ValidatorRegistered")
	}
	if len(acctAfterUnstake.DelegatedValidator) != 0 {
		t.Fatalf("precondition failed: expected no active delegation after full unstake")
	}
	if _, ok := sp.EligibleValidators[string(a[:])]; ok {
		t.Fatalf("precondition failed: A must not be eligible with zero own stake")
	}

	// Step 3: A does something completely ordinary and unrelated to being a
	// validator -- delegates 10000 to a DIFFERENT validator B.
	if _, err := sp.StakeDelegate(a[:], b[:], big.NewInt(10_000)); err != nil {
		t.Fatalf("delegate to different validator: %v", err)
	}

	acctAfterDelegate, err := sp.getAccount(a[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if !acctAfterDelegate.ValidatorRegistered {
		t.Fatalf("precondition failed: delegating elsewhere must not itself clear ValidatorRegistered")
	}
	if acctAfterDelegate.LockedZNHB.Cmp(big.NewInt(10_000)) != 0 {
		t.Fatalf("precondition failed: expected LockedZNHB 10000 after delegating to B, got %s", acctAfterDelegate.LockedZNHB)
	}

	// THE BUG: without the self-delegation gate, A would now appear in
	// EligibleValidators with basis=10000 (LockedZNHB, the amount delegated
	// AWAY to B), purely because ValidatorRegistered was never cleared.
	if basis, ok := sp.EligibleValidators[string(a[:])]; ok {
		t.Fatalf("A must NOT be validator-eligible while its stake is delegated away to a different validator, got basis %s", basis)
	}

	// Confirm via the live BFT ValidatorSet after a real epoch finalization
	// -- exactly the quorum weight consensus/bft.go's
	// recalculateVotingPowerLocked sums via core/node.go's GetValidatorSet.
	heartbeatAcct, err := sp.getAccount(a[:])
	if err != nil {
		t.Fatalf("get account for heartbeat: %v", err)
	}
	const epochTimestamp = 1_700_900_000
	heartbeatAcct.EngagementLastHeartbeat = uint64(epochTimestamp)
	if err := sp.setAccount(a[:], heartbeatAcct); err != nil {
		t.Fatalf("set heartbeat: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(1, epochTimestamp); err != nil {
		t.Fatalf("process block lifecycle: %v", err)
	}
	if power, ok := sp.ValidatorSet[string(a[:])]; ok {
		t.Fatalf("A must NOT gain BFT voting power backed by stake delegated away to B, got power %s", power)
	}

	// And confirm A is excluded from the epoch snapshot's weight set too
	// (computeEpochWeights' own independent gate, which feeds reward
	// settlement) -- not just the live ValidatorSet.
	if len(sp.epochHistory) == 0 {
		t.Fatalf("expected an epoch snapshot to have been recorded")
	}
	latest := sp.epochHistory[len(sp.epochHistory)-1]
	for _, w := range latest.Weights {
		if bytes.Equal(w.Address, a[:]) {
			t.Fatalf("A must be excluded from the epoch weight snapshot while delegated away to B")
		}
	}
}

// (f) TestGovernanceVotingPower_ExcludesAddressDelegatedAwayToDifferentValidator
// is the second, independently confirmed consequence of the same exploit:
// core/state_transition.go's processPotsoRewardEpoch additively folds
// sp.EligibleValidators' basis values into the POTSO weight-snapshot that
// governance's CastVote reads (see item 3's doc comment there), with NO
// heartbeat gate at all (unlike the BFT ValidatorSet path above, which at
// least requires validatorReadyForActivation's heartbeat-recency check) --
// reachable purely through the same two ordinary user actions, with zero
// validator-specific behavior or heartbeat manipulation.
func TestGovernanceVotingPower_ExcludesAddressDelegatedAwayToDifferentValidator(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(1_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	rewardCfg := potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(900),
		TreasuryAddress:    sp.potsoRewardConfig.TreasuryAddress,
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
	}
	if err := sp.SetPotsoRewardConfig(rewardCfg); err != nil {
		t.Fatalf("set potso reward config: %v", err)
	}
	treasuryAcc, err := manager.GetAccount(rewardCfg.TreasuryAddress[:])
	if err != nil {
		t.Fatalf("load treasury: %v", err)
	}
	treasuryAcc.BalanceZNHB = big.NewInt(900)
	if err := manager.PutAccount(rewardCfg.TreasuryAddress[:], treasuryAcc); err != nil {
		t.Fatalf("fund treasury: %v", err)
	}

	var a, b [20]byte
	a[19] = 0xB3
	b[19] = 0xB4
	writeAccount(t, sp, a, &types.Account{BalanceZNHB: big.NewInt(30_000)})
	writeAccount(t, sp, b, &types.Account{BalanceZNHB: big.NewInt(10_000)})

	// Same three ordinary steps as the BFT test above -- deliberately no
	// heartbeat anywhere, since this fold path has none.
	if err := sp.applyStake(&types.Transaction{
		Value: big.NewInt(2_000),
		Data:  mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
	}, a[:]); err != nil {
		t.Fatalf("register+self-stake: %v", err)
	}
	if err := sp.applyUnstake(&types.Transaction{Value: big.NewInt(2_000)}, a[:]); err != nil {
		t.Fatalf("full self-unstake: %v", err)
	}
	if _, err := sp.StakeDelegate(a[:], b[:], big.NewInt(10_000)); err != nil {
		t.Fatalf("delegate to different validator: %v", err)
	}

	acct, err := sp.getAccount(a[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if !acct.ValidatorRegistered {
		t.Fatalf("precondition failed: A must still be ValidatorRegistered")
	}
	if _, ok := sp.EligibleValidators[string(a[:])]; ok {
		t.Fatalf("precondition failed: A must not be in EligibleValidators")
	}

	now := time.Unix(1_700_600_000, 0).UTC()
	sp.nowFunc = func() time.Time { return now }
	if err := sp.ProcessBlockLifecycle(1, now.Add(-time.Second).Unix()); err != nil {
		t.Fatalf("process block 1: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(2, now.Unix()); err != nil {
		t.Fatalf("process block 2: %v", err)
	}

	lastProcessed, ok, err := manager.PotsoRewardsLastProcessedEpoch()
	if err != nil {
		t.Fatalf("last processed epoch: %v", err)
	}
	if !ok {
		t.Fatalf("expected a processed potso epoch")
	}

	govSnapshot, ok, err := manager.SnapshotPotsoWeights(lastProcessed)
	if err != nil {
		t.Fatalf("snapshot potso weights: %v", err)
	}
	if !ok || govSnapshot == nil {
		t.Fatalf("expected a governance-facing potso weight snapshot")
	}
	for _, entry := range govSnapshot.Entries {
		if entry.Address == a && entry.WeightBps != 0 {
			t.Fatalf("A must NOT gain governance voting weight from stake delegated away to B, got WeightBps=%d", entry.WeightBps)
		}
	}
}

// (g) TestValidatorEligibility_ResumesAfterReStakingToSelf is the companion
// non-regression case: the selfDelegated gate must only exclude accounts
// currently delegated to someone OTHER than themselves -- an account that
// fully self-unstakes and then RE-stakes back to ITSELF (still
// ValidatorRegistered=true, no re-registration) must regain eligibility
// normally once its own basis clears the minimum again. See applyUnstake's
// doc comment: "re-staking back up after a dip shouldn't require
// re-registering." The fix must only exclude accounts self-delegated to
// someone else, not accounts that are simply between stakes.
func TestValidatorEligibility_ResumesAfterReStakingToSelf(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(5_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	var a [20]byte
	a[19] = 0xB5
	writeAccount(t, sp, a, &types.Account{BalanceZNHB: big.NewInt(30_000)})

	if err := sp.applyStake(&types.Transaction{
		Value: big.NewInt(6_000),
		Data:  mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
	}, a[:]); err != nil {
		t.Fatalf("register+self-stake: %v", err)
	}
	if _, ok := sp.EligibleValidators[string(a[:])]; !ok {
		t.Fatalf("precondition failed: expected eligible after initial self-stake")
	}

	// Full self-unstake -- a temporary dip to zero own stake.
	if err := sp.applyUnstake(&types.Transaction{Value: big.NewInt(6_000)}, a[:]); err != nil {
		t.Fatalf("full self-unstake: %v", err)
	}
	if _, ok := sp.EligibleValidators[string(a[:])]; ok {
		t.Fatalf("expected NOT eligible while own stake is zero")
	}
	acctMidDip, err := sp.getAccount(a[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if !acctMidDip.ValidatorRegistered {
		t.Fatalf("precondition failed: unstaking must not clear ValidatorRegistered")
	}
	if len(acctMidDip.DelegatedValidator) != 0 {
		t.Fatalf("precondition failed: expected no active delegation mid-dip")
	}

	// Re-stake back to SELF (no third-party target, no re-registration).
	if err := sp.applyStake(&types.Transaction{Value: big.NewInt(7_000)}, a[:]); err != nil {
		t.Fatalf("re-stake to self: %v", err)
	}

	acctFinal, err := sp.getAccount(a[:])
	if err != nil {
		t.Fatalf("get final account: %v", err)
	}
	if !acctFinal.ValidatorRegistered {
		t.Fatalf("expected ValidatorRegistered to remain true throughout")
	}
	if len(acctFinal.DelegatedValidator) != 0 && !bytes.Equal(acctFinal.DelegatedValidator, a[:]) {
		t.Fatalf("expected DelegatedValidator to be self or empty after re-staking to self, got %x", acctFinal.DelegatedValidator)
	}
	basis, ok := sp.EligibleValidators[string(a[:])]
	if !ok {
		t.Fatalf("expected eligibility to resume after re-staking to self past the minimum, without re-registering")
	}
	if basis.Cmp(big.NewInt(7_000)) != 0 {
		t.Fatalf("expected eligibility basis 7000 after re-stake, got %s", basis)
	}
}

// (h) TestFallbackValidatorSet_ExcludesAddressDelegatedAwayToDifferentValidator
// covers core/epochs.go's fallbackValidatorSet independently: it pulls
// candidate addresses from three sources -- live ValidatorSet, live
// EligibleValidators, AND historical epochHistory[i].Selected -- so an
// address that used to be a real active validator in a past epoch, BEFORE
// later delegating its own stake away to a different validator, must not be
// resurrected here on the strength of a historical Selected entry plus a
// stale heartbeat. This exercises fallbackValidatorSet's own independent
// stakeRewardBasis+minStake re-derivation, not just setAccount's.
func TestFallbackValidatorSet_ExcludesAddressDelegatedAwayToDifferentValidator(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(5_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	var a, b [20]byte
	a[19] = 0xB6
	b[19] = 0xB7
	writeAccount(t, sp, a, &types.Account{BalanceZNHB: big.NewInt(30_000)})
	writeAccount(t, sp, b, &types.Account{BalanceZNHB: big.NewInt(10_000)})

	if err := sp.applyStake(&types.Transaction{
		Value: big.NewInt(6_000),
		Data:  mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
	}, a[:]); err != nil {
		t.Fatalf("register+self-stake: %v", err)
	}
	// Give A a heartbeat -- fallbackValidatorSet requires one
	// (EngagementLastHeartbeat != 0) so this isn't excluded on that basis
	// instead of the one under test.
	acct, err := sp.getAccount(a[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	acct.EngagementLastHeartbeat = 1_700_000_000
	if err := sp.setAccount(a[:], acct); err != nil {
		t.Fatalf("set heartbeat: %v", err)
	}

	if err := sp.applyUnstake(&types.Transaction{Value: big.NewInt(6_000)}, a[:]); err != nil {
		t.Fatalf("full self-unstake: %v", err)
	}
	if _, err := sp.StakeDelegate(a[:], b[:], big.NewInt(10_000)); err != nil {
		t.Fatalf("delegate to different validator: %v", err)
	}

	// Simulate A having been a real active validator in a past epoch --
	// exactly what fallbackValidatorSet's historical-Selected source exists
	// to resurrect for a genuine liveness blip.
	sp.epochHistory = append(sp.epochHistory, epoch.Snapshot{
		Epoch:    0,
		Selected: [][]byte{append([]byte(nil), a[:]...)},
	})
	// Empty out the live sources so fallback is actually forced to fall
	// back onto the historical Selected entry above.
	sp.ValidatorSet = map[string]*big.Int{}
	sp.EligibleValidators = map[string]*big.Int{}

	minStake, err := sp.minimumValidatorStake()
	if err != nil {
		t.Fatalf("minimum validator stake: %v", err)
	}
	fallback, err := sp.fallbackValidatorSet(minStake)
	if err != nil {
		t.Fatalf("fallback validator set: %v", err)
	}
	if power, ok := fallback[string(a[:])]; ok {
		t.Fatalf("A must NOT be resurrected into the fallback validator set on stake delegated away to B, got power %s", power)
	}
}

// (i) TestMigrateLegacyAccount_RequiresRegistrationAndSelfDelegation covers a
// second, independent site that used to populate EligibleValidators/
// ValidatorSet directly from raw legacy.Stake >= minStake with NO
// registration, basis, or self-delegation check at all -- strictly weaker
// than the setAccount gap this file otherwise exercises. A legacy-format
// account (predating the ValidatorRegistered field) always decodes with
// ValidatorRegistered=false, so it must never auto-gain eligibility on
// migration; it has to explicitly register afterward like any other
// account, same as everyone else post-redesign.
func TestMigrateLegacyAccount_RequiresRegistrationAndSelfDelegation(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(5_000)); err != nil {
		t.Fatalf("set minimum stake: %v", err)
	}

	var unregistered [20]byte
	unregistered[19] = 0xC1
	legacyUnregistered := &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(50_000), // well above minStake
		StakeShares: big.NewInt(0),
		// ValidatorRegistered intentionally left at its zero value (false)
		// -- exactly what every real legacy-format account decodes to,
		// since the field did not exist in the old encoding.
	}
	if _, err := sp.migrateLegacyAccount(unregistered[:], legacyUnregistered); err != nil {
		t.Fatalf("migrate unregistered legacy account: %v", err)
	}
	if basis, ok := sp.EligibleValidators[string(unregistered[:])]; ok {
		t.Fatalf("unregistered legacy account must NOT gain eligibility from raw Stake alone, got basis %s", basis)
	}
	if power, ok := sp.ValidatorSet[string(unregistered[:])]; ok {
		t.Fatalf("unregistered legacy account must NOT gain live BFT voting power from raw Stake alone, got power %s", power)
	}

	// Positive control: the same gate must still grant eligibility for a
	// legacy account that genuinely is registered, self-delegated, and
	// clears the threshold -- proving this is a real, functional gate, not
	// a tautology that always evaluates false.
	var registered [20]byte
	registered[19] = 0xC2
	legacyRegistered := &types.Account{
		BalanceNHB:          big.NewInt(0),
		BalanceZNHB:         big.NewInt(0),
		Stake:               big.NewInt(50_000),
		StakeShares:         big.NewInt(0),
		ValidatorRegistered: true,
	}
	if _, err := sp.migrateLegacyAccount(registered[:], legacyRegistered); err != nil {
		t.Fatalf("migrate registered legacy account: %v", err)
	}
	basis, ok := sp.EligibleValidators[string(registered[:])]
	if !ok {
		t.Fatalf("registered, self-delegated legacy account above the threshold must gain eligibility")
	}
	if basis.Cmp(big.NewInt(50_000)) != 0 {
		t.Fatalf("expected eligibility basis 50000, got %s", basis)
	}
	if power, ok := sp.ValidatorSet[string(registered[:])]; !ok || power.Cmp(big.NewInt(50_000)) != 0 {
		t.Fatalf("expected live ValidatorSet power 50000, got %v (ok=%v)", power, ok)
	}
}
