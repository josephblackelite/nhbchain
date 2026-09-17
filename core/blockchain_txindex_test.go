package core

import (
	"encoding/json"
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

func signedTransferTx(t *testing.T, senderKey *crypto.PrivateKey, recipient [20]byte, nonce uint64) *types.Transaction {
	t.Helper()
	tx := &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    nonce,
		To:       append([]byte(nil), recipient[:]...),
		Value:    big.NewInt(1),
		GasLimit: 21_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(senderKey.PrivateKey); err != nil {
		t.Fatalf("sign tx: %v", err)
	}
	return tx
}

// TestAddBlockIndexesTransactionsByHash confirms a transaction committed via
// the normal AddBlock path is immediately findable through
// FindTransactionHeight -- the fast path rpc.findTransaction now tries
// first, before ever falling back to a block scan.
func TestAddBlockIndexesTransactionsByHash(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	var senderAddr, recipientAddr [20]byte
	copy(senderAddr[:], senderKey.PubKey().Address().Bytes())
	copy(recipientAddr[:], recipientKey.PubKey().Address().Bytes())

	if err := node.WithState(func(m *nhbstate.Manager) error {
		return m.PutAccount(senderAddr[:], &types.Account{BalanceNHB: big.NewInt(1_000_000), BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)})
	}); err != nil {
		t.Fatalf("seed sender: %v", err)
	}

	tx := signedTransferTx(t, senderKey, recipientAddr, 0)
	txHashBytes, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash tx: %v", err)
	}

	block, err := node.CreateBlock([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block: %v", err)
	}

	height, ok, err := node.Chain().FindTransactionHeight(txHashBytes)
	if err != nil {
		t.Fatalf("find transaction height: %v", err)
	}
	if !ok {
		t.Fatalf("expected transaction to be indexed, but FindTransactionHeight reported not found")
	}
	if height != block.Header.Height {
		t.Fatalf("indexed height mismatch: got %d want %d", height, block.Header.Height)
	}
}

// TestBackfillTransactionIndexRecoversPreExistingBlocks is the core proof
// this feature exists for: a chain with real committed blocks whose
// transactions were never indexed (exactly the state every already-running
// validator is in the moment this code first deploys, since AddBlock only
// started indexing going forward) gets fully, correctly indexed by
// BackfillTransactionIndex, and a second call is a true no-op.
//
// AddBlock indexes unconditionally now, so this test can't reach that
// "committed but unindexed" state through the normal public API -- it
// constructs a Blockchain by hand and replicates AddBlock's pre-index
// write sequence (tip/height/heightIndex/hashIndex/lastTimestamp, the
// exact same keys AddBlock still writes today, just without the txhash:
// entries) to stand in for a validator's real on-disk history from before
// this feature existed.
func TestBackfillTransactionIndexRecoversPreExistingBlocks(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	genesisBlock, err := createGenesisBlock(db)
	if err != nil {
		t.Fatalf("create genesis block: %v", err)
	}
	genesisHash, err := genesisBlock.Header.Hash()
	if err != nil {
		t.Fatalf("hash genesis: %v", err)
	}
	if _, err := persistGenesisBlock(db, genesisBlock); err != nil {
		t.Fatalf("persist genesis: %v", err)
	}

	bc := &Blockchain{
		db:      db,
		tip:     cloneBytes(genesisHash),
		height:  0,
		heights: map[uint64][]byte{0: cloneBytes(genesisHash)},
	}

	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sender key: %v", err)
	}
	recipientKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate recipient key: %v", err)
	}
	var recipientAddr [20]byte
	copy(recipientAddr[:], recipientKey.PubKey().Address().Bytes())

	const blockCount = 4
	txHashesByHeight := make(map[uint64][]byte, blockCount)
	prevHash := genesisHash
	for i := uint64(1); i <= blockCount; i++ {
		tx := signedTransferTx(t, senderKey, recipientAddr, i-1)
		txRoot, err := ComputeTxRoot([]*types.Transaction{tx})
		if err != nil {
			t.Fatalf("compute tx root: %v", err)
		}
		header := &types.BlockHeader{
			Height:    i,
			Timestamp: 1700000000 + int64(i),
			PrevHash:  prevHash,
			TxRoot:    txRoot,
			StateRoot: genesisBlock.Header.StateRoot,
		}
		block := types.NewBlock(header, []*types.Transaction{tx})
		blockBytes, err := json.Marshal(block)
		if err != nil {
			t.Fatalf("marshal block: %v", err)
		}
		blockHash, err := header.Hash()
		if err != nil {
			t.Fatalf("hash block: %v", err)
		}
		// Exactly AddBlock's pre-tx-index write sequence -- see
		// blockchain.go's AddBlock -- deliberately NOT calling the real
		// AddBlock, which would also write the txhash: index and defeat
		// the point of this test.
		if err := db.Put(blockHash, blockBytes); err != nil {
			t.Fatalf("store block: %v", err)
		}
		if err := db.Put(tipKey, blockHash); err != nil {
			t.Fatalf("store tip: %v", err)
		}
		if err := db.Put(heightKeyName, encodeUint64(i)); err != nil {
			t.Fatalf("store height: %v", err)
		}
		if err := db.Put(heightKey(i), blockHash); err != nil {
			t.Fatalf("store height index: %v", err)
		}
		if err := db.Put(hashKey(blockHash), encodeUint64(i)); err != nil {
			t.Fatalf("store hash index: %v", err)
		}

		bc.tip = cloneBytes(blockHash)
		bc.height = i
		bc.heights[i] = cloneBytes(blockHash)
		prevHash = blockHash

		txHashBytes, err := tx.Hash()
		if err != nil {
			t.Fatalf("hash tx: %v", err)
		}
		txHashesByHeight[i] = txHashBytes
	}

	// Confirm the setup actually reproduced the "not indexed" state before
	// testing recovery from it.
	for height, txHash := range txHashesByHeight {
		if _, ok, err := bc.FindTransactionHeight(txHash); err != nil {
			t.Fatalf("unexpected error checking pre-backfill state at height %d: %v", height, err)
		} else if ok {
			t.Fatalf("transaction at height %d unexpectedly already indexed before backfill ran", height)
		}
	}

	indexed, err := bc.BackfillTransactionIndex()
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if indexed != blockCount {
		t.Fatalf("expected %d transactions indexed, got %d", blockCount, indexed)
	}

	for height, txHash := range txHashesByHeight {
		gotHeight, ok, err := bc.FindTransactionHeight(txHash)
		if err != nil {
			t.Fatalf("find transaction height at height %d: %v", height, err)
		}
		if !ok {
			t.Fatalf("expected transaction at height %d to be indexed after backfill", height)
		}
		if gotHeight != height {
			t.Fatalf("indexed height mismatch: got %d want %d", gotHeight, height)
		}
	}

	// Idempotency: a second call must be a true no-op (the done marker
	// short-circuits before touching any block), not just "harmless to
	// re-run."
	indexedAgain, err := bc.BackfillTransactionIndex()
	if err != nil {
		t.Fatalf("second backfill call: %v", err)
	}
	if indexedAgain != 0 {
		t.Fatalf("expected second backfill call to be a no-op, but it indexed %d transactions", indexedAgain)
	}
}
