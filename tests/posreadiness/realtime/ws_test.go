//go:build posreadiness

package posreadiness

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"

	"nhooyr.io/websocket"
)

type wsFinalityPayload struct {
	Type      string `json:"type"`
	Cursor    string `json:"cursor"`
	IntentRef string `json:"intentRef"`
	TxHash    string `json:"txHash"`
	Status    string `json:"status"`
	Block     string `json:"block,omitempty"`
	Height    uint64 `json:"height,omitempty"`
	Timestamp int64  `json:"ts"`
}

func TestRealtimeWebsocketStream(t *testing.T) {
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

	intentRef := []byte("intent-websocket-1")
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addr := fmt.Sprintf("ws://%s/ws/pos/finality", chain.RPCAddr())
	conn, _, err := websocket.Dial(ctx, addr, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if conn != nil {
			_ = conn.Close(websocket.StatusNormalClosure, "test complete")
		}
	}()

	if err := node.SubmitTransaction(tx); err != nil {
		t.Fatalf("submit tx: %v", err)
	}

	pending := readWSFinality(t, conn)
	if pending.Status != "pending" {
		t.Fatalf("expected pending status, got %q", pending.Status)
	}
	expectedIntent := encodeHex(intentRef)
	if pending.IntentRef != expectedIntent {
		t.Fatalf("unexpected intent ref: %q", pending.IntentRef)
	}
	if pending.TxHash != encodeHex(txHash) {
		t.Fatalf("unexpected tx hash: %q", pending.TxHash)
	}
	if pending.Cursor == "" {
		t.Fatalf("expected cursor for pending update")
	}
	pendingCursor := pending.Cursor

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

	finalized := readWSFinality(t, conn)
	if finalized.Status != "finalized" {
		t.Fatalf("expected finalized status, got %q", finalized.Status)
	}
	if finalized.Cursor == pendingCursor {
		t.Fatalf("expected new cursor for finalized update")
	}
	finalCursor := finalized.Cursor
	if finalized.IntentRef != expectedIntent {
		t.Fatalf("unexpected finalized intent: %q", finalized.IntentRef)
	}
	if finalized.TxHash != encodeHex(txHash) {
		t.Fatalf("unexpected finalized tx hash: %q", finalized.TxHash)
	}
	if finalized.Block != encodeHex(blockHash) {
		t.Fatalf("unexpected block hash: %q", finalized.Block)
	}
	if finalized.Height != block.Header.Height {
		t.Fatalf("unexpected block height: %d", finalized.Height)
	}
	if finalized.Timestamp != block.Header.Timestamp {
		t.Fatalf("unexpected timestamp: %d", finalized.Timestamp)
	}

	// Reconnect using the pending cursor and ensure the finalized event is replayed.
	if err := conn.Close(websocket.StatusNormalClosure, "reconnect"); err != nil {
		t.Fatalf("close websocket: %v", err)
	}
	conn = nil

	resumeCtx, resumeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer resumeCancel()

	resumeAddr := fmt.Sprintf("ws://%s/ws/pos/finality?cursor=%s", chain.RPCAddr(), pendingCursor)
	resumeConn, _, err := websocket.Dial(resumeCtx, resumeAddr, nil)
	if err != nil {
		t.Fatalf("dial resume websocket: %v", err)
	}
	defer resumeConn.Close(websocket.StatusNormalClosure, "test complete")

	replay := readWSFinality(t, resumeConn)
	if replay.Cursor != finalCursor {
		t.Fatalf("expected replay cursor %s, got %s", finalCursor, replay.Cursor)
	}
	if replay.Status != "finalized" {
		t.Fatalf("expected replay finalized status, got %q", replay.Status)
	}
	if replay.IntentRef != expectedIntent {
		t.Fatalf("unexpected replay intent ref: %q", replay.IntentRef)
	}
	if replay.TxHash != encodeHex(txHash) {
		t.Fatalf("unexpected replay tx hash: %q", replay.TxHash)
	}
}

func readWSFinality(t *testing.T, conn *websocket.Conn) wsFinalityPayload {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msgType, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read websocket: %v", err)
	}
	if msgType != websocket.MessageText {
		t.Fatalf("unexpected message type: %v", msgType)
	}
	var payload wsFinalityPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload
}

func encodeHex(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return fmt.Sprintf("0x%x", data)
}
