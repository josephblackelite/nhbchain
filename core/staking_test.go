package core

import (
	"errors"
	"math/big"
	"strconv"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"

	"nhbchain/core/events"
	"nhbchain/core/rewards"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func newStakingStateProcessor(t *testing.T) *StateProcessor {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("create trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("new state processor: %v", err)
	}
	return sp
}

func writeAccount(t *testing.T, sp *StateProcessor, addr [20]byte, account *types.Account) {
	t.Helper()
	ensureAccountDefaults(account)
	balance, overflow := uint256.FromBig(account.BalanceNHB)
	if overflow {
		t.Fatalf("balance overflow")
	}
	stateAcc := &gethtypes.StateAccount{
		Nonce:   account.Nonce,
		Balance: balance,
		Root:    common.BytesToHash(account.StorageRoot),
		CodeHash: func() []byte {
			if len(account.CodeHash) == 0 {
				return gethtypes.EmptyCodeHash.Bytes()
			}
			return common.CopyBytes(account.CodeHash)
		}(),
	}
	if stateAcc.Root == (common.Hash{}) {
		stateAcc.Root = gethtypes.EmptyRootHash
	}
	if err := sp.writeStateAccount(addr[:], stateAcc); err != nil {
		t.Fatalf("write state account: %v", err)
	}
	var delegated []byte
	if len(account.DelegatedValidator) > 0 {
		delegated = append([]byte(nil), account.DelegatedValidator...)
	}
	unbonding := make([]stakeUnbond, len(account.PendingUnbonds))
	for i, entry := range account.PendingUnbonds {
		amount := big.NewInt(0)
		if entry.Amount != nil {
			amount = new(big.Int).Set(entry.Amount)
		}
		var validator []byte
		if len(entry.Validator) > 0 {
			validator = append([]byte(nil), entry.Validator...)
		}
		unbonding[i] = stakeUnbond{
			ID:          entry.ID,
			Validator:   validator,
			Amount:      amount,
			ReleaseTime: entry.ReleaseTime,
		}
	}
	meta := &accountMetadata{
		BalanceZNHB:               new(big.Int).Set(account.BalanceZNHB),
		Stake:                     new(big.Int).Set(account.Stake),
		LockedZNHB:                new(big.Int).Set(account.LockedZNHB),
		CollateralBalance:         new(big.Int).Set(account.CollateralBalance),
		DebtPrincipal:             new(big.Int).Set(account.DebtPrincipal),
		SupplyShares:              new(big.Int).Set(account.SupplyShares),
		LendingSupplyIndex:        new(big.Int).Set(account.LendingSnapshot.SupplyIndex),
		LendingBorrowIndex:        new(big.Int).Set(account.LendingSnapshot.BorrowIndex),
		DelegatedValidator:        delegated,
		Unbonding:                 unbonding,
		UnbondingSeq:              account.NextUnbondingID,
		Username:                  account.Username,
		EngagementScore:           account.EngagementScore,
		LendingCollateralDisabled: account.LendingBreaker.CollateralDisabled,
		LendingBorrowDisabled:     account.LendingBreaker.BorrowDisabled,
	}
	if err := sp.writeAccountMetadata(addr[:], meta); err != nil {
		t.Fatalf("write account metadata: %v", err)
	}
}

func TestStakeDelegateSelf(t *testing.T) {
	sp := newStakingStateProcessor(t)
	var delegator [20]byte
	delegator[19] = 0x01

	account := &types.Account{BalanceZNHB: big.NewInt(5000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0), LockedZNHB: big.NewInt(0)}
	writeAccount(t, sp, delegator, account)

	if _, err := sp.StakeDelegate(delegator[:], nil, big.NewInt(1000)); err != nil {
		t.Fatalf("stake delegate: %v", err)
	}

	updated, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if updated.LockedZNHB.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("expected locked 1000, got %s", updated.LockedZNHB.String())
	}
	if updated.Stake.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("expected stake 1000, got %s", updated.Stake.String())
	}
	if updated.BalanceZNHB.Cmp(big.NewInt(4000)) != 0 {
		t.Fatalf("expected balance 4000, got %s", updated.BalanceZNHB.String())
	}
	if len(sp.Events()) == 0 || sp.Events()[len(sp.Events())-1].Type != "stake.delegated" {
		t.Fatalf("expected stake.delegated event, got %#v", sp.Events())
	}
	if power, ok := sp.ValidatorSet[string(delegator[:])]; !ok || power.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("expected validator power 1000, got %v", power)
	}
}

