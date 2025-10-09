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
	"nhbchain/crypto"
)

func Test_StakeEvents_EmitOnTransitions(t *testing.T) {
	sp := newStakingStateProcessor(t)

	start := time.Unix(1_700_000_000, 0).UTC()
	current := start
	sp.nowFunc = func() time.Time { return current }
	sp.BeginBlock(1, current)

	if err := sp.SetStakeRewardAPR(1_000); err != nil {
		t.Fatalf("set apr: %v", err)
	}

	var delegator, validator [20]byte
	delegator[19] = 0x10
	validator[19] = 0x11

	lastPayout := start.Add(-time.Duration(stakePayoutPeriodSeconds) * time.Second)
	writeAccount(t, sp, delegator, &types.Account{
		BalanceZNHB:        big.NewInt(25_000),
		StakeLastPayoutTs:  uint64(lastPayout.Unix()),
		StakeShares:        rewards.IndexUnit(),
		StakeLastIndex:     big.NewInt(0),
		PendingUnbonds:     nil,
		DelegatedValidator: nil,
	})
	writeAccount(t, sp, validator, &types.Account{Stake: big.NewInt(0)})

	// Initial delegation.
	beforeDelegator, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get delegator: %v", err)
	}
	beforeValidator, err := sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator: %v", err)
	}

	initialAmount := big.NewInt(1_000)
	if _, err := sp.StakeDelegate(delegator[:], validator[:], initialAmount); err != nil {
		t.Fatalf("initial delegate: %v", err)
	}
	t.Log("initial delegation recorded")

	afterDelegator, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get delegator after delegate: %v", err)
	}
	afterValidator, err := sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator after delegate: %v", err)
	}

	eventsList := sp.Events()
	if len(eventsList) < 2 {
		t.Fatalf("expected validator and delegator events, got %d", len(eventsList))
	}

	validatorEvt := eventsList[len(eventsList)-2]
	delegatorEvt := eventsList[len(eventsList)-1]

	validatorDelta := new(big.Int).Sub(afterValidator.StakeShares, beforeValidator.StakeShares)
	if validatorEvt.Type != events.TypeStakeDelegated {
		t.Fatalf("expected validator delegated event, got %s", validatorEvt.Type)
	}
	if got, want := validatorEvt.Attributes["addr"], crypto.MustNewAddress(crypto.NHBPrefix, validator[:]).String(); got != want {
		t.Fatalf("validator addr mismatch: got %s want %s", got, want)
	}
	if got, want := validatorEvt.Attributes["sharesAdded"], validatorDelta.String(); got != want {
		t.Fatalf("validator sharesAdded mismatch: got %s want %s", got, want)
	}
	if got, want := validatorEvt.Attributes["newShares"], afterValidator.StakeShares.String(); got != want {
		t.Fatalf("validator newShares mismatch: got %s want %s", got, want)
	}

	delegatorDelta := new(big.Int).Sub(afterDelegator.StakeShares, beforeDelegator.StakeShares)
	if delegatorEvt.Type != events.TypeStakeDelegated {
		t.Fatalf("expected delegator delegated event, got %s", delegatorEvt.Type)
	}
	if got, want := delegatorEvt.Attributes["addr"], crypto.MustNewAddress(crypto.NHBPrefix, delegator[:]).String(); got != want {
		t.Fatalf("delegator addr mismatch: got %s want %s", got, want)
	}
	if got, want := delegatorEvt.Attributes["sharesAdded"], delegatorDelta.String(); got != want {
		t.Fatalf("delegator sharesAdded mismatch: got %s want %s", got, want)
	}
	if got, want := delegatorEvt.Attributes["newShares"], afterDelegator.StakeShares.String(); got != want {
		t.Fatalf("delegator newShares mismatch: got %s want %s", got, want)
	}
	if got, want := delegatorEvt.Attributes["validator"], crypto.MustNewAddress(crypto.NHBPrefix, validator[:]).String(); got != want {
		t.Fatalf("delegator validator mismatch: got %s want %s", got, want)
	}
	if got, want := delegatorEvt.Attributes["amount"], initialAmount.String(); got != want {
		t.Fatalf("delegator amount mismatch: got %s want %s", got, want)
	}
	if got, want := delegatorEvt.Attributes["locked"], afterDelegator.LockedZNHB.String(); got != want {
		t.Fatalf("delegator locked mismatch: got %s want %s", got, want)
	}

	// Advance a year and top up to trigger share accrual.
	current = start.Add(365 * 24 * time.Hour)
	sp.BeginBlock(2, current)

	beforeDelegator, err = sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get delegator before top up: %v", err)
	}
	beforeValidator, err = sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator before top up: %v", err)
	}

	topUp := big.NewInt(500)
	if _, err := sp.StakeDelegate(delegator[:], validator[:], topUp); err != nil {
		t.Fatalf("top up delegate: %v", err)
	}
	t.Log("top up delegation recorded")

	afterDelegator, err = sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get delegator after top up: %v", err)
	}
	afterValidator, err = sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator after top up: %v", err)
	}

	eventsList = sp.Events()
	if len(eventsList) < 4 {
		t.Fatalf("expected two more events after top up, got %d", len(eventsList))
	}

	validatorEvt = eventsList[len(eventsList)-2]
	delegatorEvt = eventsList[len(eventsList)-1]

	validatorDelta = new(big.Int).Sub(afterValidator.StakeShares, beforeValidator.StakeShares)
	if got := validatorEvt.Attributes["sharesAdded"]; got != validatorDelta.String() {
		t.Fatalf("validator sharesAdded mismatch on top up: got %s want %s", got, validatorDelta.String())
	}
	delegatorDelta = new(big.Int).Sub(afterDelegator.StakeShares, beforeDelegator.StakeShares)
	if got := delegatorEvt.Attributes["sharesAdded"]; got != delegatorDelta.String() {
		t.Fatalf("delegator sharesAdded mismatch on top up: got %s want %s", got, delegatorDelta.String())
	}

	// Advance another year and undelegate part of the position.
	current = start.Add(2 * 365 * 24 * time.Hour)
	sp.BeginBlock(3, current)

	beforeDelegator, err = sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get delegator before undelegate: %v", err)
	}
	beforeValidator, err = sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator before undelegate: %v", err)
	}

	unstakeAmount := big.NewInt(400)
	unbond, err := sp.StakeUndelegate(delegator[:], unstakeAmount)
	if err != nil {
		t.Fatalf("undelegate: %v", err)
	}
	t.Log("undelegation emitted")

	afterDelegator, err = sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get delegator after undelegate: %v", err)
	}
	afterValidator, err = sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator after undelegate: %v", err)
	}

	eventsList = sp.Events()
	if len(eventsList) < 6 {
		t.Fatalf("expected two undelegated events, got %d", len(eventsList))
	}

	validatorEvt = eventsList[len(eventsList)-2]
	delegatorEvt = eventsList[len(eventsList)-1]

	validatorRemoved := new(big.Int).Sub(beforeValidator.StakeShares, afterValidator.StakeShares)
	if validatorRemoved.Sign() < 0 {
		validatorRemoved = big.NewInt(0)
	}
	if got := validatorEvt.Attributes["sharesRemoved"]; got != validatorRemoved.String() {
		t.Fatalf("validator sharesRemoved mismatch: got %s want %s", got, validatorRemoved.String())
	}
	if got := validatorEvt.Attributes["amount"]; got != unstakeAmount.String() {
		t.Fatalf("validator amount mismatch: got %s want %s", got, unstakeAmount.String())
	}

	delegatorRemoved := new(big.Int).Sub(beforeDelegator.StakeShares, afterDelegator.StakeShares)
	if delegatorRemoved.Sign() < 0 {
		delegatorRemoved = big.NewInt(0)
	}
	if got := delegatorEvt.Attributes["sharesRemoved"]; got != delegatorRemoved.String() {
		t.Fatalf("delegator sharesRemoved mismatch: got %s want %s", got, delegatorRemoved.String())
	}
	if got := delegatorEvt.Attributes["releaseTime"]; got != strconv.FormatUint(unbond.ReleaseTime, 10) {
		t.Fatalf("release time mismatch: got %s want %d", got, unbond.ReleaseTime)
	}
	if got := delegatorEvt.Attributes["unbondingId"]; got != strconv.FormatUint(unbond.ID, 10) {
		t.Fatalf("unbond id mismatch: got %s want %d", got, unbond.ID)
	}

	// Advance to a future period and claim rewards.
	current = start.Add(2*365*24*time.Hour + 90*24*time.Hour)
	sp.BeginBlock(4, current)
	if _, err := sp.advanceStakeRewards(); err != nil {
		t.Fatalf("advance rewards: %v", err)
	}

	beforeDelegator, err = sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get delegator before claim: %v", err)
	}
	// Force the payout window to align with a single period so the rewards claim mints a non-zero amount.
	adjustedPayout := current.Add(-2 * time.Duration(stakePayoutPeriodSeconds) * time.Second)
	beforeDelegator.StakeLastPayoutTs = uint64(adjustedPayout.Unix())
	beforeDelegator.StakeLastIndex = big.NewInt(0)
	beforeDelegator.StakeShares = rewards.IndexUnit()
	if err := sp.PutAccount(delegator[:], beforeDelegator); err != nil {
		t.Fatalf("update payout timestamp: %v", err)
	}
	beforeDelegator, err = sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("reload delegator before claim: %v", err)
	}
	boostedIndex := new(big.Int).Mul(rewards.IndexUnit(), big.NewInt(1000))
	if err := sp.writeBigInt(nhbstate.StakingGlobalIndexKey(), boostedIndex); err != nil {
		t.Fatalf("seed global index: %v", err)
	}
	storedIndex, err := sp.loadBigInt(nhbstate.StakingGlobalIndexKey())
	if err != nil {
		t.Fatalf("load seeded index: %v", err)
	}
	t.Logf("seeded global index=%s shares=%s", storedIndex.String(), beforeDelegator.StakeShares.String())

	nowTs := uint64(current.Unix())
	elapsed := nowTs - beforeDelegator.StakeLastPayoutTs
	if elapsed <= stakePayoutPeriodSeconds {
		t.Fatalf("expected payout window elapsed")
	}
	expectedPeriods := elapsed / stakePayoutPeriodSeconds

	minted, err := sp.StakeClaimRewards(delegator[:])
	if err != nil {
		t.Fatalf("claim rewards: %v", err)
	}
	t.Log("rewards claimed")

	afterDelegator, err = sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get delegator after claim: %v", err)
	}

	eventsList = sp.Events()
	t.Logf("minted=%s totalEvents=%d", minted.String(), len(eventsList))
	if len(eventsList) < 8 {
		t.Fatalf("expected rewards events, got %d", len(eventsList))
	}

	rewardsEvt := eventsList[len(eventsList)-2]
	legacyEvt := eventsList[len(eventsList)-1]

	if rewardsEvt.Type != events.TypeStakeRewardsClaimed {
		t.Fatalf("expected rewards event type, got %s", rewardsEvt.Type)
	}
	if got := rewardsEvt.Attributes["addr"]; got != crypto.MustNewAddress(crypto.NHBPrefix, delegator[:]).String() {
		t.Fatalf("rewards addr mismatch: got %s", rewardsEvt.Attributes["addr"])
	}
	if got := rewardsEvt.Attributes["minted"]; got != minted.String() {
		t.Fatalf("minted mismatch: got %s want %s", got, minted.String())
	}
	if got := rewardsEvt.Attributes["periods"]; got != strconv.FormatUint(expectedPeriods, 10) {
		t.Fatalf("periods mismatch: got %s want %d", got, expectedPeriods)
	}
	if got := rewardsEvt.Attributes["lastIndex"]; got != afterDelegator.StakeLastIndex.String() {
		t.Fatalf("lastIndex mismatch: got %s want %s", got, afterDelegator.StakeLastIndex.String())
	}

	emissionKey := nhbstate.StakingEmissionYTDKey(uint32(current.UTC().Year()))
	emissionTotal, err := sp.loadBigInt(emissionKey)
	if err != nil {
		t.Fatalf("load emission total: %v", err)
	}
	if got := rewardsEvt.Attributes["emissionYTD"]; got != emissionTotal.String() {
		t.Fatalf("emissionYTD mismatch: got %s want %s", got, emissionTotal.String())
	}

	if legacyEvt.Type != events.TypeStakeRewardsClaimedLegacy {
		t.Fatalf("expected legacy alias, got %s", legacyEvt.Type)
	}
	if len(legacyEvt.Attributes) != len(rewardsEvt.Attributes) {
		t.Fatalf("legacy attributes mismatch: got %d want %d", len(legacyEvt.Attributes), len(rewardsEvt.Attributes))
	}
	for key, value := range rewardsEvt.Attributes {
		if got := legacyEvt.Attributes[key]; got != value {
			t.Fatalf("legacy attribute %s mismatch: got %s want %s", key, got, value)
		}
	}
}
