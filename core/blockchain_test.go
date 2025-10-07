package core

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"nhbchain/core/genesis"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"
	"nhbchain/storage/trie"
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
	} else if !strings.Contains(err.Error(), "genesis is required") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestNewBlockchainLoadsGenesisFromFile(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	addr := crypto.MustNewAddress(crypto.NHBPrefix, bytes.Repeat([]byte{0x01}, 20)).String()
	spec := genesis.GenesisSpec{
		GenesisTime: "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{
			{
				Symbol:   "NHB",
				Name:     "NHBCoin",
				Decimals: 18,
			},
		},
		Validators: []genesis.ValidatorSpec{
			{
				Address: addr,
				Power:   1,
			},
		},
		Alloc: map[string]map[string]string{
			addr: {
				"NHB": "1000",
			},
		},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
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

	expectedTimestamp, err := time.Parse(time.RFC3339, spec.GenesisTime)
	if err != nil {
		t.Fatalf("parse genesisTime: %v", err)
	}
	if loadedGenesis.Header.Timestamp != expectedTimestamp.Unix() {
		t.Fatalf("unexpected genesis header timestamp: got %d want %d", loadedGenesis.Header.Timestamp, expectedTimestamp.Unix())
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
	if last := bc.LastTimestamp(); last != block2.Header.Timestamp {
		t.Fatalf("unexpected last timestamp before restart: got %d want %d", last, block2.Header.Timestamp)
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
	if last := reopenedBC.LastTimestamp(); last != block2.Header.Timestamp {
		t.Fatalf("unexpected last timestamp after restart: got %d want %d", last, block2.Header.Timestamp)
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

func TestTipReturnsCopy(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })

	bc, err := NewBlockchain(db, "", true)
	if err != nil {
		t.Fatalf("failed to create blockchain: %v", err)
	}

	tip := bc.Tip()
	if len(tip) == 0 {
		t.Fatalf("expected non-empty tip hash")
	}

	original := append([]byte(nil), tip...)

	tip[0] ^= 0xFF

	if bytes.Equal(tip, bc.Tip()) {
		t.Fatalf("mutating returned tip should not affect blockchain state")
	}

	if got := bc.Tip(); !bytes.Equal(got, original) {
		t.Fatalf("blockchain tip mutated: got %x want %x", got, original)
	}
}

func TestNewBlockchainChainIDMismatchDoesNotPersistState(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	addr := crypto.MustNewAddress(crypto.NHBPrefix, bytes.Repeat([]byte{0x01}, 20)).String()

	spec := genesis.GenesisSpec{
		GenesisTime: "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{
			{
				Symbol:   "NHB",
				Name:     "NHBCoin",
				Decimals: 18,
			},
		},
		Validators: []genesis.ValidatorSpec{
			{
				Address: addr,
				Power:   1,
			},
		},
		Alloc: map[string]map[string]string{
			addr: {
				"NHB": "1000",
			},
		},
	}

	tempDB := storage.NewMemDB()
	defer tempDB.Close()
	block, finalize, err := genesis.BuildGenesisFromSpec(&spec, tempDB, nil)
	if err != nil {
		t.Fatalf("build genesis for derived chain id: %v", err)
	}
	if finalize == nil {
		t.Fatalf("expected finalize callback")
	}
	hash, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash genesis: %v", err)
	}
	derivedID := binary.BigEndian.Uint64(hash[:8])
	mismatch := derivedID + 1
	spec.ChainID = &mismatch

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "genesis.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write genesis file: %v", err)
	}

	if _, err := NewBlockchain(db, path, false); err == nil {
		t.Fatalf("expected chain id mismatch error")
	} else if !strings.Contains(err.Error(), "chainId mismatch") {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := db.Get([]byte("consensus/validatorset")); err == nil {
		t.Fatalf("consensus validator set should not be persisted on mismatch")
	}

	stateTrie, err := trie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("open state trie: %v", err)
	}
	if stateTrie.Hash() != gethtypes.EmptyRootHash {
		t.Fatalf("state trie should remain empty on mismatch")
	}

}
