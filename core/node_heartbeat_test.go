package core

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// seedValidatorAccount ensures the test node's own validator address has an
// on-chain account to operate against, mirroring what a real node's genesis
// or prior activity would have already produced.
func seedValidatorAccount(t *testing.T, node *Node) []byte {
	t.Helper()
	validatorAddr := node.validatorKey.PubKey().Address().Bytes()

	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	account, err := node.state.getAccount(validatorAddr)
	if err != nil {
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
		t.Fatalf("seed validator account: %v", err)
	}
	return validatorAddr
}

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

// TestEngagementValidatorHeartbeatDueUsesOnChainStateNotLocalBookkeeping
// exercises the restart-desync bug directly: a heartbeat recorded on the
// real, persisted account state must gate the next attempt even though the
// engagement.Manager backing this node has never seen a single heartbeat
// for this device (exactly what a freshly-restarted validator process looks
// like, since RegisterDevice always resets local bookkeeping to empty).
// Before this fix, "is it time yet" was decided by that empty local map
// instead, which would have reported due=true immediately here -- letting a
// freshly-restarted process fire off a heartbeat transaction that the
// mempool's on-chain-state-based admission check would then reject as too
// soon.
func TestEngagementValidatorHeartbeatDueUsesOnChainStateNotLocalBookkeeping(t *testing.T) {
	node := newTestNode(t)
	validatorAddr := seedValidatorAccount(t, node)

	now := time.Now().UTC()

	// No on-chain heartbeat recorded yet: due immediately, matching
	// applyHeartbeat's own "EngagementLastHeartbeat != 0" guard.
	due, err := node.EngagementValidatorHeartbeatDue(validatorAddr, now)
	if err != nil {
		t.Fatalf("heartbeat due check: %v", err)
	}
	if !due {
		t.Fatalf("expected due=true for an account with no recorded heartbeat")
	}

	// Directly set EngagementLastHeartbeat on the persisted account to
	// "just now", simulating a heartbeat mined by a *previous* process
	// run right before this (simulated) restart. Deliberately do NOT
	// touch node.engagementMgr here -- it stays exactly as fresh as it
	// would be after RegisterDevice on a real restart, with no per-device
	// state at all for this validator, which is the whole point: this
	// check must not need it.
	node.stateMu.Lock()
	account, err := node.state.getAccount(validatorAddr)
	if err != nil {
		node.stateMu.Unlock()
		t.Fatalf("reload validator account: %v", err)
	}
	account.EngagementLastHeartbeat = uint64(now.Unix())
	if err := node.state.setAccount(validatorAddr, account); err != nil {
		node.stateMu.Unlock()
		t.Fatalf("update validator account: %v", err)
	}
	node.stateMu.Unlock()

	due, err = node.EngagementValidatorHeartbeatDue(validatorAddr, now)
	if err != nil {
		t.Fatalf("heartbeat due check after recent on-chain heartbeat: %v", err)
	}
	if due {
		t.Fatalf("restart-desync bug reintroduced: due=true immediately after an on-chain heartbeat")
	}

	interval := node.EngagementHeartbeatInterval()

	// Exactly at the raw interval boundary (zero margin) must still read
	// as NOT due -- this is precisely the race HeartbeatSubmissionMargin
	// exists to close, since second-granularity elapsed time comparisons
	// at exactly the enforced minimum are what produced spurious
	// rejections in production.
	if dueAtBoundary, err := node.EngagementValidatorHeartbeatDue(validatorAddr, now.Add(interval)); err != nil {
		t.Fatalf("heartbeat due check at interval boundary: %v", err)
	} else if dueAtBoundary {
		t.Fatalf("expected due=false exactly at the raw interval with no safety margin")
	}

	// Once interval+margin has comfortably elapsed, it must become due.
	after := now.Add(interval + HeartbeatSubmissionMargin + time.Second)
	if dueAfterMargin, err := node.EngagementValidatorHeartbeatDue(validatorAddr, after); err != nil {
		t.Fatalf("heartbeat due check after margin: %v", err)
	} else if !dueAfterMargin {
		t.Fatalf("expected due=true once interval+margin has elapsed")
	}
}

