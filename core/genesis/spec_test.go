package genesis

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	gethtypes "github.com/ethereum/go-ethereum/core/types"

	"nhbchain/crypto"
	"nhbchain/storage"
)

func TestLoadGenesisSpecAndBuildGenesis(t *testing.T) {
	addr1 := crypto.NewAddress(crypto.NHBPrefix, bytes.Repeat([]byte{0x01}, 20)).String()
	addr2 := crypto.NewAddress(crypto.ZNHBPrefix, bytes.Repeat([]byte{0x02}, 20)).String()

	chainID := uint64(42)
	spec := GenesisSpec{
		GenesisTime: "2024-01-01T00:00:00Z",
		NativeTokens: []NativeTokenSpec{
			{
				Symbol:        "NHB",
				Name:          "NHBCoin",
				Decimals:      18,
				MintAuthority: addr1,
			},
		},
		Validators: []ValidatorSpec{
			{
				Address: addr1,
				Power:   10,
				Moniker: "validator-1",
			},
		},
		Alloc: map[string]map[string]string{
			addr1: {
				"NHB": "1000",
			},
			addr2: {
				"NHB": "2000",
			},
		},
		Roles: map[string][]string{
			"role.nhb": []string{addr1, addr2},
		},
		ChainID: &chainID,
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "genesis.json")
	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write spec: %v", err)
	}

	loaded, err := LoadGenesisSpec(path)
	if err != nil {
		t.Fatalf("LoadGenesisSpec: %v", err)
	}

	if loaded.GenesisTime != spec.GenesisTime {
		t.Fatalf("genesisTime mismatch: got %q want %q", loaded.GenesisTime, spec.GenesisTime)
	}
	if len(loaded.NativeTokens) != len(spec.NativeTokens) {
		t.Fatalf("unexpected token count: got %d want %d", len(loaded.NativeTokens), len(spec.NativeTokens))
	}
	if len(loaded.Validators) != len(spec.Validators) {
		t.Fatalf("unexpected validator count: got %d want %d", len(loaded.Validators), len(spec.Validators))
	}

	if id, ok := loaded.ChainIDValue(); !ok || id != chainID {
		t.Fatalf("unexpected chain id: got %d (ok=%t) want %d", id, ok, chainID)
	}

	expectedTimestamp, err := time.Parse(time.RFC3339, spec.GenesisTime)
	if err != nil {
		t.Fatalf("parse genesisTime: %v", err)
	}
	if !loaded.GenesisTimestamp().Equal(expectedTimestamp) {
		t.Fatalf("genesis timestamp mismatch: got %v want %v", loaded.GenesisTimestamp(), expectedTimestamp)
	}

	db := storage.NewMemDB()
	defer db.Close()

	block, err := BuildGenesisFromSpec(loaded, db)
	if err != nil {
		t.Fatalf("BuildGenesisFromSpec: %v", err)
	}

	if block.Header.Height != 0 {
		t.Fatalf("expected height 0, got %d", block.Header.Height)
	}
	if block.Header.Timestamp != expectedTimestamp.Unix() {
		t.Fatalf("unexpected timestamp: got %d want %d", block.Header.Timestamp, expectedTimestamp.Unix())
	}
	if len(block.Header.PrevHash) != 0 {
		t.Fatalf("expected prev hash to be empty")
	}
	if !bytes.Equal(block.Header.StateRoot, gethtypes.EmptyRootHash.Bytes()) {
		t.Fatalf("unexpected state root")
	}
	if !bytes.Equal(block.Header.TxRoot, gethtypes.EmptyRootHash.Bytes()) {
		t.Fatalf("unexpected tx root")
	}

	block2, err := BuildGenesisFromSpec(loaded, db)
	if err != nil {
		t.Fatalf("BuildGenesisFromSpec second call: %v", err)
	}
	hash1, err := block.Header.Hash()
	if err != nil {
		t.Fatalf("hash block: %v", err)
	}
	hash2, err := block2.Header.Hash()
	if err != nil {
		t.Fatalf("hash block2: %v", err)
	}
	if !bytes.Equal(hash1, hash2) {
		t.Fatalf("expected deterministic genesis hash")
	}
}
