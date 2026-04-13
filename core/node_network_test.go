package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sync"
	"testing"

	"nhbchain/crypto"
	"nhbchain/p2p"
)

func TestProcessNetworkMessageRejectsInvalidTransaction(t *testing.T) {
	node := newTestNode(t)

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	invalidChainID := big.NewInt(0xdead)
	tx := prepareSignedTransaction(t, node, senderKey, 0, invalidChainID)
	payload, err := json.Marshal(tx)
	if err != nil {
		t.Fatalf("marshal transaction: %v", err)
	}

	msg := &p2p.Message{Type: p2p.MsgTypeTx, Payload: payload}
	err = node.ProcessNetworkMessage(msg)
	if err == nil {
		t.Fatalf("expected invalid payload error")
	}
	if !errors.Is(err, p2p.ErrInvalidPayload) {
		t.Fatalf("expected ErrInvalidPayload, got %v", err)
	}

	if mempool := node.GetMempool(); len(mempool) != 0 {
		t.Fatalf("expected empty mempool, got %d", len(mempool))
	}

	if _, err := node.CreateBlock(nil); err != nil {
		t.Fatalf("create block after invalid tx: %v", err)
	}
}

func TestGetAccountConcurrentWithCommit(t *testing.T) {
	node := newTestNode(t)

	address := node.validatorKey.PubKey().Address().Bytes()
	const commitRounds = 5

	errCh := make(chan error, 2)
	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for i := 0; i < commitRounds; i++ {
			block, err := node.CreateBlock(nil)
			if err != nil {
				errCh <- fmt.Errorf("create block: %w", err)
				return
			}
			if err := node.CommitBlock(block); err != nil {
				errCh <- fmt.Errorf("commit block: %w", err)
				return
			}
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := node.GetAccount(address); err != nil {
				errCh <- fmt.Errorf("get account: %w", err)
				return
			}
		}
	}()

	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent access failed: %v", err)
		}
	}
}
