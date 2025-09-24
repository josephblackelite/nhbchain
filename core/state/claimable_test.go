package state

import (
	"errors"
	"math/big"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/claimable"
	"nhbchain/core/types"
)

func fundAccount(t *testing.T, manager *Manager, addr [20]byte, nhb, znhb int64) {
	t.Helper()
	account := &types.Account{
		BalanceNHB:  big.NewInt(nhb),
		BalanceZNHB: big.NewInt(znhb),
		Stake:       big.NewInt(0),
	}
	if err := manager.PutAccount(addr[:], account); err != nil {
		t.Fatalf("put account: %v", err)
	}
}

func TestClaimableCreateAndClaim(t *testing.T) {
	manager := newTestManager(t)
	var payer [20]byte
	payer[19] = 1
	fundAccount(t, manager, payer, 1000, 0)

	preimage := []byte("super-secret")
	hash := ethcrypto.Keccak256(preimage)
	var hashLock [32]byte
	copy(hashLock[:], hash)

	deadline := int64(500)
	claim, err := manager.CreateClaimable(payer, "NHB", big.NewInt(100), hashLock, deadline)
	if err != nil {
		t.Fatalf("create claimable: %v", err)
	}
	if claim.Status != claimable.ClaimStatusInit {
		t.Fatalf("expected status init, got %v", claim.Status)
	}

	var payee [20]byte
	payee[19] = 2

	if _, _, err := manager.ClaimableClaim(claim.ID, []byte("bad"), payee); !errors.Is(err, claimable.ErrInvalidPreimage) {
		t.Fatalf("expected invalid preimage error, got %v", err)
	}

	updated, changed, err := manager.ClaimableClaim(claim.ID, preimage, payee)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !changed {
		t.Fatalf("expected state change on first claim")
	}
	if updated.Status != claimable.ClaimStatusClaimed {
		t.Fatalf("expected claimed status, got %v", updated.Status)
	}

	payerAcc, err := manager.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get payer: %v", err)
	}
	if want := big.NewInt(900); payerAcc.BalanceNHB.Cmp(want) != 0 {
		t.Fatalf("unexpected payer balance: got %s want %s", payerAcc.BalanceNHB, want)
	}
	payeeAcc, err := manager.GetAccount(payee[:])
	if err != nil {
		t.Fatalf("get payee: %v", err)
	}
	if want := big.NewInt(100); payeeAcc.BalanceNHB.Cmp(want) != 0 {
		t.Fatalf("unexpected payee balance: got %s want %s", payeeAcc.BalanceNHB, want)
	}

	_, replayChanged, err := manager.ClaimableClaim(claim.ID, preimage, payee)
	if err != nil {
		t.Fatalf("claim replay: %v", err)
	}
	if replayChanged {
		t.Fatalf("expected replay claim to be no-op")
	}
}

func TestClaimableCancelAndExpire(t *testing.T) {
	manager := newTestManager(t)
	var payer [20]byte
	payer[19] = 3
	fundAccount(t, manager, payer, 0, 1000)

	var hashLock [32]byte
	deadline := int64(100)
	claim, err := manager.CreateClaimable(payer, "ZNHB", big.NewInt(200), hashLock, deadline)
	if err != nil {
		t.Fatalf("create claimable: %v", err)
	}

	// Cancel before deadline
	cancelled, changed, err := manager.ClaimableCancel(claim.ID, payer, deadline-1)
	if err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if !changed {
		t.Fatalf("expected cancel to change state")
	}
	if cancelled.Status != claimable.ClaimStatusCancelled {
		t.Fatalf("expected cancelled status, got %v", cancelled.Status)
	}
	payerAcc, err := manager.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get payer: %v", err)
	}
	if want := big.NewInt(1000); payerAcc.BalanceZNHB.Cmp(want) != 0 {
		t.Fatalf("payer balance not restored after cancel: got %s want %s", payerAcc.BalanceZNHB, want)
	}

	_, replayCancel, err := manager.ClaimableCancel(claim.ID, payer, deadline-1)
	if err != nil {
		t.Fatalf("cancel replay: %v", err)
	}
	if replayCancel {
		t.Fatalf("expected cancel replay to be no-op")
	}

	// Expire path
	fundAccount(t, manager, payer, 500, 1000) // replenish NHB for second claimable
	deadlineExpire := int64(50)
	second, err := manager.CreateClaimable(payer, "NHB", big.NewInt(300), hashLock, deadlineExpire)
	if err != nil {
		t.Fatalf("create second claimable: %v", err)
	}
	if _, _, err := manager.ClaimableExpire(second.ID, deadlineExpire-1); !errors.Is(err, claimable.ErrNotExpired) {
		t.Fatalf("expected not expired error, got %v", err)
	}
	expired, changed, err := manager.ClaimableExpire(second.ID, deadlineExpire)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if !changed {
		t.Fatalf("expected expire to change state")
	}
	if expired.Status != claimable.ClaimStatusExpired {
		t.Fatalf("expected expired status, got %v", expired.Status)
	}
	payerAcc, err = manager.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get payer after expire: %v", err)
	}
	if want := big.NewInt(500); payerAcc.BalanceNHB.Cmp(want) != 0 {
		t.Fatalf("payer NHB balance not restored after expire: got %s want %s", payerAcc.BalanceNHB, want)
	}
	_, replayExpire, err := manager.ClaimableExpire(second.ID, deadlineExpire+10)
	if err != nil {
		t.Fatalf("expire replay: %v", err)
	}
	if replayExpire {
		t.Fatalf("expected expire replay to be no-op")
	}
}
