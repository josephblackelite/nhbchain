package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
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

func TestPotsoStakeLifecycle(t *testing.T) {
	node := newTestNode(t)
	ownerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate owner key: %v", err)
	}
	owner := toAddress(ownerKey)
	fundAccount(t, node, owner, big.NewInt(1000))

	nonce1, lock1, err := node.PotsoStakeLock(owner, big.NewInt(600))
	if err != nil {
		t.Fatalf("stake lock: %v", err)
	}
	if lock1 == nil || lock1.Amount == nil || lock1.Amount.String() != "600" {
		t.Fatalf("unexpected lock1 amount: %+v", lock1)
	}
	nonce2, _, err := node.PotsoStakeLock(owner, big.NewInt(400))
	if err != nil {
		t.Fatalf("second stake lock: %v", err)
	}
	if nonce2 == nonce1 {
		t.Fatalf("expected distinct nonces")
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

	unbonded, withdrawAt, err := node.PotsoStakeUnbond(owner, big.NewInt(700))
	if err != nil {
		t.Fatalf("stake unbond: %v", err)
	}
	if unbonded == nil || unbonded.String() != "700" {
		t.Fatalf("unexpected unbonded amount %v", unbonded)
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

	if _, err := node.PotsoStakeWithdraw(owner); err == nil {
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

	payouts, err := node.PotsoStakeWithdraw(owner)
	if err != nil {
		t.Fatalf("withdraw matured: %v", err)
	}
	total := big.NewInt(0)
	for _, payout := range payouts {
		total.Add(total, payout.Amount)
	}
	if total.String() != "700" {
		t.Fatalf("expected 700 withdrawn, got %s", total.String())
	}
	if len(payouts) == 0 {
		t.Fatalf("expected at least one payout")
	}

	second, err := node.PotsoStakeWithdraw(owner)
	if err != nil {
		t.Fatalf("idempotent withdraw failed: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("expected no payouts on second withdraw, got %d", len(second))
	}

	info, err = node.PotsoStakeInfo(owner)
	if err != nil {
		t.Fatalf("stake info after withdraw: %v", err)
	}
	if info.Withdrawable.Sign() != 0 {
		t.Fatalf("expected no withdrawable after payout, got %s", info.Withdrawable.String())
	}

	events := node.Events()
	if len(events) < 3 {
		t.Fatalf("expected stake events to be recorded")
	}
}