func TestStakeUndelegateAndClaim(t *testing.T) {
	sp := newStakingStateProcessor(t)
	base := time.Unix(1700000000, 0)
	sp.nowFunc = func() time.Time { return base }

	var delegator, validator [20]byte
	delegator[19] = 0x02
	validator[19] = 0x03

	delegatorAccount := &types.Account{BalanceZNHB: big.NewInt(2000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0), LockedZNHB: big.NewInt(0)}
	writeAccount(t, sp, delegator, delegatorAccount)
	if _, err := sp.StakeDelegate(delegator[:], validator[:], big.NewInt(1200)); err != nil {
		t.Fatalf("delegate: %v", err)
	}

	unbond, err := sp.StakeUndelegate(delegator[:], big.NewInt(1200))
	if err != nil {
		t.Fatalf("undelegate: %v", err)
	}
	expectedRelease := uint64(base.Add(unbondingPeriod).Unix())
	if unbond.ReleaseTime != expectedRelease {
		t.Fatalf("expected release %d got %d", expectedRelease, unbond.ReleaseTime)
	}
	updated, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if updated.LockedZNHB.Sign() != 0 {
		t.Fatalf("expected locked 0, got %s", updated.LockedZNHB.String())
	}
	if len(updated.PendingUnbonds) != 1 {
		t.Fatalf("expected one unbond entry, got %d", len(updated.PendingUnbonds))
	}
	validatorAcct, err := sp.getAccount(validator[:])
	if err != nil {
		t.Fatalf("get validator: %v", err)
	}
	if validatorAcct.Stake.Sign() != 0 {
		t.Fatalf("expected validator stake 0, got %s", validatorAcct.Stake.String())
	}

	if _, err := sp.StakeClaim(delegator[:], unbond.ID); err == nil {
		t.Fatalf("expected claim to fail before maturity")
	}

	base = base.Add(unbondingPeriod + time.Hour)
	sp.nowFunc = func() time.Time { return base }

	claimed, err := sp.StakeClaim(delegator[:], unbond.ID)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.ID != unbond.ID {
		t.Fatalf("expected claim id %d got %d", unbond.ID, claimed.ID)
	}
	finalAcct, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	if len(finalAcct.PendingUnbonds) != 0 {
		t.Fatalf("expected no pending unbonds")
	}
	if finalAcct.BalanceZNHB.Cmp(big.NewInt(2000)) != 0 {
		t.Fatalf("expected restored balance 2000 got %s", finalAcct.BalanceZNHB.String())
	}
	events := sp.Events()
	if len(events) < 3 || events[len(events)-1].Type != "stake.claimed" {
		t.Fatalf("expected stake.claimed event, got %#v", events)
	}
}

func TestStakeDelegateSwitchValidatorBlocked(t *testing.T) {
	sp := newStakingStateProcessor(t)
	var delegator, validatorA, validatorB [20]byte
	delegator[19] = 0x10
	validatorA[19] = 0x11
	validatorB[19] = 0x12

	acct := &types.Account{BalanceZNHB: big.NewInt(1500), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0), LockedZNHB: big.NewInt(0)}
	writeAccount(t, sp, delegator, acct)
	if _, err := sp.StakeDelegate(delegator[:], validatorA[:], big.NewInt(1000)); err != nil {
		t.Fatalf("delegate A: %v", err)
	}
	if _, err := sp.StakeDelegate(delegator[:], validatorB[:], big.NewInt(100)); err == nil {
		t.Fatalf("expected error when switching validator without undelegating")
	}
}

type staticPauseView map[string]bool

func (p staticPauseView) IsPaused(module string) bool { return p[module] }

