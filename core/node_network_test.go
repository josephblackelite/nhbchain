package core

import (
	"encoding/json"
	"errors"
	"math/big"
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
