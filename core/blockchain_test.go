package core

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"nhbchain/core/types"
	"nhbchain/storage"
)

func newTestBlock(height uint64, prevHash []byte) *types.Block {
	header := &types.BlockHeader{
		Height:    height,
		Timestamp: int64(height),
		PrevHash:  append([]byte(nil), prevHash...),
		TxRoot:    gethtypes.EmptyRootHash.Bytes(),
	}
	return types.NewBlock(header, nil)
}

func TestNewBlockchainRequiresGenesisWhenAutogenesisDisabled(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	if _, err := NewBlockchain(db, "", false); err == nil {
		t.Fatalf("expected error when genesis is required but unavailable")
	}
}

func TestNewBlockchainLoadsGenesisFromFile(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	genesis := types.NewBlock(&types.BlockHeader{Height: 0, Timestamp: 1}, nil)
	data, err := json.Marshal(genesis)
	if err != nil {
		t.Fatalf("marshal genesis: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "genesis.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write genesis file: %v", err)
	}

	bc, err := NewBlockchain(db, path, false)
	if err != nil {
		t.Fatalf("new blockchain with file genesis: %v", err)
	}

	if bc.GetHeight() != 0 {
		t.Fatalf("expected height 0 after loading genesis file, got %d", bc.GetHeight())
	}

	loadedGenesis, err := bc.GetBlockByHeight(0)
	if err != nil {
		t.Fatalf("get genesis block: %v", err)
	}
	if loadedGenesis.Header.Timestamp != genesis.Header.Timestamp {
		t.Fatalf("unexpected genesis header timestamp: got %d want %d", loadedGenesis.Header.Timestamp, genesis.Header.Timestamp)
	}
}

func TestNewBlockchainReturnsErrorForMissingGenesisFile(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	missingPath := filepath.Join(t.TempDir(), "missing.json")
	if _, err := NewBlockchain(db, missingPath, false); err == nil {
		t.Fatalf("expected error when genesis file cannot be read")
	}
}

func TestBlockchainPersistenceAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "db")

	db, err := storage.NewLevelDB(dbPath)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	bc, err := NewBlockchain(db, "", true)
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

	reopenedBC, err := NewBlockchain(reopenedDB, "", true)
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