func Test_PauseBlocksMutations_All(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetPauseView(staticPauseView{moduleStaking: true})

	var delegator [20]byte
	delegator[19] = 0x21

	account := &types.Account{BalanceZNHB: big.NewInt(5_000), LockedZNHB: big.NewInt(1_000), Stake: big.NewInt(1_000)}
	writeAccount(t, sp, delegator, account)

	if _, err := sp.StakeDelegate(delegator[:], nil, big.NewInt(100)); !errors.Is(err, ErrStakePaused) {
		t.Fatalf("expected ErrStakePaused for delegate, got %v", err)
	}
	checkStakePausedEvent(t, sp, events.StakeOperationDelegate, 0, delegator)

	if _, err := sp.StakeUndelegate(delegator[:], big.NewInt(100)); !errors.Is(err, ErrStakePaused) {
		t.Fatalf("expected ErrStakePaused for undelegate, got %v", err)
	}
	checkStakePausedEvent(t, sp, events.StakeOperationUndelegate, 0, delegator)

	if _, err := sp.StakeClaim(delegator[:], 42); !errors.Is(err, ErrStakePaused) {
		t.Fatalf("expected ErrStakePaused for claim, got %v", err)
	}
	checkStakePausedEvent(t, sp, events.StakeOperationClaim, 42, delegator)

	if _, err := sp.StakeClaimRewards(delegator[:]); !errors.Is(err, ErrStakePaused) {
		t.Fatalf("expected ErrStakePaused for claim rewards, got %v", err)
	}
	checkStakePausedEvent(t, sp, events.StakeOperationClaimRewards, 0, delegator)
}

func checkStakePausedEvent(t *testing.T, sp *StateProcessor, operation string, unbondID uint64, delegator [20]byte) {
	t.Helper()
	eventsLog := sp.Events()
	if len(eventsLog) == 0 {
		t.Fatalf("expected stake.paused event for %s", operation)
	}
	evt := eventsLog[len(eventsLog)-1]
	if evt.Type != events.TypeStakePaused {
		t.Fatalf("expected %s event, got %s", events.TypeStakePaused, evt.Type)
	}
	if op := evt.Attributes["operation"]; op != operation {
		t.Fatalf("unexpected operation attribute: got %s want %s", op, operation)
	}
	if addr := evt.Attributes["addr"]; addr != crypto.MustNewAddress(crypto.NHBPrefix, delegator[:]).String() {
		t.Fatalf("unexpected addr attribute: got %s", addr)
	}
	if evt.Attributes["reason"] == "" {
		t.Fatalf("expected reason attribute")
	}
	if unbondID > 0 {
		if evt.Attributes["unbondingId"] != strconv.FormatUint(unbondID, 10) {
			t.Fatalf("unexpected unbondingId attribute: got %s want %d", evt.Attributes["unbondingId"], unbondID)
		}
	}
}

func TestStakeClaimRewards_ExactPeriodBoundary(t *testing.T) {
	sp := newStakingStateProcessor(t)

	now := time.Unix(1_700_100_000, 0).UTC()
	sp.nowFunc = func() time.Time { return now }

	var delegator [20]byte
	delegator[19] = 0x31

	shares := big.NewInt(1_234)
	lastIndex := rewards.IndexUnit()
	account := &types.Account{
		BalanceZNHB:       big.NewInt(0),
		StakeShares:       new(big.Int).Set(shares),
		StakeLastIndex:    new(big.Int).Set(lastIndex),
		StakeLastPayoutTs: uint64(now.Add(-time.Duration(stakePayoutPeriodSeconds) * time.Second).Unix()),
	}
	writeAccount(t, sp, delegator, account)

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.PutAccountMetadata(delegator[:], account); err != nil {
		t.Fatalf("put account metadata: %v", err)
	}

	storedBefore, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get stored account: %v", err)
	}
	if storedBefore.StakeShares.Sign() <= 0 {
		t.Fatalf("expected positive stake shares, got %s", storedBefore.StakeShares)
	}
	delta := big.NewInt(1_000)
	globalIndex := new(big.Int).Add(lastIndex, delta)
	if err := manager.SetStakingGlobalIndex(globalIndex); err != nil {
		t.Fatalf("set global index: %v", err)
	}
	globalIndex, err = manager.StakingGlobalIndex()
	if err != nil {
		t.Fatalf("load global index: %v", err)
	}

	expectedMinted, expectedDelta := expectedStakeMint(t, storedBefore, globalIndex, now)
	if expectedDelta.Sign() <= 0 {
		t.Fatalf("expected positive index delta, got %s (last=%s global=%s)", expectedDelta, storedBefore.StakeLastIndex, globalIndex)
	}
	if expectedMinted.Sign() <= 0 {
		t.Fatalf("expected positive minted amount, got %s", expectedMinted)
	}

	minted, err := sp.StakeClaimRewards(delegator[:])
	if err != nil {
		t.Fatalf("claim rewards: %v", err)
	}
	if minted.Cmp(expectedMinted) != 0 {
		t.Fatalf("unexpected minted amount: got %s want %s", minted, expectedMinted)
	}

	updated, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}

	expectedIndex := new(big.Int).Add(storedBefore.StakeLastIndex, expectedDelta)
	if updated.StakeLastIndex.Cmp(expectedIndex) != 0 {
		t.Fatalf("unexpected last index: got %s want %s", updated.StakeLastIndex, expectedIndex)
	}
	if updated.StakeLastPayoutTs != uint64(now.Unix()) {
		t.Fatalf("unexpected payout timestamp: got %d want %d", updated.StakeLastPayoutTs, now.Unix())
	}
	if updated.BalanceZNHB.Cmp(expectedMinted) != 0 {
		t.Fatalf("unexpected balance: got %s want %s", updated.BalanceZNHB, expectedMinted)
	}
}

