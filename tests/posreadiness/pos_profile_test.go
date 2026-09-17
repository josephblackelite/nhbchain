//go:build posreadiness

package posreadiness

import (
	"bytes"
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/pos"
	"nhbchain/tests/posreadiness/harness"
	security "nhbchain/tests/posreadiness/security"
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

func buildTransferTx(key *crypto.PrivateKey, nonce uint64) (*types.Transaction, error) {
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    nonce,
		To:       append([]byte(nil), key.PubKey().Address().Bytes()...),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
		Value:    big.NewInt(0),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		return nil, err
	}
	return tx, nil
}

func TestIntentReadiness(t *testing.T) {
	chain := newMiniChain(t)
	block, err := chain.FinalizeTxs()
	if err != nil {
		t.Fatalf("finalize empty block: %v", err)
	}
	if block == nil {
		t.Fatalf("expected block")
	}
	if got := chain.Node().Chain().GetHeight(); got == 0 {
		t.Fatalf("expected non-zero height after commit")
	}
}

func TestPaymasterReadiness(t *testing.T) {
	chain := newMiniChain(t)
	node := chain.Node()
	limits := core.PaymasterLimits{GlobalDailyCapWei: big.NewInt(1)}
	node.SetPaymasterLimits(limits)
	snapshot := node.PaymasterLimits()
	if snapshot.GlobalDailyCapWei == nil || snapshot.GlobalDailyCapWei.Cmp(limits.GlobalDailyCapWei) != 0 {
		t.Fatalf("unexpected limits snapshot: %+v", snapshot)
	}
}

func TestRegistryReadiness(t *testing.T) {
	chain := newMiniChain(t)
	putErr := chain.Node().WithState(func(m *nhbstate.Manager) error {
		return m.POSPutDevice(&pos.Device{DeviceID: "device-1", Merchant: "merchant-1"})
	})
	if putErr != nil {
		t.Fatalf("put device: %v", putErr)
	}
	var fetched *pos.Device
	var exists bool
	getErr := chain.Node().WithState(func(m *nhbstate.Manager) error {
		record, ok, err := m.POSGetDevice("device-1")
		if err != nil {
			return err
		}
		fetched = record
		exists = ok
		return nil
	})
	if getErr != nil {
		t.Fatalf("get device: %v", getErr)
	}
	if !exists || fetched.DeviceID != "device-1" {
		t.Fatalf("unexpected registry fetch: %#v exists=%v", fetched, exists)
	}
}

func TestRealtimeReadiness(t *testing.T) {
	chain := newMiniChain(t)
	node := chain.Node()
	node.SetTransactionSimulationEnabled(false)

	sender, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender: %v", err)
	}
	recipient, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient: %v", err)
	}
	if err := seedAccount(node, sender, big.NewInt(1_000_000)); err != nil {
		t.Fatalf("seed sender: %v", err)
	}
	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("commit funding block: %v", err)
	}

	intentRef := []byte("intent-readiness-1")
	tx := &types.Transaction{
		ChainID:      types.NHBChainID(),
		Type:         types.TxTypeTransfer,
		Nonce:        0,
		To:           recipient.PubKey().Address().Bytes(),
		Value:        big.NewInt(1),
		GasLimit:     21_000,
		GasPrice:     big.NewInt(1),
		IntentExpiry: uint64(time.Now().Add(time.Hour).Unix()),
		IntentRef:    append([]byte(nil), intentRef...),
	}
	if err := tx.Sign(sender.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	txHash, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash tx: %v", err)
	}

	updates, cancel, backlog, err := node.POSFinalitySubscribe(context.Background(), "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()
	if len(backlog) != 0 {
		t.Fatalf("expected empty backlog, got %d", len(backlog))
	}

	if err := node.SubmitTransaction(tx); err != nil {
		t.Fatalf("submit tx: %v", err)
	}

	pending := waitForFinalityUpdate(t, updates)
	if pending.Status != core.POSFinalityStatusPending {
		t.Fatalf("unexpected pending status: %s", pending.Status)
	}
	if !bytes.Equal(pending.IntentRef, intentRef) {
		t.Fatalf("unexpected pending intent ref: %x", pending.IntentRef)
	}
	if !bytes.Equal(pending.TxHash, txHash) {
		t.Fatalf("unexpected pending tx hash: %x", pending.TxHash)
	}

	block, err := chain.FinalizeTxs(tx)
	if err != nil {
		t.Fatalf("finalize block: %v", err)
	}
	if block == nil || block.Header == nil {
		t.Fatalf("expected finalized block")
	}
	blockHash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}

	finalized := waitForFinalityUpdate(t, updates)
	if finalized.Status != core.POSFinalityStatusFinalized {
		t.Fatalf("unexpected finalized status: %s", finalized.Status)
	}
	if !bytes.Equal(finalized.IntentRef, intentRef) {
		t.Fatalf("unexpected finalized intent ref: %x", finalized.IntentRef)
	}
	if !bytes.Equal(finalized.TxHash, txHash) {
		t.Fatalf("unexpected finalized tx hash: %x", finalized.TxHash)
	}
	if !bytes.Equal(finalized.BlockHash, blockHash) {
		t.Fatalf("unexpected block hash: %x", finalized.BlockHash)
	}
	if finalized.Height != block.Header.Height {
		t.Fatalf("unexpected block height: %d", finalized.Height)
	}
	if finalized.Timestamp != block.Header.Timestamp {
		t.Fatalf("unexpected timestamp: %d", finalized.Timestamp)
	}
}

