//go:build posreadiness

package posreadiness

import (
	"bytes"
	"context"
	"math/big"
	"testing"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	posv1 "nhbchain/proto/pos"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func TestRealtimeGRPCStream(t *testing.T) {
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

	intentRef := []byte("intent-grpc-1")
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

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dialCancel()

	conn, err := grpc.DialContext(dialCtx, chain.RPCAddr(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial grpc: %v", err)
	}
	defer conn.Close()

	client := posv1.NewRealtimeClient(conn)

	streamCtx, streamCancel := context.WithCancel(context.Background())
	defer streamCancel()

	stream, err := client.SubscribeFinality(streamCtx, &posv1.SubscribeFinalityRequest{})
	if err != nil {
		t.Fatalf("subscribe finality: %v", err)
	}

	if err := node.SubmitTransaction(tx); err != nil {
		t.Fatalf("submit tx: %v", err)
	}

	pending := recvFinalityUpdate(t, stream)
	if pending.GetStatus() != posv1.FinalityStatus_FINALITY_STATUS_PENDING {
		t.Fatalf("expected pending status, got %v", pending.GetStatus())
	}
	if pending.GetCursor() == "" {
		t.Fatalf("expected cursor for pending update")
	}
	if !bytes.Equal(pending.GetIntentRef(), intentRef) {
		t.Fatalf("unexpected pending intent ref: %x", pending.GetIntentRef())
	}
	if !bytes.Equal(pending.GetTxHash(), txHash) {
		t.Fatalf("unexpected pending tx hash: %x", pending.GetTxHash())
	}
	pendingCursor := pending.GetCursor()

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

	finalized := recvFinalityUpdate(t, stream)
	if finalized.GetStatus() != posv1.FinalityStatus_FINALITY_STATUS_FINALIZED {
		t.Fatalf("expected finalized status, got %v", finalized.GetStatus())
	}
	if finalized.GetCursor() == pendingCursor {
		t.Fatalf("expected distinct cursor for finalized update")
	}
	finalCursor := finalized.GetCursor()
	if !bytes.Equal(finalized.GetIntentRef(), intentRef) {
		t.Fatalf("unexpected finalized intent ref: %x", finalized.GetIntentRef())
	}
	if !bytes.Equal(finalized.GetTxHash(), txHash) {
		t.Fatalf("unexpected finalized tx hash: %x", finalized.GetTxHash())
	}
	if !bytes.Equal(finalized.GetBlockHash(), blockHash) {
		t.Fatalf("unexpected block hash: %x", finalized.GetBlockHash())
	}
	if finalized.GetHeight() != block.Header.Height {
		t.Fatalf("unexpected block height: %d", finalized.GetHeight())
	}
	if finalized.GetTimestamp() != block.Header.Timestamp {
		t.Fatalf("unexpected timestamp: %d", finalized.GetTimestamp())
	}

	// Cancel the first stream before resubscribing.
	streamCancel()

	resumeCtx, resumeCancel := context.WithCancel(context.Background())
	defer resumeCancel()

	resume, err := client.SubscribeFinality(resumeCtx, &posv1.SubscribeFinalityRequest{Cursor: pendingCursor})
	if err != nil {
		t.Fatalf("resume subscribe: %v", err)
	}
	replay := recvFinalityUpdate(t, resume)
	if replay.GetCursor() != finalCursor {
		t.Fatalf("expected replay cursor %s, got %s", finalCursor, replay.GetCursor())
	}
	if replay.GetStatus() != posv1.FinalityStatus_FINALITY_STATUS_FINALIZED {
		t.Fatalf("expected replay finalized status, got %v", replay.GetStatus())
	}
	if !bytes.Equal(replay.GetIntentRef(), intentRef) {
		t.Fatalf("unexpected replay intent ref: %x", replay.GetIntentRef())
	}
	if !bytes.Equal(replay.GetTxHash(), txHash) {
		t.Fatalf("unexpected replay tx hash: %x", replay.GetTxHash())
	}
	if !bytes.Equal(replay.GetBlockHash(), blockHash) {
		t.Fatalf("unexpected replay block hash: %x", replay.GetBlockHash())
	}
}

func recvFinalityUpdate(t *testing.T, stream posv1.Realtime_SubscribeFinalityClient) *posv1.FinalityUpdate {
	t.Helper()
	type result struct {
		resp *posv1.SubscribeFinalityResponse
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		resp, err := stream.Recv()
		ch <- result{resp: resp, err: err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("receive finality: %v", res.err)
		}
		if res.resp == nil || res.resp.Update == nil {
			t.Fatalf("missing finality update")
		}
		return res.resp.Update
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for finality update")
		return nil
	}
}