func TestStakeClaimRewards_MultiPeriodCatchUp(t *testing.T) {
	sp := newStakingStateProcessor(t)

	now := time.Unix(1_700_200_000, 0).UTC()
	sp.nowFunc = func() time.Time { return now }

	var delegator [20]byte
	delegator[19] = 0x32

	shares := big.NewInt(10)
	lastIndex := rewards.IndexUnit()
	period := time.Duration(stakePayoutPeriodSeconds) * time.Second
	lastPayout := now.Add(-3*period - 10*24*time.Hour)
	account := &types.Account{
		BalanceZNHB:       big.NewInt(0),
		StakeShares:       new(big.Int).Set(shares),
		StakeLastIndex:    new(big.Int).Set(lastIndex),
		StakeLastPayoutTs: uint64(lastPayout.Unix()),
	}
	writeAccount(t, sp, delegator, account)

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.PutAccountMetadata(delegator[:], account); err != nil {
		t.Fatalf("put account metadata: %v", err)
	}

	storedBefore, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get stored account: %v", err)
	}
	if storedBefore.StakeShares.Sign() <= 0 {
		t.Fatalf("expected positive stake shares, got %s", storedBefore.StakeShares)
	}

	delta := big.NewInt(1_000)
	globalIndex := new(big.Int).Add(lastIndex, delta)
	if err := manager.SetStakingGlobalIndex(globalIndex); err != nil {
		t.Fatalf("set global index: %v", err)
	}
	globalIndex, err = manager.StakingGlobalIndex()
	if err != nil {
		t.Fatalf("load global index: %v", err)
	}

	expectedMinted, expectedDelta := expectedStakeMint(t, storedBefore, globalIndex, now)
	if expectedDelta.Sign() <= 0 {
		t.Fatalf("expected positive index delta, got %s (last=%s global=%s)", expectedDelta, storedBefore.StakeLastIndex, globalIndex)
	}
	if expectedMinted.Sign() <= 0 {
		t.Fatalf("expected positive minted amount, got %s", expectedMinted)
	}

	minted, err := sp.StakeClaimRewards(delegator[:])
	if err != nil {
		t.Fatalf("claim rewards: %v", err)
	}
	if minted.Cmp(expectedMinted) != 0 {
		t.Fatalf("unexpected minted amount: got %s want %s", minted, expectedMinted)
	}

	updated, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}

	expectedIndex := new(big.Int).Add(storedBefore.StakeLastIndex, expectedDelta)
	if updated.StakeLastIndex.Cmp(expectedIndex) != 0 {
		t.Fatalf("unexpected last index: got %s want %s", updated.StakeLastIndex, expectedIndex)
	}
	if updated.StakeLastPayoutTs != uint64(now.Unix()) {
		t.Fatalf("unexpected payout timestamp: got %d want %d", updated.StakeLastPayoutTs, now.Unix())
	}
	if updated.BalanceZNHB.Cmp(expectedMinted) != 0 {
		t.Fatalf("unexpected balance: got %s want %s", updated.BalanceZNHB, expectedMinted)
	}
}

