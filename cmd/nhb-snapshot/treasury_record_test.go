package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"nhbchain/core"
	"nhbchain/crypto"
)

// A node records its treasury configuration under a key of its own in the chain
// database (core/treasury_config_record.go). The reader here opens only the keys
// it knows, so a database that holds the record must read exactly as the chain
// that core reports.
func TestReaderToleratesTheTreasuryConfigRecord(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "db")
	db := openWritableDB(t, dir, false)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	quietly(func() { _, err = core.NewNode(db, key, "", true, false) })
	if err != nil {
		db.Close()
		t.Fatalf("start node: %v", err)
	}
	// The key is repeated here, as the reader's own keys are.
	if _, err := db.Get([]byte("treasuryconfig:v1")); err != nil {
		db.Close()
		t.Fatalf("the node did not record its treasury configuration: %v", err)
	}
	var bc *core.Blockchain
	quietly(func() { bc, err = core.NewBlockchain(db, "", true) })
	if err != nil {
		db.Close()
		t.Fatalf("reopen chain: %v", err)
	}
	wantTip, wantGenesis, wantChain := bc.Tip(), bc.GenesisHash(), bc.ChainID()
	wantRoot := bc.CurrentHeader().StateRoot
	db.Close()

	id, err := openAndReadIdentity(dir, checkOptions{})
	if err != nil {
		t.Fatalf("read identity: %v", err)
	}
	if !bytes.Equal(id.TipHash, wantTip) || !bytes.Equal(id.GenesisHash, wantGenesis) ||
		id.ChainID != wantChain || !bytes.Equal(id.StateRoot, wantRoot) {
		t.Fatalf("the reader's view of the chain differs from core's: %+v", id)
	}
}
