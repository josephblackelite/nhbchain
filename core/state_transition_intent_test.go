package core

import (
	"encoding/json"
	"errors"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestApplyTransactionConsumesIntent(t *testing.T) {
	sp := newStakingStateProcessor(t)
	fixed := time.Unix(1_700_000_000, 0).UTC()
	sp.nowFunc = func() time.Time { return fixed }
	sp.BeginBlock(1, fixed)
	defer sp.EndBlock()

	priv, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := priv.PubKey().Address().Bytes()
	account := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	if err := sp.setAccount(addr, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	ref := []byte("pos-intent-1234")
	expiry := uint64(fixed.Add(2 * time.Hour).Unix())
	tx := buildHeartbeatTx(t, priv, 0, ref, expiry, fixed)

	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply transaction: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	record, ok, err := manager.IntentRegistryGet(ref)
	if err != nil {
		t.Fatalf("intent lookup: %v", err)
	}
	if !ok {
		t.Fatalf("intent record missing")
	}
	if !record.Consumed {
		t.Fatalf("expected intent to be consumed")
	}
	// Expiry should be clamped to the TTL window (24h) when necessary.
	maxExpiry := uint64(fixed.Add(defaultIntentTTL).Unix())
	expectedExpiry := expiry
	if expectedExpiry > maxExpiry {
		expectedExpiry = maxExpiry
	}
	if record.Expiry != expectedExpiry {
		t.Fatalf("unexpected expiry: got %d want %d", record.Expiry, expectedExpiry)
	}

	foundEvent := false
	for _, evt := range sp.events {
		if evt.Type == "payments.intent_consumed" {
			foundEvent = true
			if evt.Attributes["intentRef"] == "" {
				t.Fatalf("intentRef attribute missing")
			}
			if evt.Attributes["txHash"] == "" {
				t.Fatalf("txHash attribute missing")
			}
		}
	}
	if !foundEvent {
		t.Fatalf("expected payments.intent_consumed event")
	}
}

func TestApplyTransactionRejectsConsumedIntent(t *testing.T) {
	sp := newStakingStateProcessor(t)
	fixed := time.Unix(1_700_000_000, 0).UTC()
	sp.nowFunc = func() time.Time { return fixed }
	sp.BeginBlock(1, fixed)
	defer sp.EndBlock()

	priv, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := priv.PubKey().Address().Bytes()
	account := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	if err := sp.setAccount(addr, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	ref := []byte("pos-intent-duplicate")
	expiry := uint64(fixed.Add(time.Hour).Unix())
	tx := buildHeartbeatTx(t, priv, 0, ref, expiry, fixed)
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("first apply: %v", err)
	}

	txReplay := buildHeartbeatTx(t, priv, 1, ref, expiry, fixed.Add(time.Minute))
	if err := sp.ApplyTransaction(txReplay); !errors.Is(err, nhbstate.ErrIntentConsumed) {
		t.Fatalf("expected ErrIntentConsumed, got %v", err)
	}
}

func TestApplyTransactionRejectsExpiredIntent(t *testing.T) {
	sp := newStakingStateProcessor(t)
	fixed := time.Unix(1_700_000_000, 0).UTC()
	sp.nowFunc = func() time.Time { return fixed }
	sp.BeginBlock(1, fixed)
	defer sp.EndBlock()

	priv, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := priv.PubKey().Address().Bytes()
	account := &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}
	if err := sp.setAccount(addr, account); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	ref := []byte("pos-intent-expired")
	expiry := uint64(fixed.Add(-time.Minute).Unix())
	tx := buildHeartbeatTx(t, priv, 0, ref, expiry, fixed)

	if err := sp.ApplyTransaction(tx); !errors.Is(err, nhbstate.ErrIntentExpired) {
		t.Fatalf("expected ErrIntentExpired, got %v", err)
	}
}

func buildHeartbeatTx(t *testing.T, priv *crypto.PrivateKey, nonce uint64, ref []byte, expiry uint64, ts time.Time) *types.Transaction {
	t.Helper()
	payload := types.HeartbeatPayload{Timestamp: ts.Unix(), DeviceID: "device-1"}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal heartbeat: %v", err)
	}
	tx := &types.Transaction{
		ChainID:         types.NHBChainID(),
		Type:            types.TxTypeHeartbeat,
		Nonce:           nonce,
		Data:            data,
		GasLimit:        21000,
		GasPrice:        big.NewInt(1),
		IntentRef:       append([]byte(nil), ref...),
		IntentExpiry:    expiry,
		MerchantAddress: "merchant-123",
		DeviceID:        "device-1",
	}
	if err := tx.Sign(priv.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	return tx
}
