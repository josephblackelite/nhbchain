package rpc

// The digest ring keeps what a snapshot rebuild takes from each block, so that
// the next rebuild does not read it again. Consensus lets a block carry hundreds
// of transactions, so what the ring keeps has to be bounded per block as well as
// in blocks, and a block that is too full to keep must still give the same
// snapshot.

import (
	"bytes"
	"context"
	"math/big"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/core/types"
)

func TestDigestRingDoesNotKeepFullBlocks(t *testing.T) {
	node, _, _, _, key := newRandNode(t, false)
	chain := node.Chain()
	sender := costSeededKey(151, 1)
	recipient := costAddr(costSeededKey(151, 2))

	prev := chain.Tip()
	stateRoot := append([]byte(nil), chain.CurrentHeader().StateRoot...)
	base := time.Now().Unix() - 100
	nonce := uint64(0)
	addBlock := func(height uint64, count int) {
		t.Helper()
		txs := make([]*types.Transaction, 0, count)
		for i := 0; i < count; i++ {
			nonce++
			txs = append(txs, costSignedTx(t, sender, types.TxTypeTransfer, nonce, recipient, big.NewInt(int64(nonce)), nil))
		}
		root, err := core.ComputeTxRoot(txs)
		if err != nil {
			t.Fatalf("tx root: %v", err)
		}
		header := &types.BlockHeader{
			Height:             height,
			Timestamp:          base + int64(height)*2,
			PrevHash:           prev,
			StateRoot:          stateRoot,
			TxRoot:             root,
			ExecutionGraphRoot: bytes.Repeat([]byte{0xE1}, 32),
			Validator:          costAddr(key),
		}
		hash, err := header.Hash()
		if err != nil {
			t.Fatalf("hash header: %v", err)
		}
		if err := chain.AddBlock(&types.Block{Header: header, Transactions: txs}); err != nil {
			t.Fatalf("add block %d: %v", height, err)
		}
		prev = hash
	}
	restore := silenceStdout(t)
	addBlock(1, 3)
	addBlock(2, snapshotDigestMaxTxs+1) // too full to keep
	addBlock(3, snapshotDigestMaxTxs)   // as full as one may be and still be kept
	addBlock(4, 2)                      // the tip
	restore()

	srv := quietServer(t, node, costServerConfig())
	settleActivityIndex(srv)
	want, err := srv.legacyBuildExplorerSnapshot(explorerDefaultRecentBlocks)
	if err != nil {
		t.Fatalf("reference snapshot: %v", err)
	}
	for pass := 0; pass < 2; pass++ {
		got, err := srv.buildExplorerSnapshot(context.Background(), explorerDefaultRecentBlocks)
		if err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
		if a, b := mustJSON(t, normalizeSnapshot(want)), mustJSON(t, normalizeSnapshot(got)); a != b {
			t.Fatalf("pass %d: the snapshot differs from the reference\nwant %s\n got %s", pass, a, b)
		}
	}
	if srv.digests.get(1) == nil || srv.digests.get(3) == nil {
		t.Fatalf("blocks with at most %d records below the tip were not kept", snapshotDigestMaxTxs)
	}
	if srv.digests.get(2) != nil {
		t.Fatalf("a block with %d records was kept; the ring must not pin blocks that full", snapshotDigestMaxTxs+1)
	}
	if srv.digests.get(4) != nil {
		t.Fatalf("the tip block was kept")
	}
}