func TestStakeClaimRewards_APRChangeMidPeriod(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var delegator [20]byte
	delegator[19] = 0x33

	start := time.Unix(1_700_300_000, 0).UTC()
	mid := start.Add(15 * 24 * time.Hour)
	end := start.Add(time.Duration(stakePayoutPeriodSeconds) * time.Second)

	sp.nowFunc = func() time.Time { return start.Add(42 * time.Hour) }
	sp.BeginBlock(1, start)
	if err := sp.SetStakeRewardAPR(1_000); err != nil {
		t.Fatalf("set apr: %v", err)
	}
	if got, want := sp.stakeRewardEngine.LastUpdateTs(), uint64(start.Unix()); got != want {
		t.Fatalf("unexpected last update after initial apr: got %d want %d", got, want)
	}

	shares := big.NewInt(200)
	account := &types.Account{
		BalanceZNHB:       big.NewInt(0),
		StakeShares:       new(big.Int).Set(shares),
		StakeLastIndex:    rewards.IndexUnit(),
		StakeLastPayoutTs: uint64(start.Unix()),
	}
	writeAccount(t, sp, delegator, account)

	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.PutAccountMetadata(delegator[:], account); err != nil {
		t.Fatalf("put account metadata: %v", err)
	}

	sp.nowFunc = func() time.Time { return start.Add(99 * time.Hour) }
	sp.BeginBlock(2, mid)
	if err := sp.SetStakeRewardAPR(3_000); err != nil {
		t.Fatalf("update apr: %v", err)
	}
	if got, want := sp.stakeRewardEngine.LastUpdateTs(), uint64(mid.Unix()); got != want {
		t.Fatalf("unexpected last update at apr change: got %d want %d", got, want)
	}

	sp.nowFunc = func() time.Time { return start.Add(123 * time.Hour) }
	sp.BeginBlock(3, end)
	if err := sp.SetStakeRewardAPR(3_000); err != nil {
		t.Fatalf("roll apr: %v", err)
	}
	if got, want := sp.stakeRewardEngine.LastUpdateTs(), uint64(end.Unix()); got != want {
		t.Fatalf("unexpected last update at period end: got %d want %d", got, want)
	}

	beforeClaim, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get stored account: %v", err)
	}
	if beforeClaim.StakeShares.Sign() <= 0 {
		t.Fatalf("expected positive stake shares, got %s", beforeClaim.StakeShares)
	}

	expectedEngine := rewards.NewEngine()
	expectedEngine.SetLastUpdateTs(uint64(start.Unix()))
	expectedEngine.UpdateGlobalIndex(mid, 1_000)
	expectedEngine.UpdateGlobalIndex(end, 3_000)
	globalIndex := expectedEngine.Index()
	if err := manager.SetStakingGlobalIndex(globalIndex); err != nil {
		t.Fatalf("set expected global index: %v", err)
	}

	expectedMinted, expectedDelta := expectedStakeMint(t, beforeClaim, globalIndex, end)
	if expectedDelta.Sign() <= 0 {
		t.Fatalf("expected positive index delta, got %s (last=%s global=%s)", expectedDelta, beforeClaim.StakeLastIndex, globalIndex)
	}
	if expectedMinted.Sign() <= 0 {
		t.Fatalf("expected positive minted amount, got %s", expectedMinted)
	}

	sp.nowFunc = func() time.Time { return end }
	minted, err := sp.StakeClaimRewards(delegator[:])
	if err != nil {
		t.Fatalf("claim rewards: %v", err)
	}

	if minted.Cmp(expectedMinted) != 0 {
		t.Fatalf("unexpected minted amount: got %s want %s", minted, expectedMinted)
	}

	updated, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	expectedIndex := new(big.Int).Add(beforeClaim.StakeLastIndex, expectedDelta)
	if updated.StakeLastIndex.Cmp(expectedIndex) != 0 {
		t.Fatalf("unexpected last index: got %s want %s", updated.StakeLastIndex, expectedIndex)
	}
	if updated.StakeLastPayoutTs != uint64(end.Unix()) {
		t.Fatalf("unexpected payout timestamp: got %d want %d", updated.StakeLastPayoutTs, end.Unix())
	}
	if updated.BalanceZNHB.Cmp(expectedMinted) != 0 {
		t.Fatalf("unexpected balance: got %s want %s", updated.BalanceZNHB, expectedMinted)
	}
}

