//go:build posreadiness

package posreadiness

import (
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
		GasLimit: 1,
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
	limits := core.PaymasterLimits{Global: big.NewInt(1)}
	node.SetPaymasterLimits(limits)
	snapshot := node.PaymasterLimits()
	if snapshot.Global == nil || snapshot.Global.Cmp(limits.Global) != 0 {
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
	updates, cancel, backlog, err := chain.Node().POSFinalitySubscribe(context.Background(), "")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer cancel()
	if len(backlog) != 0 {
		t.Fatalf("expected empty backlog, got %d", len(backlog))
	}
	if _, err := chain.FinalizeTxs(); err != nil {
		t.Fatalf("finalize block: %v", err)
	}
	select {
	case <-time.After(time.Second):
		t.Fatalf("expected finality update")
	case update := <-updates:
		if update.Status != core.POSFinalityStatusFinalized {
			t.Fatalf("unexpected status: %s", update.Status)
		}
	}
}

func TestSecurityReadiness(t *testing.T) {
	chain := newMiniChain(t)
	node := chain.Node()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	if err := seedAccount(node, key, big.NewInt(1_000_000)); err != nil {
		t.Fatalf("seed account: %v", err)
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
