package core

import (
	"encoding/json"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// TestTransactionIndexCompleteFollowsTheBackfill: the index is complete only
// once the backfill has covered the blocks that were stored before the index
// existed. Until then a hash the index does not know may still be in a block, and
// a caller must scan to find out; afterwards it may not be, and need not.
func TestTransactionIndexCompleteFollowsTheBackfill(t *testing.T) {
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
		heights: map[uint64][]byte{0: cloneBytes(genesisHash)},
	}
	if bc.TransactionIndexComplete() {
		t.Fatalf("a store the backfill has not run on reported a complete index")
	}

	// One block stored the way blocks were stored before the index existed.
	senderKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	var recipient [20]byte
	recipient[0] = 9
	tx := signedTransferTx(t, senderKey, recipient, 0)
	txRoot, err := ComputeTxRoot([]*types.Transaction{tx})
	if err != nil {
		t.Fatalf("tx root: %v", err)
	}
	header := &types.BlockHeader{Height: 1, Timestamp: 1700000001, PrevHash: genesisHash, TxRoot: txRoot, StateRoot: genesisBlock.Header.StateRoot}
	blockBytes, err := json.Marshal(types.NewBlock(header, []*types.Transaction{tx}))
	if err != nil {
		t.Fatalf("marshal block: %v", err)
	}
	blockHash, err := header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}
	for key, value := range map[string][]byte{
		string(blockHash):          blockBytes,
		string(tipKey):             blockHash,
		string(heightKeyName):      encodeUint64(1),
		string(heightKey(1)):       blockHash,
		string(hashKey(blockHash)): encodeUint64(1),
	} {
		if err := db.Put([]byte(key), value); err != nil {
			t.Fatalf("store: %v", err)
		}
	}
	bc.tip, bc.height, bc.heights[1] = cloneBytes(blockHash), 1, cloneBytes(blockHash)

	txHash, err := tx.Hash()
	if err != nil {
		t.Fatalf("hash tx: %v", err)
	}
	if _, ok, _ := bc.FindTransactionHeight(txHash); ok {
		t.Fatalf("the unindexed transaction is already indexed")
	}
	if bc.TransactionIndexComplete() {
		t.Fatalf("the index reported complete while a stored block is not in it")
	}

	if _, err := bc.BackfillTransactionIndex(); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !bc.TransactionIndexComplete() {
		t.Fatalf("the index is not complete after the backfill")
	}
	if height, ok, err := bc.FindTransactionHeight(txHash); err != nil || !ok || height != 1 {
		t.Fatalf("the backfilled transaction: height %d found %v err %v", height, ok, err)
	}

	// It stays complete across a restart, which builds a new Blockchain over the same store.
	restarted, err := NewBlockchain(db, "", true)
	if err != nil {
		t.Fatalf("reopen the chain: %v", err)
	}
	if !restarted.TransactionIndexComplete() {
		t.Fatalf("the completeness marker did not survive the restart")
	}
}

// TestFreshChainIndexIsCompleteFromTheStart: a chain that never had blocks
// without the index has nothing to backfill; the marker is set the first time the
// node starts and every block after that is indexed as it is added.
func TestFreshChainIndexIsCompleteFromTheStart(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	bc, err := NewBlockchain(db, "", true)
	if err != nil {
		t.Fatalf("new chain: %v", err)
	}
	if bc.TransactionIndexComplete() {
		t.Fatalf("no marker should exist before the node has started once")
	}
	if _, err := bc.BackfillTransactionIndex(); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if !bc.TransactionIndexComplete() {
		t.Fatalf("a fresh chain's index should be complete after the start-up backfill")
	}
}