func TestStakeClaimRewards_RoundingExtremes(t *testing.T) {
	t.Run("huge stake", func(t *testing.T) {
		sp := newStakingStateProcessor(t)

		now := time.Unix(1_700_400_000, 0).UTC()
		sp.nowFunc = func() time.Time { return now }

		var delegator [20]byte
		delegator[19] = 0x41

		shares := new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil)
		account := &types.Account{
			BalanceZNHB:       big.NewInt(0),
			StakeShares:       new(big.Int).Set(shares),
			StakeLastIndex:    rewards.IndexUnit(),
			StakeLastPayoutTs: uint64(now.Add(-time.Duration(stakePayoutPeriodSeconds) * time.Second).Unix()),
		}
		writeAccount(t, sp, delegator, account)

		manager := nhbstate.NewManager(sp.Trie)
		if err := manager.PutAccountMetadata(delegator[:], account); err != nil {
			t.Fatalf("put account metadata: %v", err)
		}
		delta := big.NewInt(3)
		globalIndex := new(big.Int).Add(rewards.IndexUnit(), delta)
		if err := manager.SetStakingGlobalIndex(globalIndex); err != nil {
			t.Fatalf("set global index: %v", err)
		}

		storedBefore, err := sp.getAccount(delegator[:])
		if err != nil {
			t.Fatalf("get stored account: %v", err)
		}
		if storedBefore.StakeShares.Sign() <= 0 {
			t.Fatalf("expected positive stake shares, got %s", storedBefore.StakeShares)
		}
		globalIndex, err = manager.StakingGlobalIndex()
		if err != nil {
			t.Fatalf("load global index: %v", err)
		}
		expectedMinted, expectedDelta := expectedStakeMint(t, storedBefore, globalIndex, now)
		if expectedDelta.Sign() <= 0 {
			t.Fatalf("expected positive delta, got %s", expectedDelta)
		}
		directExpectation := new(big.Int).Mul(delta, shares)
		if expectedMinted.Cmp(directExpectation) != 0 {
			t.Fatalf("unexpected expected minted: got %s want %s", expectedMinted, directExpectation)
		}

		minted, err := sp.StakeClaimRewards(delegator[:])
		if err != nil {
			t.Fatalf("claim rewards: %v", err)
		}
		if minted.Cmp(expectedMinted) != 0 {
			t.Fatalf("unexpected minted amount: got %s want %s", minted, expectedMinted)
		}

		updated, err := sp.getAccount(delegator[:])
		if err != nil {
			t.Fatalf("get account: %v", err)
		}
		if updated.BalanceZNHB.Cmp(expectedMinted) != 0 {
			t.Fatalf("unexpected balance: got %s want %s", updated.BalanceZNHB, expectedMinted)
		}
	})

	t.Run("tiny share rounding", func(t *testing.T) {
		sp := newStakingStateProcessor(t)

		now := time.Unix(1_700_500_000, 0).UTC()
		sp.nowFunc = func() time.Time { return now }

		var delegator [20]byte
		delegator[19] = 0x42

		period := time.Duration(stakePayoutPeriodSeconds) * time.Second
		shares := big.NewInt(1)
		account := &types.Account{
			BalanceZNHB:       big.NewInt(0),
			StakeShares:       new(big.Int).Set(shares),
			StakeLastIndex:    rewards.IndexUnit(),
			StakeLastPayoutTs: uint64(now.Add(-(period + time.Second)).Unix()),
		}
		writeAccount(t, sp, delegator, account)

		manager := nhbstate.NewManager(sp.Trie)
		if err := manager.PutAccountMetadata(delegator[:], account); err != nil {
			t.Fatalf("put account metadata: %v", err)
		}
		delta := big.NewInt(1)
		globalIndex := new(big.Int).Add(rewards.IndexUnit(), delta)
		if err := manager.SetStakingGlobalIndex(globalIndex); err != nil {
			t.Fatalf("set global index: %v", err)
		}

		storedBefore, err := sp.getAccount(delegator[:])
		if err != nil {
			t.Fatalf("get stored account: %v", err)
		}
		if storedBefore.StakeShares.Sign() <= 0 {
			t.Fatalf("expected positive stake shares, got %s", storedBefore.StakeShares)
		}
		globalIndex, err = manager.StakingGlobalIndex()
		if err != nil {
			t.Fatalf("load global index: %v", err)
		}
		expectedMinted, expectedDelta := expectedStakeMint(t, storedBefore, globalIndex, now)
		if expectedMinted.Sign() != 0 {
			t.Fatalf("expected zero minted amount, got %s", expectedMinted)
		}
		if expectedDelta.Sign() != 0 {
			t.Fatalf("expected zero delta, got %s", expectedDelta)
		}

		minted, err := sp.StakeClaimRewards(delegator[:])
		if err != nil {
			t.Fatalf("claim rewards: %v", err)
		}
		if minted.Sign() != 0 {
			t.Fatalf("expected zero minted amount, got %s", minted)
		}

		updated, err := sp.getAccount(delegator[:])
		if err != nil {
			t.Fatalf("get account: %v", err)
		}
		if updated.StakeLastIndex.Cmp(rewards.IndexUnit()) != 0 {
			t.Fatalf("unexpected last index after rounding: got %s", updated.StakeLastIndex)
		}
		if updated.BalanceZNHB.Sign() != 0 {
			t.Fatalf("expected zero balance, got %s", updated.BalanceZNHB)
		}
		if updated.StakeLastPayoutTs != uint64(now.Unix()) {
			t.Fatalf("unexpected payout timestamp: got %d want %d", updated.StakeLastPayoutTs, now.Unix())
		}
	})
}

