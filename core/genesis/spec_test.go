// core/genesis/spec_test.go
package genesis

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	gethtypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/consensus/store"
	"nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func TestLoadGenesisSpecAndBuildGenesis(t *testing.T) {
	addr1 := crypto.MustNewAddress(crypto.NHBPrefix, bytes.Repeat([]byte{0x01}, 20)).String()
	addr2 := crypto.MustNewAddress(crypto.ZNHBPrefix, bytes.Repeat([]byte{0x02}, 20)).String()

	chainID := uint64(42)
	paused := true

	spec := GenesisSpec{
		GenesisTime: "2024-01-01T00:00:00Z",
		NativeTokens: []NativeTokenSpec{
			{
				Symbol:        "NHB",
				Name:          "NHBCoin",
				Decimals:      18,
				MintAuthority: addr1,
			},
			{
				Symbol:            "ZNHB",
				Name:              "ZapNHB",
				Decimals:          18,
				InitialMintPaused: &paused,
			},
		},
		Validators: []ValidatorSpec{
			{
				Address: addr1,
				Power:   10,
				Moniker: "validator-1",
				PubKey:  "aabbcc",
			},
		},
		Alloc: map[string]map[string]string{
			addr1: {
				"NHB":  "1000",
				"ZNHB": "50",
			},
			addr2: {
				"NHB": "2000",
			},
		},
		Roles: map[string][]string{
			"role.nhb": {addr1, addr2},
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

	block, finalize, err := BuildGenesisFromSpec(loaded, db)
	if err != nil {
		t.Fatalf("BuildGenesisFromSpec: %v", err)
	}
	if finalize == nil {
		t.Fatalf("expected finalize callback")
	}
	if err := finalize(); err != nil {
		t.Fatalf("finalize genesis: %v", err)
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
	if bytes.Equal(block.Header.StateRoot, gethtypes.EmptyRootHash.Bytes()) {
		t.Fatalf("expected non-empty state root")
	}
	if !bytes.Equal(block.Header.TxRoot, gethtypes.EmptyRootHash.Bytes()) {
		t.Fatalf("unexpected tx root")
	}

	stateTrie, err := trie.NewTrie(db, block.Header.StateRoot)
	if err != nil {
		t.Fatalf("open state trie: %v", err)
	}
	manager := state.NewManager(stateTrie)

	tokens, err := manager.TokenList()
	if err != nil {
		t.Fatalf("token list: %v", err)
	}
	sort.Strings(tokens)
	expectedTokens := []string{"NHB", "ZNHB"}
	if len(tokens) != len(expectedTokens) {
		t.Fatalf("unexpected token list size: got %d want %d", len(tokens), len(expectedTokens))
	}
	for i, symbol := range expectedTokens {
		if tokens[i] != symbol {
			t.Fatalf("unexpected token[%d]: got %q want %q", i, tokens[i], symbol)
		}
	}

	parsedAddr1, err := ParseBech32Account(addr1)
	if err != nil {
		t.Fatalf("parse addr1: %v", err)
	}
	nhbMeta, err := manager.Token("NHB")
	if err != nil {
		t.Fatalf("load NHB token: %v", err)
	}
	if !bytes.Equal(nhbMeta.MintAuthority, parsedAddr1[:]) {
		t.Fatalf("unexpected mint authority")
	}

	balance1, err := manager.Balance(parsedAddr1[:], "NHB")
	if err != nil {
		t.Fatalf("balance addr1 NHB: %v", err)
	}
	if balance1.String() != "1000" {
		t.Fatalf("unexpected NHB balance for addr1: %s", balance1.String())
	}

	account1, err := manager.GetAccount(parsedAddr1[:])
	if err != nil {
		t.Fatalf("get account1: %v", err)
	}
	if account1.BalanceNHB.String() != "1000" {
		t.Fatalf("unexpected account1 NHB balance: %s", account1.BalanceNHB.String())
	}
	if account1.BalanceZNHB.String() != "50" {
		t.Fatalf("unexpected account1 ZNHB balance: %s", account1.BalanceZNHB.String())
	}

	members, err := manager.RoleMembers("role.nhb")
	if err != nil {
		t.Fatalf("role members: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("unexpected role membership size: %d", len(members))
	}

	validatorSet, err := manager.LoadValidatorSet()
	if err != nil {
		t.Fatalf("load validator set: %v", err)
	}
	if len(validatorSet) != 1 {
		t.Fatalf("unexpected validator set size: %d", len(validatorSet))
	}

	stored, err := db.Get([]byte("consensus/validatorset"))
	if err != nil {
		t.Fatalf("load consensus validators: %v", err)
	}
	var validators []store.Validator
	if err := rlp.DecodeBytes(stored, &validators); err != nil {
		t.Fatalf("decode validators: %v", err)
	}
	if len(validators) != 1 {
		t.Fatalf("unexpected persisted validator count: %d", len(validators))
	}

	block2, finalize2, err := BuildGenesisFromSpec(loaded, db)
	if err != nil {
		t.Fatalf("BuildGenesisFromSpec second call: %v", err)
	}
	if finalize2 == nil {
		t.Fatalf("expected finalize callback on second run")
	}
	if err := finalize2(); err != nil {
		t.Fatalf("finalize second genesis: %v", err)
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
