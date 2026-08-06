package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
)

func TestEngagementSubmitHeartbeatDoesNotDeadlockStateLock(t *testing.T) {
	node := newTestNode(t)

	validatorAddr := node.validatorKey.PubKey().Address().Bytes()

	node.stateMu.Lock()
	account, err := node.state.getAccount(validatorAddr)
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("load validator account: %v", err)
	}
	if account == nil {
		account = &types.Account{
			BalanceNHB:  big.NewInt(0),
			BalanceZNHB: big.NewInt(0),
			Stake:       big.NewInt(0),
		}
	}
	if err := node.state.setAccount(validatorAddr, account); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("seed validator account: %v", err)
	}
	node.stateMu.Unlock()

	var validator [20]byte
	copy(validator[:], validatorAddr)
	token, err := node.EngagementRegisterDevice(validator, "validator-heartbeat-test")
	if err != nil {
		t.Fatalf("register device: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := node.EngagementSubmitHeartbeat("validator-heartbeat-test", token, 0)
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("submit heartbeat: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat submission deadlocked")
	}

	node.mempoolMu.Lock()
	defer node.mempoolMu.Unlock()
	if len(node.mempool) != 1 {
		t.Fatalf("expected heartbeat transaction in mempool, got %d", len(node.mempool))
	}
	if node.mempool[0] == nil || node.mempool[0].Type != types.TxTypeHeartbeat {
		t.Fatalf("expected heartbeat transaction in mempool")
	}
}