func TestStakeLifecycleSelfConsistency(t *testing.T) {
	sp := newStakingStateProcessor(t)

	var delegator [20]byte
	delegator[19] = 0x43

	start := time.Unix(1_700_600_000, 0).UTC()
	period := time.Duration(stakePayoutPeriodSeconds) * time.Second

	initialBalance := big.NewInt(5_000)
	initialLocked := big.NewInt(1_000)
	initialShares := big.NewInt(200)

	account := &types.Account{
		BalanceZNHB:        new(big.Int).Set(initialBalance),
		LockedZNHB:         new(big.Int).Set(initialLocked),
		Stake:              new(big.Int).Set(initialLocked),
		StakeShares:        new(big.Int).Set(initialShares),
		StakeLastIndex:     rewards.IndexUnit(),
		StakeLastPayoutTs:  uint64(start.Add(-period).Unix()),
		DelegatedValidator: delegator[:],
	}
	writeAccount(t, sp, delegator, account)

	initialTotal := new(big.Int).Add(new(big.Int).Set(initialBalance), new(big.Int).Set(initialLocked))

	sp.nowFunc = func() time.Time { return start }
	sp.BeginBlock(1, start)
	if _, err := sp.StakeDelegate(delegator[:], nil, big.NewInt(500)); err != nil {
		t.Fatalf("stake delegate: %v", err)
	}

	afterStake, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account after stake: %v", err)
	}
	totalAfterStake := new(big.Int).Add(new(big.Int).Set(afterStake.BalanceZNHB), new(big.Int).Set(afterStake.LockedZNHB))
	if totalAfterStake.Cmp(initialTotal) != 0 {
		t.Fatalf("stake should conserve total balance: got %s want %s", totalAfterStake, initialTotal)
	}

	claimTime := start.Add(period)
	sp.nowFunc = func() time.Time { return claimTime }
	manager := nhbstate.NewManager(sp.Trie)
	delta := big.NewInt(25)
	globalIndex := new(big.Int).Add(afterStake.StakeLastIndex, delta)
	if err := manager.SetStakingGlobalIndex(globalIndex); err != nil {
		t.Fatalf("set global index: %v", err)
	}

	minted, err := sp.StakeClaimRewards(delegator[:])
	if err != nil {
		t.Fatalf("claim rewards: %v", err)
	}
	mintedCopy := new(big.Int).Set(minted)

	postClaim, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account after claim: %v", err)
	}

	sp.nowFunc = func() time.Time { return claimTime }
	unbondAmount := new(big.Int).Set(postClaim.LockedZNHB)
	if unbondAmount.Sign() <= 0 {
		t.Fatalf("expected locked balance to unstake")
	}
	unbond, err := sp.StakeUndelegate(delegator[:], unbondAmount)
	if err != nil {
		t.Fatalf("stake undelegate: %v", err)
	}
	if unbond.Amount.Cmp(unbondAmount) != 0 {
		t.Fatalf("unexpected unbond amount: got %s want %s", unbond.Amount, unbondAmount)
	}
	expectedRelease := uint64(claimTime.Add(unbondingPeriod).Unix())
	if unbond.ReleaseTime != expectedRelease {
		t.Fatalf("unexpected release time: got %d want %d", unbond.ReleaseTime, expectedRelease)
	}

	preClaimAccount, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get account before final claim: %v", err)
	}
	totalWithPending := new(big.Int).Add(new(big.Int).Set(preClaimAccount.BalanceZNHB), new(big.Int).Set(preClaimAccount.LockedZNHB))
	pendingTotal := big.NewInt(0)
	for _, entry := range preClaimAccount.PendingUnbonds {
		pendingTotal.Add(pendingTotal, entry.Amount)
	}
	totalWithPending.Add(totalWithPending, pendingTotal)
	expectedTotal := new(big.Int).Add(initialTotal, mintedCopy)
	if totalWithPending.Cmp(expectedTotal) != 0 {
		t.Fatalf("unexpected total with pending: got %s want %s", totalWithPending, expectedTotal)
	}

	releaseTime := claimTime.Add(unbondingPeriod + time.Hour)
	sp.nowFunc = func() time.Time { return releaseTime }
	if _, err := sp.StakeClaim(delegator[:], unbond.ID); err != nil {
		t.Fatalf("stake claim: %v", err)
	}

	finalAcct, err := sp.getAccount(delegator[:])
	if err != nil {
		t.Fatalf("get final account: %v", err)
	}
	if len(finalAcct.PendingUnbonds) != 0 {
		t.Fatalf("expected no pending unbonds")
	}
	if finalAcct.LockedZNHB.Sign() != 0 {
		t.Fatalf("expected no locked stake")
	}
	finalTotal := new(big.Int).Add(new(big.Int).Set(finalAcct.BalanceZNHB), new(big.Int).Set(finalAcct.LockedZNHB))
	if finalTotal.Cmp(expectedTotal) != 0 {
		t.Fatalf("unexpected final total: got %s want %s", finalTotal, expectedTotal)
	}
}

