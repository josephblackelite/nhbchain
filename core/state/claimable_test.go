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
	claim, err := manager.CreateClaimable(payer, "NHB", big.NewInt(100), hashLock, deadline, [32]byte{}, "test-chain")
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
	claim, err := manager.CreateClaimable(payer, "ZNHB", big.NewInt(200), hashLock, deadline, [32]byte{}, "test-chain")
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
	second, err := manager.CreateClaimable(payer, "NHB", big.NewInt(300), hashLock, deadlineExpire, [32]byte{}, "test-chain")
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

func TestClaimableCreditRollbackOnCommitFailure(t *testing.T) {
	manager := newTestManager(t)
	var payer [20]byte
	payer[19] = 4
	fundAccount(t, manager, payer, 100, 0)

	vault, err := escrowModuleAddress("NHB")
	if err != nil {
		t.Fatalf("vault address: %v", err)
	}
	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	if err := manager.PutAccount(vault[:], &types.Account{BalanceNHB: new(big.Int).Set(maxUint256), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed vault: %v", err)
	}

	if err := manager.ClaimableCredit(payer, "NHB", big.NewInt(1)); err == nil {
		t.Fatalf("expected overflow error when crediting vault")
	}

	payerAcc, err := manager.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get payer: %v", err)
	}
	if want := big.NewInt(100); payerAcc.BalanceNHB.Cmp(want) != 0 {
		t.Fatalf("payer balance mutated on failed credit: got %s want %s", payerAcc.BalanceNHB, want)
	}

	vaultAcc, err := manager.GetAccount(vault[:])
	if err != nil {
		t.Fatalf("get vault: %v", err)
	}
	if vaultAcc.BalanceNHB.Cmp(maxUint256) != 0 {
		t.Fatalf("vault balance changed on failed credit")
	}
}

func TestClaimableCreditInsufficientFunds(t *testing.T) {
	manager := newTestManager(t)
	var payer [20]byte
	payer[19] = 5
	fundAccount(t, manager, payer, 50, 0)

	vault, err := escrowModuleAddress("NHB")
	if err != nil {
		t.Fatalf("vault address: %v", err)
	}
	fundAccount(t, manager, vault, 0, 0)

	if err := manager.ClaimableCredit(payer, "NHB", big.NewInt(60)); !errors.Is(err, claimable.ErrInsufficientFunds) {
		t.Fatalf("expected insufficient funds error, got %v", err)
	}

	payerAcc, err := manager.GetAccount(payer[:])
	if err != nil {
		t.Fatalf("get payer: %v", err)
	}
	if want := big.NewInt(50); payerAcc.BalanceNHB.Cmp(want) != 0 {
		t.Fatalf("payer balance changed after insufficient funds: got %s want %s", payerAcc.BalanceNHB, want)
	}

	vaultAcc, err := manager.GetAccount(vault[:])
	if err != nil {
		t.Fatalf("get vault: %v", err)
	}
	if want := big.NewInt(0); vaultAcc.BalanceNHB.Cmp(want) != 0 {
		t.Fatalf("vault balance changed after insufficient funds: got %s want %s", vaultAcc.BalanceNHB, want)
	}
}

func TestClaimableDebitRollbackOnCommitFailure(t *testing.T) {
	manager := newTestManager(t)
	vault, err := escrowModuleAddress("NHB")
	if err != nil {
		t.Fatalf("vault address: %v", err)
	}
	fundAccount(t, manager, vault, 200, 0)

	var recipient [20]byte
	recipient[19] = 6
	maxUint256 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1))
	fundAccount(t, manager, recipient, 0, 0)
	if err := manager.PutAccount(recipient[:], &types.Account{BalanceNHB: new(big.Int).Set(maxUint256), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed recipient: %v", err)
	}

	if err := manager.ClaimableDebit("NHB", big.NewInt(1), recipient); err == nil {
		t.Fatalf("expected overflow error when debiting vault")
	}

	vaultAcc, err := manager.GetAccount(vault[:])
	if err != nil {
		t.Fatalf("get vault: %v", err)
	}
	if want := big.NewInt(200); vaultAcc.BalanceNHB.Cmp(want) != 0 {
		t.Fatalf("vault balance mutated on failed debit: got %s want %s", vaultAcc.BalanceNHB, want)
	}

	recipientAcc, err := manager.GetAccount(recipient[:])
	if err != nil {
		t.Fatalf("get recipient: %v", err)
	}
	if recipientAcc.BalanceNHB.Cmp(maxUint256) != 0 {
		t.Fatalf("recipient balance changed on failed debit")
	}
}

func TestClaimableDebitInsufficientFunds(t *testing.T) {
	manager := newTestManager(t)
	vault, err := escrowModuleAddress("NHB")
	if err != nil {
		t.Fatalf("vault address: %v", err)
	}
	fundAccount(t, manager, vault, 10, 0)

	var recipient [20]byte
	recipient[19] = 7
	fundAccount(t, manager, recipient, 0, 0)

	if err := manager.ClaimableDebit("NHB", big.NewInt(20), recipient); !errors.Is(err, claimable.ErrInsufficientFunds) {
		t.Fatalf("expected insufficient funds error, got %v", err)
	}

	vaultAcc, err := manager.GetAccount(vault[:])
	if err != nil {
		t.Fatalf("get vault: %v", err)
	}
	if want := big.NewInt(10); vaultAcc.BalanceNHB.Cmp(want) != 0 {
		t.Fatalf("vault balance changed after insufficient funds: got %s want %s", vaultAcc.BalanceNHB, want)
	}

	recipientAcc, err := manager.GetAccount(recipient[:])
	if err != nil {
		t.Fatalf("get recipient: %v", err)
	}
	if want := big.NewInt(0); recipientAcc.BalanceNHB.Cmp(want) != 0 {
		t.Fatalf("recipient balance changed after insufficient funds: got %s want %s", recipientAcc.BalanceNHB, want)
	}
}
