package state

import (
	"errors"
	"math/big"
	"testing"
	"time"

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

// TestClaimableCreateUsesDeterministicBlockTime guards PL-DC-40:
// CreateClaimable used to stamp CreatedAt with time.Now().Unix() -- real
// wall-clock time -- instead of the deterministic block timestamp every
// validator agrees on. Two validators (or one validator replaying the same
// block later, e.g. during catch-up sync) would then compute different
// CreatedAt/ExpiresAt values for an identical claimable purely because of
// clock drift or elapsed wall-clock time, which is a state-root divergence
// hazard the moment this code is reachable. This test proves CreatedAt
// tracks the caller-supplied block time exactly -- identical across two
// independent calls at the same simulated block time, even with real
// wall-clock time elapsing between them -- and never falls back to
// time.Now().
func TestClaimableCreateUsesDeterministicBlockTime(t *testing.T) {
	const fixedBlockTime = int64(1) // deliberately nowhere near real wall-clock time
	const deadline = int64(1_000_000)

	var hashLock [32]byte

	manager1 := newTestManager(t)
	var payer1 [20]byte
	payer1[19] = 1
	fundAccount(t, manager1, payer1, 1000, 0)
	claim1, err := manager1.CreateClaimable(payer1, "NHB", big.NewInt(100), hashLock, deadline, [32]byte{}, "test-chain", claimable.RecipientKindNone, fixedBlockTime)
	if err != nil {
		t.Fatalf("create claimable 1: %v", err)
	}

	// Real wall-clock time elapses here, simulating a validator replaying
	// this exact block well after it was first produced.
	time.Sleep(10 * time.Millisecond)

	manager2 := newTestManager(t)
	var payer2 [20]byte
	payer2[19] = 1
	fundAccount(t, manager2, payer2, 1000, 0)
	claim2, err := manager2.CreateClaimable(payer2, "NHB", big.NewInt(100), hashLock, deadline, [32]byte{}, "test-chain", claimable.RecipientKindNone, fixedBlockTime)
	if err != nil {
		t.Fatalf("create claimable 2: %v", err)
	}

	if claim1.CreatedAt != fixedBlockTime {
		t.Fatalf("CreatedAt must equal the supplied block time exactly: got %d want %d", claim1.CreatedAt, fixedBlockTime)
	}
	if claim1.ExpiresAt != deadline {
		t.Fatalf("ExpiresAt must equal the supplied deadline exactly: got %d want %d", claim1.ExpiresAt, deadline)
	}
	if claim2.CreatedAt != fixedBlockTime {
		t.Fatalf("CreatedAt must equal the supplied block time exactly: got %d want %d", claim2.CreatedAt, fixedBlockTime)
	}
	if claim1.CreatedAt != claim2.CreatedAt {
		t.Fatalf("two calls at the same simulated block time produced different CreatedAt values (%d vs %d) despite wall-clock time elapsing between them -- this is a determinism/consensus hazard", claim1.CreatedAt, claim2.CreatedAt)
	}

	if now := time.Now().Unix(); claim1.CreatedAt == now {
		t.Fatalf("CreatedAt matched wall-clock time.Now() (%d) -- CreateClaimable must stamp CreatedAt from the supplied block time, never time.Now()", now)
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
	blockTime := int64(1)
	claim, err := manager.CreateClaimable(payer, "NHB", big.NewInt(100), hashLock, deadline, [32]byte{}, "test-chain", claimable.RecipientKindNone, blockTime)
	if err != nil {
		t.Fatalf("create claimable: %v", err)
	}
	if claim.Status != claimable.ClaimStatusInit {
		t.Fatalf("expected status init, got %v", claim.Status)
	}

	var payee [20]byte
	payee[19] = 2

	if _, _, err := manager.ClaimableClaim(claim.ID, []byte("bad"), payee, deadline-1); !errors.Is(err, claimable.ErrInvalidPreimage) {
		t.Fatalf("expected invalid preimage error, got %v", err)
	}

	updated, changed, err := manager.ClaimableClaim(claim.ID, preimage, payee, deadline-1)
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

	_, replayChanged, err := manager.ClaimableClaim(claim.ID, preimage, payee, deadline-1)
	if err != nil {
		t.Fatalf("claim replay: %v", err)
	}
	if replayChanged {
		t.Fatalf("expected replay claim to be no-op")
	}
}

// TestClaimableClaimRejectsAfterDeadline is an independent regression test
// for the deadline half of NHB-TRIAGE-C6: ClaimableClaim used to never
// check Deadline at all, so a claim with a correct preimage stayed payable
// forever, even past the point ClaimableExpire considers the funds
// reclaimable by the payer. The two windows must be exact complements --
// valid to claim while now < Deadline, valid to expire once now >=
// Deadline -- with no gap where either party could still win depending on
// who calls first.
func TestClaimableClaimRejectsAfterDeadline(t *testing.T) {
	manager := newTestManager(t)
	var payer [20]byte
	payer[19] = 9
	fundAccount(t, manager, payer, 1000, 0)

	preimage := []byte("past-the-deadline")
	hash := ethcrypto.Keccak256(preimage)
	var hashLock [32]byte
	copy(hashLock[:], hash)

	deadline := int64(1000)
	blockTime := int64(1)
	claim, err := manager.CreateClaimable(payer, "NHB", big.NewInt(100), hashLock, deadline, [32]byte{}, "test-chain", claimable.RecipientKindNone, blockTime)
	if err != nil {
		t.Fatalf("create claimable: %v", err)
	}

	var payee [20]byte
	payee[19] = 10

	if _, _, err := manager.ClaimableClaim(claim.ID, preimage, payee, deadline); !errors.Is(err, claimable.ErrDeadlineExceeded) {
		t.Fatalf("SECURITY: expected deadline-exceeded error claiming exactly at the deadline, got %v", err)
	}
	if _, _, err := manager.ClaimableClaim(claim.ID, preimage, payee, deadline+100); !errors.Is(err, claimable.ErrDeadlineExceeded) {
		t.Fatalf("SECURITY: expected deadline-exceeded error claiming well past the deadline, got %v", err)
	}

	payeeAcc, err := manager.GetAccount(payee[:])
	if err != nil {
		t.Fatalf("get payee: %v", err)
	}
	if payeeAcc.BalanceNHB.Sign() != 0 {
		t.Fatalf("SECURITY: payee received funds from a claim that should have been rejected: %s", payeeAcc.BalanceNHB)
	}

	// The record is still Init (not consumed by the rejected attempts), so
	// the payer's own path -- ClaimableExpire, once the deadline has
	// passed -- must still succeed and return the funds.
	expired, changed, err := manager.ClaimableExpire(claim.ID, deadline)
	if err != nil {
		t.Fatalf("expire after deadline: %v", err)
	}
	if !changed || expired.Status != claimable.ClaimStatusExpired {
		t.Fatalf("expected expire to succeed and mark the record expired, got changed=%v status=%v", changed, expired.Status)
	}

	// And now that it's expired, a claim attempt with the exact right
	// preimage but before the deadline check would matter is still
	// rejected, this time by the status check.
	if _, _, err := manager.ClaimableClaim(claim.ID, preimage, payee, deadline-1); !errors.Is(err, claimable.ErrInvalidState) {
		t.Fatalf("expected invalid-state error claiming an already-expired record, got %v", err)
	}
}

func TestClaimableCancelAndExpire(t *testing.T) {
	manager := newTestManager(t)
	var payer [20]byte
	payer[19] = 3
	fundAccount(t, manager, payer, 0, 1000)

	var hashLock [32]byte
	deadline := int64(100)
	blockTime := int64(1)
	claim, err := manager.CreateClaimable(payer, "ZNHB", big.NewInt(200), hashLock, deadline, [32]byte{}, "test-chain", claimable.RecipientKindNone, blockTime)
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
	second, err := manager.CreateClaimable(payer, "NHB", big.NewInt(300), hashLock, deadlineExpire, [32]byte{}, "test-chain", claimable.RecipientKindNone, blockTime)
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
