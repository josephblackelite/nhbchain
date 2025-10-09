package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nhbchain/core/genesis"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/storage"

	gethtypes "github.com/ethereum/go-ethereum/core/types"
)

func writeTestGenesis(t *testing.T, dir string) (genesis.GenesisSpec, string) {
	t.Helper()

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	addr := key.PubKey().Address().String()

	spec := genesis.GenesisSpec{
		GenesisTime: "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{
			{
				Symbol:   "NHB",
				Name:     "NHBCoin",
				Decimals: 18,
			},
			{
				Symbol:   "ZNHB",
				Name:     "zNHBCoin",
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
				"NHB":  "1000",
				"ZNHB": "1000",
			},
		},
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis spec: %v", err)
	}

	path := filepath.Join(dir, "genesis.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write genesis file: %v", err)
	}
	return spec, path
}

func TestNewNodeWithLevelDBGenesisReload(t *testing.T) {
	tmpDir := t.TempDir()
	_, genesisPath := writeTestGenesis(t, tmpDir)
	dbPath := filepath.Join(tmpDir, "db")

	db, err := storage.NewLevelDB(dbPath)
	if err != nil {
		t.Fatalf("create leveldb: %v", err)
	}

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	if _, err := NewNode(db, key, genesisPath, false, false); err != nil {
		t.Fatalf("create node: %v", err)
	}

	db.Close()

	db2, err := storage.NewLevelDB(dbPath)
	if err != nil {
		t.Fatalf("reopen leveldb: %v", err)
	}
	defer db2.Close()

	key2, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate second key: %v", err)
	}

	if _, err := NewNode(db2, key2, genesisPath, false, false); err != nil {
		t.Fatalf("create node after restart: %v", err)
	}
}

func TestNewNodeRebuildsMissingGenesisState(t *testing.T) {
	tmpDir := t.TempDir()
	spec, genesisPath := writeTestGenesis(t, tmpDir)
	dbPath := filepath.Join(tmpDir, "db")

	db, err := storage.NewLevelDB(dbPath)
	if err != nil {
		t.Fatalf("create leveldb: %v", err)
	}

	block, _, err := genesis.BuildGenesisFromSpec(&spec, db, nil)
	if err != nil {
		t.Fatalf("build genesis from spec: %v", err)
	}
	if _, err := persistGenesisBlock(db, block); err != nil {
		t.Fatalf("persist genesis block: %v", err)
	}
	db.Close()

	reopened, err := storage.NewLevelDB(dbPath)
	if err != nil {
		t.Fatalf("reopen leveldb: %v", err)
	}
	defer reopened.Close()

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	if _, err := NewNode(reopened, key, genesisPath, false, false); err != nil {
		t.Fatalf("create node after rebuilding genesis state: %v", err)
	}
}

func Test_StateVersion_RefusesOldSchemaWithoutFlag(t *testing.T) {
	db := storage.NewMemDB()
	defer db.Close()

	header := &types.BlockHeader{
		Height:    0,
		Timestamp: time.Now().UTC().Unix(),
		PrevHash:  []byte{},
		StateRoot: gethtypes.EmptyRootHash.Bytes(),
		TxRoot:    gethtypes.EmptyRootHash.Bytes(),
	}
	block := types.NewBlock(header, nil)
	if _, err := persistGenesisBlock(db, block); err != nil {
		t.Fatalf("persist genesis block: %v", err)
	}

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	if _, err := NewNode(db, key, "", false, false); err == nil {
		t.Fatalf("expected state version guard to reject mismatched schema")
	} else if !strings.Contains(err.Error(), "schema version mismatch") {
		t.Fatalf("expected schema version mismatch error, got %v", err)
	}

	if _, err := NewNode(db, key, "", false, true); err != nil {
		t.Fatalf("allow-migrate should bypass state version guard: %v", err)
	}
}
