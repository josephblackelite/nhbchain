package core

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/potso"
)

func fundAccount(t *testing.T, node *Node, addr [20]byte, amount *big.Int) {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	account, err := manager.GetAccount(addr[:])
	if err != nil {
		t.Fatalf("get account: %v", err)
	}
	account.BalanceZNHB = new(big.Int).Set(amount)
	if err := manager.PutAccount(addr[:], account); err != nil {
		t.Fatalf("put account: %v", err)
	}
}

// submitPotsoStakeTx builds, signs (with key, at nonce), and applies a
// TxTypePotsoStakeLock/Unbond/Withdraw transaction directly against node's
// StateProcessor -- the owner is always the signer, never a payload field
// (see core/potso_stake_tx.go), and replay protection is the standard
// account nonce (checked generically by sp.validateSenderAccount before
// dispatch), replacing the old bespoke authNonce parameter these tests used
// to pass to Node.PotsoStakeLock/Unbond/Withdraw directly.
func submitPotsoStakeTx(t *testing.T, node *Node, key *crypto.PrivateKey, nonce uint64, txType types.TxType, amount *big.Int) error {
	t.Helper()
	var data []byte
	if txType != types.TxTypePotsoStakeWithdraw {
		encoded, err := rlp.EncodeToBytes(struct{ Amount *big.Int }{Amount: amount})
		if err != nil {
			t.Fatalf("encode payload: %v", err)
		}
		data = encoded
	}
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    nonce,
		Data:     data,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	return node.state.ApplyTransaction(tx)
}

func TestPotsoStakeLifecycle(t *testing.T) {
	node := newTestNode(t)
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	owner := toAddress(ownerKey)
	fundAccount(t, node, owner, big.NewInt(1000))

	if err := submitPotsoStakeTx(t, node, ownerKey, 0, types.TxTypePotsoStakeLock, big.NewInt(600)); err != nil {
		t.Fatalf("stake lock: %v", err)
	}
	// Same nonce again must be rejected by the standard account-nonce check
	// -- there is no separate bespoke nonce left to duplicate/stale-check.
	if err := submitPotsoStakeTx(t, node, ownerKey, 0, types.TxTypePotsoStakeLock, big.NewInt(1)); err == nil {
		t.Fatalf("expected stale nonce rejection")
	}
	if err := submitPotsoStakeTx(t, node, ownerKey, 1, types.TxTypePotsoStakeLock, big.NewInt(400)); err != nil {
		t.Fatalf("second stake lock: %v", err)
	}

	info, err := node.PotsoStakeInfo(owner)
	if err != nil {
		t.Fatalf("stake info: %v", err)
	}
	if info.Bonded.String() != "1000" {
		t.Fatalf("expected bonded 1000, got %s", info.Bonded.String())
	}
	if len(info.Locks) != 2 {
		t.Fatalf("expected two locks, got %d", len(info.Locks))
	}

	if err := submitPotsoStakeTx(t, node, ownerKey, 2, types.TxTypePotsoStakeUnbond, big.NewInt(700)); err != nil {
		t.Fatalf("stake unbond: %v", err)
	}
	if err := submitPotsoStakeTx(t, node, ownerKey, 2, types.TxTypePotsoStakeUnbond, big.NewInt(1)); err == nil {
		t.Fatalf("expected stale nonce rejection for unbond")
	}
	info, err = node.PotsoStakeInfo(owner)
	if err != nil {
		t.Fatalf("stake info after unbond: %v", err)
	}
	if info.Bonded.String() != "300" {
		t.Fatalf("expected bonded 300, got %s", info.Bonded.String())
	}
	if info.PendingUnbond.String() != "700" {
		t.Fatalf("expected pending 700, got %s", info.PendingUnbond.String())
	}
	if info.Withdrawable.Sign() != 0 {
		t.Fatalf("expected zero withdrawable, got %s", info.Withdrawable.String())
	}

	// Find the withdrawAt timestamp the unbond just scheduled, to relocate
	// the queue entry into the past below (this test doesn't want to sleep
	// 7 real days).
	var withdrawAt uint64
	for _, lock := range info.Locks {
		if lock.UnbondAt != 0 && lock.WithdrawAt > withdrawAt {
			withdrawAt = lock.WithdrawAt
		}
	}
	if withdrawAt == 0 {
		t.Fatalf("expected an unbonding lock with a scheduled withdrawAt")
	}

	if err := submitPotsoStakeTx(t, node, ownerKey, 3, types.TxTypePotsoStakeWithdraw, nil); err == nil {
		t.Fatalf("expected withdraw to fail before cooldown")
	}

	past := uint64(time.Now().Add(-time.Hour).Unix())
	originalDay := potso.WithdrawDay(withdrawAt)
	newDay := potso.WithdrawDay(past)

	node.stateMu.Lock()
	manager := nhbstate.NewManager(node.state.Trie)
	entries, err := manager.PotsoStakeQueueEntries(originalDay)
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("queue entries: %v", err)
	}
	if err := manager.PotsoStakePutQueueEntries(originalDay, nil); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("clear original queue: %v", err)
	}
	for _, entry := range entries {
		lock, ok, getErr := manager.PotsoStakeGetLock(owner, entry.Nonce)
		if getErr != nil {
			node.stateMu.Unlock()
			t.Fatalf("load lock: %v", getErr)
		}
		if !ok {
			continue
		}
		lock.WithdrawAt = past
		if err := manager.PotsoStakePutLock(owner, entry.Nonce, lock); err != nil {
			node.stateMu.Unlock()
			t.Fatalf("update lock: %v", err)
		}
		entry.Amount = new(big.Int).Set(lock.Amount)
		if err := manager.PotsoStakeQueueAppend(newDay, entry); err != nil {
			node.stateMu.Unlock()
			t.Fatalf("requeue entry: %v", err)
		}
	}
	node.stateMu.Unlock()

	// submitPotsoStakeTx at nonce 3 above failed validation before the tx
	// ever incremented the account nonce, so 3 is still the next valid one.
	if err := submitPotsoStakeTx(t, node, ownerKey, 3, types.TxTypePotsoStakeWithdraw, nil); err != nil {
		t.Fatalf("withdraw matured: %v", err)
	}

	infoAfterWithdraw, err := node.PotsoStakeInfo(owner)
	if err != nil {
		t.Fatalf("stake info after withdraw: %v", err)
	}
	if infoAfterWithdraw.Withdrawable.Sign() != 0 {
		t.Fatalf("expected no withdrawable after payout, got %s", infoAfterWithdraw.Withdrawable.String())
	}

	if err := submitPotsoStakeTx(t, node, ownerKey, 3, types.TxTypePotsoStakeWithdraw, nil); err == nil {
		t.Fatalf("expected stale nonce rejection for withdraw")
	}
	if err := submitPotsoStakeTx(t, node, ownerKey, 4, types.TxTypePotsoStakeWithdraw, nil); err != nil {
		t.Fatalf("idempotent withdraw failed: %v", err)
	}

	events := node.Events()
	if len(events) < 3 {
		t.Fatalf("expected stake events to be recorded")
	}
}
