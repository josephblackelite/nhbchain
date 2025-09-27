package core

import (
	"errors"
	"sync"
	"testing"

	"nhbchain/core/types"
)

func TestNodeMempoolConcurrentAdds(t *testing.T) {
	node := newTestNode(t)
	node.SetMempoolLimit(0)

	const producers = 32
	const perProducer = 64
	var wg sync.WaitGroup
	for p := 0; p < producers; p++ {
		base := p * perProducer
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < perProducer; i++ {
				tx := &types.Transaction{
					ChainID: types.NHBChainID(),
					Nonce:   uint64(base + i),
				}
				if err := node.AddTransaction(tx); err != nil {
					t.Errorf("add transaction: %v", err)
				}
			}
		}(base)
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
	const limit = 75
	node.SetMempoolLimit(limit)

	const workers = 10
	const perWorker = 20
	var wg sync.WaitGroup
	var mu sync.Mutex
	var fullCount int
	for w := 0; w < workers; w++ {
		base := w * perWorker
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				tx := &types.Transaction{
					ChainID: types.NHBChainID(),
					Nonce:   uint64(base + i),
				}
				err := node.AddTransaction(tx)
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
		}(base)
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
