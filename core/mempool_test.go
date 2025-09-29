package core

import (
	"errors"
	"math/big"
	"sync"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

func TestNodeMempoolConcurrentAdds(t *testing.T) {
	node := newTestNode(t)
	node.SetMempoolUnlimitedOptIn(true)
	node.SetMempoolLimit(0)
	node.SetTransactionSimulationEnabled(false)

	const producers = 2
	const perProducer = 2
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				key, err := crypto.GeneratePrivateKey()
				if err != nil {
					t.Errorf("generate key: %v", err)
					return
				}
				tx := prepareSignedTransaction(t, node, key, 0, types.NHBChainID())
				if err := node.AddTransaction(tx); err != nil {
					t.Errorf("add transaction: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	txs := node.GetMempool()
	expected := producers * perProducer
	if len(txs) != expected {
		t.Fatalf("expected %d transactions, got %d", expected, len(txs))
	}
}

func TestNodeMempoolLimitEnforcedConcurrently(t *testing.T) {
	node := newTestNode(t)
	const limit = 3
	node.SetMempoolLimit(limit)
	node.SetTransactionSimulationEnabled(false)

	const workers = 2
	const perWorker = 3
	var wg sync.WaitGroup
	var mu sync.Mutex
	var fullCount int
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				key, err := crypto.GeneratePrivateKey()
				if err != nil {
					t.Errorf("generate key: %v", err)
					return
				}
				tx := prepareSignedTransaction(t, node, key, 0, types.NHBChainID())
				err = node.AddTransaction(tx)
				if err != nil {
					if !errors.Is(err, ErrMempoolFull) {
						t.Errorf("unexpected error: %v", err)
						return
					}
					mu.Lock()
					fullCount++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	txs := node.GetMempool()
	if len(txs) != limit {
		t.Fatalf("expected %d transactions in mempool, got %d", limit, len(txs))
	}
	if fullCount == 0 {
		t.Fatalf("expected ErrMempoolFull under load")
	}
}

func prepareSignedTransaction(t *testing.T, node *Node, key *crypto.PrivateKey, nonce uint64, chainID *big.Int) *types.Transaction {
	t.Helper()
	ensureAccountState(t, node, key, nonce)
	if chainID == nil {
		chainID = types.NHBChainID()
	}
	tx := &types.Transaction{
		ChainID: chainID,
		Type:    types.TxTypeHeartbeat,
		Nonce:   nonce,
		Value:   big.NewInt(0),
	}
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign transaction: %v", err)
	}
	return tx
}

func ensureAccountState(t *testing.T, node *Node, key *crypto.PrivateKey, nonce uint64) {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	manager := nhbstate.NewManager(node.state.Trie)
	addr := key.PubKey().Address().Bytes()
	account := &types.Account{
		Nonce:      nonce,
		BalanceNHB: big.NewInt(1_000_000_000_000),
	}
	if err := manager.PutAccount(addr, account); err != nil {
		t.Fatalf("put account: %v", err)
	}
}
