package service

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"testing"

	"nhbchain/core"
	"nhbchain/core/types"
	consensusv1 "nhbchain/proto/consensus/v1"
)

type fakeConsensusNode struct {
	mu         sync.Mutex
	validators map[string]*big.Int
}

func newFakeConsensusNode() *fakeConsensusNode {
	return &fakeConsensusNode{validators: map[string]*big.Int{"validator-0": big.NewInt(1)}}
}

func (f *fakeConsensusNode) SubmitTransaction(tx *types.Transaction) error { return nil }

func (f *fakeConsensusNode) GetValidatorSet() map[string]*big.Int {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshot := make(map[string]*big.Int, len(f.validators))
	for addr, power := range f.validators {
		if power != nil {
			snapshot[addr] = new(big.Int).Set(power)
		} else {
			snapshot[addr] = nil
		}
	}
	return snapshot
}

func (f *fakeConsensusNode) GetBlockByHeight(height uint64) (*types.Block, error) { return nil, nil }
func (f *fakeConsensusNode) GetHeight() uint64                                    { return 0 }
func (f *fakeConsensusNode) GetMempool() []*types.Transaction                     { return nil }
func (f *fakeConsensusNode) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	return nil, nil
}
func (f *fakeConsensusNode) CommitBlock(block *types.Block) error { return nil }
func (f *fakeConsensusNode) GetLastCommitHash() []byte            { return nil }
func (f *fakeConsensusNode) QueryState(namespace, key string) (*core.QueryResult, error) {
	return nil, nil
}
func (f *fakeConsensusNode) QueryPrefix(namespace, prefix string) ([]core.QueryRecord, error) {
	return nil, nil
}
func (f *fakeConsensusNode) SimulateTx(txBytes []byte) (*core.SimulationResult, error) {
	return nil, nil
}

func TestServerGetValidatorSetConcurrentMutation(t *testing.T) {
	node := newFakeConsensusNode()
	srv := NewServer(node)

	const iterations = 1000
	ctx := context.Background()
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			node.mu.Lock()
			key := fmt.Sprintf("validator-%d", i%5)
			node.validators[key] = big.NewInt(int64(i))
			if i%3 == 0 {
				delete(node.validators, key)
			}
			node.mu.Unlock()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("panic: %v", r)
			}
		}()
		for i := 0; i < iterations; i++ {
			resp, err := srv.GetValidatorSet(ctx, &consensusv1.GetValidatorSetRequest{})
			if err != nil {
				errCh <- err
				return
			}
			for range resp.GetValidators() {
				// Iterate to mimic downstream processing.
			}
		}
	}()

	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("GetValidatorSet encountered error: %v", err)
	default:
	}
}
