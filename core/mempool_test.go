package core

import (
	"errors"
	"math/big"
	"sync"
	"testing"

	"nhbchain/config"
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

func TestCommitBlockFailureRetainsMempool(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tx := prepareSignedTransaction(t, node, key, 0, types.NHBChainID())
	if err := node.AddTransaction(tx); err != nil {
		t.Fatalf("add transaction: %v", err)
	}

	proposed := node.GetMempool()
	if len(proposed) != 1 {
		t.Fatalf("expected 1 transaction from initial proposal, got %d", len(proposed))
	}

	// Subsequent proposal attempts before the commit result should yield no new transactions.
	if again := node.GetMempool(); len(again) != 0 {
		t.Fatalf("expected no additional transactions while proposal in-flight, got %d", len(again))
	}

	header := &types.BlockHeader{
		Height:    node.chain.GetHeight() + 1,
		Timestamp: node.currentTime().Unix(),
		PrevHash:  node.chain.Tip(),
		Validator: node.validatorKey.PubKey().Address().Bytes(),
		TxRoot:    []byte("mismatch"),
	}
	block := types.NewBlock(header, proposed)
	if err := node.CommitBlock(block); err == nil {
		t.Fatalf("expected commit failure due to tx root mismatch")
	}

	reproposed := node.GetMempool()
	if len(reproposed) != 1 {
		t.Fatalf("expected transaction to remain after failed commit, got %d", len(reproposed))
	}
	if reproposed[0] != tx {
		t.Fatalf("expected same transaction pointer after failure")
	}
}

func TestCommitBlockSuccessPrunesMempool(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tx := prepareSignedTransaction(t, node, key, 0, types.NHBChainID())
	if err := node.AddTransaction(tx); err != nil {
		t.Fatalf("add transaction: %v", err)
	}

	proposed := node.GetMempool()
	if len(proposed) != 1 {
		t.Fatalf("expected 1 transaction to propose, got %d", len(proposed))
	}

	block, err := node.CreateBlock(proposed)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	if remaining := node.GetMempool(); len(remaining) != 0 {
		t.Fatalf("expected mempool to be empty after successful commit, got %d", len(remaining))
	}
}

func TestNodeMempoolByteLimit(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)
	node.SetMempoolLimit(10)

	keyA, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	txA := prepareSignedTransaction(t, node, keyA, 0, types.NHBChainID())
	sizeA, err := transactionSize(txA)
	if err != nil {
		t.Fatalf("transaction size: %v", err)
	}
	node.globalCfgMu.Lock()
	node.globalCfg.Mempool.MaxBytes = int64(sizeA)
	node.globalCfgMu.Unlock()

	if err := node.AddTransaction(txA); err != nil {
		t.Fatalf("add first transaction: %v", err)
	}

	keyB, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	txB := prepareSignedTransaction(t, node, keyB, 0, types.NHBChainID())
	if err := node.AddTransaction(txB); err == nil || !errors.Is(err, ErrMempoolByteLimit) {
		t.Fatalf("expected ErrMempoolByteLimit, got %v", err)
	}
}

func TestNodeMempoolPerSenderLimit(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)
	node.SetMempoolLimit(mempoolMaxSenderTx + 5)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	for i := 0; i < mempoolMaxSenderTx; i++ {
		tx := prepareSignedTransaction(t, node, senderKey, uint64(i), types.NHBChainID())
		if err := node.AddTransaction(tx); err != nil {
			t.Fatalf("unexpected add error %d: %v", i, err)
		}
	}
	blockingTx := prepareSignedTransaction(t, node, senderKey, uint64(mempoolMaxSenderTx), types.NHBChainID())
	if err := node.AddTransaction(blockingTx); err == nil || !errors.Is(err, ErrMempoolSenderLimit) {
		t.Fatalf("expected ErrMempoolSenderLimit, got %v", err)
	}
}

func TestNodeMempoolQuotaLimit(t *testing.T) {
	node := newTestNode(t)
	node.SetTransactionSimulationEnabled(false)
	node.SetMempoolLimit(10)
	node.globalCfgMu.Lock()
	node.globalCfg.Quotas.Trade = config.Quota{MaxRequestsPerMin: 1, EpochSeconds: 86400}
	node.globalCfgMu.Unlock()

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	first := prepareSignedTransaction(t, node, senderKey, 0, types.NHBChainID())
	if err := node.AddTransaction(first); err != nil {
		t.Fatalf("add first transaction: %v", err)
	}
	second := prepareSignedTransaction(t, node, senderKey, 1, types.NHBChainID())
	if err := node.AddTransaction(second); err == nil || !errors.Is(err, ErrMempoolQuotaExceeded) {
		t.Fatalf("expected ErrMempoolQuotaExceeded, got %v", err)
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
