package core

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	nativecommon "nhbchain/native/common"
	systemquotas "nhbchain/native/system/quotas"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func TestQuotaEnforcementHeartbeat(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("state processor: %v", err)
	}

	sp.SetQuotaConfig(map[string]nativecommon.Quota{
		modulePotso: {MaxRequestsPerMin: 3, EpochSeconds: 60},
	})

	sender := make([]byte, 20)
	sender[0] = 0x42
	account := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
	if err := sp.setAccount(sender, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	start := time.Unix(1_700_000_000, 0).UTC()

	sp.BeginBlock(1, start)
	for i := 0; i < 3; i++ {
		payload := types.HeartbeatPayload{DeviceID: "dev", Timestamp: start.Add(time.Duration(i+1) * time.Minute).Unix()}
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal heartbeat: %v", err)
		}
		tx := &types.Transaction{Type: types.TxTypeHeartbeat, Data: data}
		acc, err := sp.getAccount(sender)
		if err != nil {
			t.Fatalf("get account: %v", err)
		}
		if err := sp.handleNativeTransaction(tx, sender, acc); err != nil {
			t.Fatalf("heartbeat %d: %v", i, err)
		}
	}

	failPayload := types.HeartbeatPayload{DeviceID: "dev", Timestamp: start.Add(4 * time.Minute).Unix()}
	failData, err := json.Marshal(failPayload)
	if err != nil {
		t.Fatalf("marshal fail heartbeat: %v", err)
	}
	failTx := &types.Transaction{Type: types.TxTypeHeartbeat, Data: failData}
	acc, err := sp.getAccount(sender)
	if err != nil {
		t.Fatalf("get account for failure: %v", err)
	}
	if err := sp.handleNativeTransaction(failTx, sender, acc); err == nil || !errors.Is(err, nativecommon.ErrQuotaRequestsExceeded) {
		t.Fatalf("expected quota error, got %v", err)
	}

	events := sp.Events()
	if len(events) == 0 {
		t.Fatalf("expected quota event")
	}
	last := events[len(events)-1]
	if last.Type != "QuotaExceeded" {
		t.Fatalf("unexpected event type: %s", last.Type)
	}
	if reason := last.Attributes["reason"]; reason != "requests" {
		t.Fatalf("unexpected quota reason: %s", reason)
	}

	sp.EndBlock()
	if err := sp.ProcessBlockLifecycle(1, start.Unix()); err != nil {
		t.Fatalf("process block 1: %v", err)
	}

	sp.BeginBlock(2, start.Add(time.Minute))
	nextPayload := types.HeartbeatPayload{DeviceID: "dev", Timestamp: start.Add(5 * time.Minute).Unix()}
	nextData, err := json.Marshal(nextPayload)
	if err != nil {
		t.Fatalf("marshal next heartbeat: %v", err)
	}
	nextTx := &types.Transaction{Type: types.TxTypeHeartbeat, Data: nextData}
	acc, err = sp.getAccount(sender)
	if err != nil {
		t.Fatalf("get account after rollover: %v", err)
	}
	if err := sp.handleNativeTransaction(nextTx, sender, acc); err != nil {
		t.Fatalf("heartbeat after rollover: %v", err)
	}
	sp.EndBlock()
	if err := sp.ProcessBlockLifecycle(2, start.Add(time.Minute).Unix()); err != nil {
		t.Fatalf("process block 2: %v", err)
	}

	sp.BeginBlock(3, start.Add(2*time.Minute))
	sp.EndBlock()
	if err := sp.ProcessBlockLifecycle(3, start.Add(2*time.Minute).Unix()); err != nil {
		t.Fatalf("process block 3: %v", err)
	}

	store := systemquotas.NewStore(nhbstate.NewManager(sp.Trie))
	if _, ok, err := store.Load("potso", 0, sender); err != nil {
		t.Fatalf("load counters: %v", err)
	} else if ok {
		t.Fatalf("expected epoch 0 counters pruned")
	}
}