// TestPendingHeartbeatFeeSurvivesGetMempoolProposalTracking reproduces the
// confirmed root cause of the multi-hour stuck-nonce pattern observed in
// production: GetMempool() marks every transaction it returns as "already
// proposed" and excludes it from later calls until the block that
// (attempted to) include it is resolved via CreateBlock's own bookkeeping.
// A validator that never becomes an active block proposer (or the
// consensus gRPC GetMempool endpoint, if polled by anything) can trigger
// that exclusion without ever resolving it, permanently hiding a pending
// heartbeat from pendingHeartbeatFee's old GetMempool()-based scan. That
// made retries fall back to the default gasPrice of 1, colliding forever
// with the original stuck transaction's own price of 1 ("already exists
// and fee is not higher").
//
// This test proves the fix: a retry must still be able to find and outbid
// the pending transaction even after GetMempool() has already hidden it.
func TestPendingHeartbeatFeeSurvivesGetMempoolProposalTracking(t *testing.T) {
	node := newTestNode(t)
	seedValidatorAccount(t, node)

	var validator [20]byte
	copy(validator[:], node.validatorKey.PubKey().Address().Bytes())
	token, err := node.EngagementRegisterDevice(validator, "pending-fee-test")
	if err != nil {
		t.Fatalf("register device: %v", err)
	}

	base := time.Now().UTC().Unix()
	if _, err := node.EngagementSubmitHeartbeat("pending-fee-test", token, base); err != nil {
		t.Fatalf("first heartbeat: %v", err)
	}

	node.mempoolMu.Lock()
	if len(node.mempool) != 1 {
		count := len(node.mempool)
		node.mempoolMu.Unlock()
		t.Fatalf("expected 1 pending heartbeat tx, got %d", count)
	}
	firstGasPrice := new(big.Int).Set(node.mempool[0].GasPrice)
	node.mempoolMu.Unlock()

	// Simulate a BFT proposal-build call (or the consensus gRPC
	// GetMempool endpoint) observing the mempool once.
	if got := node.GetMempool(); len(got) != 1 {
		t.Fatalf("expected GetMempool to surface the pending heartbeat once, got %d", len(got))
	}
	// A second call must now hide it -- this is GetMempool's documented,
	// intentional behavior for proposal-building, and exactly the
	// condition that broke the old pendingHeartbeatFee implementation.
	if got := node.GetMempool(); len(got) != 0 {
		t.Fatalf("expected GetMempool to hide the already-proposed heartbeat on the next call, got %d", len(got))
	}

	// A retry tick well past the heartbeat interval must still discover
	// and outbid the still-pending transaction, replacing it in place
	// rather than colliding with it.
	retryTs := base + 61
	if _, err := node.EngagementSubmitHeartbeat("pending-fee-test", token, retryTs); err != nil {
		t.Fatalf("retry heartbeat should replace the stuck pending tx, got error: %v", err)
	}

	node.mempoolMu.Lock()
	defer node.mempoolMu.Unlock()
	if len(node.mempool) != 1 {
		t.Fatalf("expected the retry to replace the pending tx in place, got %d entries", len(node.mempool))
	}
	replaced := node.mempool[0]
	if replaced.GasPrice == nil || replaced.GasPrice.Cmp(firstGasPrice) <= 0 {
		t.Fatalf("expected replacement gas price to exceed original %s, got %v", firstGasPrice, replaced.GasPrice)
	}
	var payload types.HeartbeatPayload
	if err := json.Unmarshal(replaced.Data, &payload); err != nil {
		t.Fatalf("decode replaced payload: %v", err)
	}
	if payload.Timestamp != retryTs {
		t.Fatalf("expected replaced tx to carry retry timestamp %d, got %d", retryTs, payload.Timestamp)
	}
}

// TestCreateBlockPrunesRateLimitedHeartbeatWithoutAbortingBlock proves the
// complementary defense-in-depth fix: even if a heartbeat transaction that
// would fail applyHeartbeat's rate-limit check somehow reaches CreateBlock
// (for example because admission-time simulation was disabled, or a caller
// supplied an explicit, already-stale timestamp through the RPC-exposed
// manual submission path), it must be pruned from the proposal rather than
// aborting the whole block -- a validator's own liveness ping must never be
// able to prevent every other pending transaction from being included.
func TestCreateBlockPrunesRateLimitedHeartbeatWithoutAbortingBlock(t *testing.T) {
	node := newTestNode(t)

	hbKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate heartbeat key: %v", err)
	}
	hbAddr := hbKey.PubKey().Address().Bytes()

	now := node.currentTime().UTC()
	if err := node.state.setAccount(hbAddr, &types.Account{
		BalanceNHB:              big.NewInt(0),
		BalanceZNHB:             big.NewInt(0),
		Stake:                   big.NewInt(0),
		EngagementLastHeartbeat: uint64(now.Unix()),
	}); err != nil {
		t.Fatalf("seed heartbeat account: %v", err)
	}

	// A payload timestamp only 1 second past the account's on-chain
	// EngagementLastHeartbeat is well inside the configured interval,
	// guaranteeing applyHeartbeat rejects it with ErrHeartbeatTooSoon.
	payload := types.HeartbeatPayload{DeviceID: "doomed-heartbeat", Timestamp: now.Unix() + 1}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	heartbeatTx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeHeartbeat,
		Nonce:    0,
		Data:     data,
		GasLimit: 21000,
		GasPrice: big.NewInt(1),
	}
	if err := heartbeatTx.Sign(hbKey.PrivateKey); err != nil {
		t.Fatalf("sign heartbeat tx: %v", err)
	}

	validTx := buildIdentityRegistrationTx(t, node, "unrelated-user")

	node.mempool = []*types.Transaction{heartbeatTx, validTx}

	block, err := node.CreateBlock(node.mempool)
	if err != nil {
		t.Fatalf("expected doomed heartbeat to be pruned, not abort the whole block: %v", err)
	}
	if len(block.Transactions) != 1 || block.Transactions[0] != validTx {
		t.Fatalf("expected block to contain only the valid transaction, got %d txs", len(block.Transactions))
	}

	node.mempoolMu.Lock()
	defer node.mempoolMu.Unlock()
	for _, tx := range node.mempool {
		if tx == heartbeatTx {
			t.Fatalf("expected the rate-limited heartbeat transaction to be pruned from the mempool")
		}
	}
}
