package core

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"

	"nhbchain/core/types"
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