func waitForFinalityUpdate(t *testing.T, updates <-chan core.POSFinalityUpdate) core.POSFinalityUpdate {
	t.Helper()
	select {
	case <-time.After(5 * time.Second):
		t.Fatalf("expected finality update")
		return core.POSFinalityUpdate{}
	case update, ok := <-updates:
		if !ok {
			t.Fatalf("updates channel closed")
		}
		return update
	}
}

func TestSecurityReadiness(t *testing.T) {
	t.Run("MempoolAdmission", func(t *testing.T) {
		chain := newMiniChain(t)
		node := chain.Node()
		node.SetTransactionSimulationEnabled(false)
		if _, err := chain.FinalizeTxs(); err != nil {
			t.Fatalf("finalize genesis block: %v", err)
		}
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		tx, err := buildTransferTx(key, 0)
		if err != nil {
			t.Fatalf("build tx: %v", err)
		}
		if err := node.SubmitTransaction(tx); err != nil {
			t.Fatalf("submit tx: %v", err)
		}
		mempool := node.GetMempool()
		if len(mempool) == 0 {
			t.Fatalf("expected mempool entry")
		}
	})

	t.Run("Transport", security.RunTransportSuite)
}

func TestFeesReadiness(t *testing.T) {
	chain := newMiniChain(t)
	node := chain.Node()
	node.SetMempoolLimit(1)
	node.SetTransactionSimulationEnabled(false)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := seedAccount(node, key, big.NewInt(1_000_000)); err != nil {
		t.Fatalf("seed account: %v", err)
	}

	first, err := buildTransferTx(key, 0)
	if err != nil {
		t.Fatalf("build first tx: %v", err)
	}
	if err := node.SubmitTransaction(first); err != nil {
		t.Fatalf("submit first tx: %v", err)
	}
	second, err := buildTransferTx(key, 1)
	if err != nil {
		t.Fatalf("build second tx: %v", err)
	}
	if err := node.SubmitTransaction(second); !errors.Is(err, core.ErrMempoolFull) {
		t.Fatalf("expected mempool full error, got %v", err)
	}
}

func BenchmarkPOSQOS(b *testing.B) {
	chain, err := harness.NewMiniChain()
	if err != nil {
		b.Fatalf("new mini chain: %v", err)
	}
	defer chain.Close()
	node := chain.Node()
	node.SetTransactionSimulationEnabled(false)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		b.Fatalf("generate key: %v", err)
	}
	if err := seedAccount(node, key, big.NewInt(1_000_000)); err != nil {
		b.Fatalf("seed account: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tx, err := buildTransferTx(key, uint64(i))
		if err != nil {
			b.Fatalf("build tx: %v", err)
		}
		if err := node.SubmitTransaction(tx); err != nil && !errors.Is(err, core.ErrMempoolFull) {
			b.Fatalf("submit tx: %v", err)
		}
		if _, err := chain.FinalizeTxs(); err != nil {
			b.Fatalf("finalize: %v", err)
		}
	}
}
