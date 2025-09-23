package core

import (
	"bytes"
	"path/filepath"
	"testing"

	"nhbchain/core/types"
	"nhbchain/storage"
)

func newTestBlock(height uint64, prevHash []byte) *types.Block {
	header := &types.BlockHeader{
		Height:    height,
		Timestamp: int64(height),
		PrevHash:  append([]byte(nil), prevHash...),
	}
	return types.NewBlock(header, nil)
}

func TestBlockchainPersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db")

	db, err := storage.NewLevelDB(dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	bc, err := NewBlockchain(db)
	if err != nil {
		t.Fatalf("failed to create blockchain: %v", err)
	}

	block1 := newTestBlock(1, bc.Tip())
	block1Hash, err := block1.Header.Hash()
	if err != nil {
		t.Fatalf("failed to hash block1: %v", err)
	}
	if err := bc.AddBlock(block1); err != nil {
		t.Fatalf("failed to add block1: %v", err)
	}

	block2 := newTestBlock(2, bc.Tip())
	block2Hash, err := block2.Header.Hash()
	if err != nil {
		t.Fatalf("failed to hash block2: %v", err)
	}
	if err := bc.AddBlock(block2); err != nil {
		t.Fatalf("failed to add block2: %v", err)
	}

	if bc.GetHeight() != 2 {
		t.Fatalf("expected height 2 before restart, got %d", bc.GetHeight())
	}

	db.Close()

	reopenedDB, err := storage.NewLevelDB(dbPath)
	if err != nil {
		t.Fatalf("failed to reopen db: %v", err)
	}
	defer reopenedDB.Close()

	reopenedBC, err := NewBlockchain(reopenedDB)
	if err != nil {
		t.Fatalf("failed to reopen blockchain: %v", err)
	}

	if reopenedBC.GetHeight() != 2 {
		t.Fatalf("expected height 2 after restart, got %d", reopenedBC.GetHeight())
	}

	reopenedBlock1, err := reopenedBC.GetBlockByHeight(1)
	if err != nil {
		t.Fatalf("failed to get block1 by height: %v", err)
	}
	reopenedBlock1Hash, err := reopenedBlock1.Header.Hash()
	if err != nil {
		t.Fatalf("failed to hash reopened block1: %v", err)
	}
	if !bytes.Equal(reopenedBlock1Hash, block1Hash) {
		t.Fatalf("block1 hash mismatch after restart")
	}

	reopenedBlock2, err := reopenedBC.GetBlockByHeight(2)
	if err != nil {
		t.Fatalf("failed to get block2 by height: %v", err)
	}
	reopenedBlock2Hash, err := reopenedBlock2.Header.Hash()
	if err != nil {
		t.Fatalf("failed to hash reopened block2: %v", err)
	}
	if !bytes.Equal(reopenedBlock2Hash, block2Hash) {
		t.Fatalf("block2 hash mismatch after restart")
	}

	if !bytes.Equal(reopenedBC.Tip(), block2Hash) {
		t.Fatalf("expected tip to match last block after restart")
	}

	genesis, err := reopenedBC.GetBlockByHeight(0)
	if err != nil {
		t.Fatalf("failed to get genesis block after restart: %v", err)
	}
	if len(genesis.Header.PrevHash) != 0 {
		t.Fatalf("expected genesis prev hash to be empty")
	}

}
