//go:build posreadiness

package intent

import (
	"encoding/hex"
	"errors"
	"math/big"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/tests/posreadiness/harness"
)

func newMiniChain(t *testing.T) *harness.MiniChain {
	t.Helper()
	chain, err := harness.NewMiniChain()
	if err != nil {
		t.Fatalf("new mini chain: %v", err)
	}
	t.Cleanup(func() {
		if err := chain.Close(); err != nil {
			t.Fatalf("close minichain: %v", err)
		}
	})
	return chain
}

func seedAccount(node *core.Node, key *crypto.PrivateKey, balance *big.Int) error {
	return node.WithState(func(m *nhbstate.Manager) error {
		account := &types.Account{BalanceNHB: new(big.Int).Set(balance), BalanceZNHB: big.NewInt(0)}
		return m.PutAccount(key.PubKey().Address().Bytes(), account)
	})
}

func buildIntentHeartbeatTx(key *crypto.PrivateKey, nonce uint64, intentRef []byte, expiry uint64, merchant, device string) (*types.Transaction, error) {
	tx := &types.Transaction{
		ChainID:         types.NHBChainID(),
		Type:            types.TxTypeHeartbeat,
		Nonce:           nonce,
		GasLimit:        1,
		GasPrice:        big.NewInt(1),
		Value:           big.NewInt(0),
		IntentRef:       append([]byte(nil), intentRef...),
		IntentExpiry:    expiry,
		MerchantAddress: merchant,
		DeviceID:        device,
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		return nil, err
	}
	return tx, nil
}

func TestValidIntentFinalizes(t *testing.T) {
	chain := newMiniChain(t)
	node := chain.Node()
	node.SetTransactionSimulationEnabled(false)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := seedAccount(node, key, big.NewInt(1_000_000)); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("commit seed block: %v", err)
	}

	intentRef := []byte("intent-valid-finalize")
	expiry := uint64(time.Now().Add(2 * time.Minute).Unix())
	merchant := "merchant-123"
	device := "device-abc"
	tx, err := buildIntentHeartbeatTx(key, 0, intentRef, expiry, merchant, device)
	if err != nil {
		t.Fatalf("build intent tx: %v", err)
	}

	if err := node.SubmitTransaction(tx); err != nil {
		t.Fatalf("submit intent tx: %v", err)
	}
	if _, err := chain.FinalizeTxs(tx); err != nil {
		t.Fatalf("finalize block: %v", err)
	}

	hash, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash tx: %v", err)
	}
	encodedRef := hex.EncodeToString(intentRef)
	encodedHash := "0x" + hex.EncodeToString(hash)

	var matched bool
	for _, evt := range node.Events() {
		if evt.Type != events.TypePaymentIntentConsumed {
			continue
		}
		if evt.Attributes["intentRef"] != encodedRef {
			continue
		}
		if evt.Attributes["txHash"] != encodedHash {
			t.Fatalf("unexpected tx hash: %q", evt.Attributes["txHash"])
		}
		if evt.Attributes["merchantAddr"] != merchant {
			t.Fatalf("unexpected merchant attribute: %q", evt.Attributes["merchantAddr"])
		}
		if evt.Attributes["deviceId"] != device {
			t.Fatalf("unexpected device attribute: %q", evt.Attributes["deviceId"])
		}
		matched = true
		break
	}
	if !matched {
		t.Fatalf("payment intent consumed event not emitted")
	}
}

func TestDuplicateIntentRejected(t *testing.T) {
	chain := newMiniChain(t)
	node := chain.Node()
	node.SetTransactionSimulationEnabled(false)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := seedAccount(node, key, big.NewInt(1_000_000)); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("commit seed block: %v", err)
	}

	intentRef := []byte("intent-duplicate")
	expiry := uint64(time.Now().Add(2 * time.Minute).Unix())

	first, err := buildIntentHeartbeatTx(key, 0, intentRef, expiry, "merchant-dup", "device-dup")
	if err != nil {
		t.Fatalf("build first intent tx: %v", err)
	}
	if err := node.SubmitTransaction(first); err != nil {
		t.Fatalf("submit first intent: %v", err)
	}
	if _, err := chain.FinalizeTxs(first); err != nil {
		t.Fatalf("finalize first intent: %v", err)
	}

	node.SetTransactionSimulationEnabled(true)

	second, err := buildIntentHeartbeatTx(key, 1, intentRef, expiry, "merchant-dup", "device-dup")
	if err != nil {
		t.Fatalf("build duplicate intent tx: %v", err)
	}
	err = node.SubmitTransaction(second)
	if err == nil {
		t.Fatalf("expected duplicate intent rejection")
	}
	if !errors.Is(err, core.ErrInvalidTransaction) {
		t.Fatalf("expected invalid transaction error, got %v", err)
	}
	if !errors.Is(err, nhbstate.ErrIntentConsumed) {
		t.Fatalf("expected intent consumed error, got %v", err)
	}
}

func TestExpiredIntentRejected(t *testing.T) {
	chain := newMiniChain(t)
	node := chain.Node()
	node.SetTransactionSimulationEnabled(false)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := seedAccount(node, key, big.NewInt(1_000_000)); err != nil {
		t.Fatalf("seed account: %v", err)
	}
	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("commit seed block: %v", err)
	}

	intentRef := []byte("intent-expired")
	expiry := uint64(time.Now().Add(-1 * time.Minute).Unix())
	node.SetTransactionSimulationEnabled(true)

	tx, err := buildIntentHeartbeatTx(key, 0, intentRef, expiry, "merchant-expired", "device-expired")
	if err != nil {
		t.Fatalf("build expired intent tx: %v", err)
	}

	err = node.SubmitTransaction(tx)
	if err == nil {
		t.Fatalf("expected expired intent rejection")
	}
	if !errors.Is(err, core.ErrInvalidTransaction) {
		t.Fatalf("expected invalid transaction error, got %v", err)
	}
	if !errors.Is(err, nhbstate.ErrIntentExpired) {
		t.Fatalf("expected intent expired error, got %v", err)
	}
}