func expectedStakeMint(t *testing.T, account *types.Account, globalIndex *big.Int, now time.Time) (*big.Int, *big.Int) {
	t.Helper()
	if account == nil || globalIndex == nil {
		return big.NewInt(0), big.NewInt(0)
	}
	ensureAccountDefaults(account)

	currentIndex := new(big.Int).Set(globalIndex)
	lastIndex := new(big.Int).Set(account.StakeLastIndex)
	deltaIndex := currentIndex.Sub(currentIndex, lastIndex)
	if deltaIndex.Sign() <= 0 || account.StakeShares.Sign() <= 0 {
		return big.NewInt(0), big.NewInt(0)
	}

	nowTs := uint64(now.Unix())
	if nowTs <= account.StakeLastPayoutTs {
		return big.NewInt(0), big.NewInt(0)
	}
	elapsed := nowTs - account.StakeLastPayoutTs
	periodSeconds := uint64(stakePayoutPeriodSeconds)
	if periodSeconds == 0 {
		t.Fatalf("invalid stake payout period")
	}
	periods := elapsed / periodSeconds
	if periods == 0 {
		return big.NewInt(0), big.NewInt(0)
	}
	eligibleSeconds := periods * periodSeconds
	eligibleDelta := new(big.Int).Mul(deltaIndex, new(big.Int).SetUint64(eligibleSeconds))
	eligibleDelta.Quo(eligibleDelta, new(big.Int).SetUint64(elapsed))
	if eligibleDelta.Sign() <= 0 {
		return big.NewInt(0), big.NewInt(0)
	}
	minted := new(big.Int).Mul(eligibleDelta, account.StakeShares)
	return minted, eligibleDelta
}
