package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nhbchain/core/genesis"
	"nhbchain/crypto"
	"nhbchain/storage"
)

func treasuryTestAddr(b byte) [20]byte {
	var out [20]byte
	copy(out[:], bytes.Repeat([]byte{b}, 20))
	return out
}

func treasuryTestAddrString(b byte) string {
	return treasuryAddressString(treasuryTestAddr(b))
}

// treasuryTestSpec is a minimal genesis spec. Its admin wallet and buyback
// signers are the only things the tests vary between restarts: neither is part
// of the genesis hash, so every variant describes the same chain.
func treasuryTestSpec(t *testing.T) genesis.GenesisSpec {
	t.Helper()
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	validator := validatorKey.PubKey().Address().String()
	return genesis.GenesisSpec{
		GenesisTime: "2024-01-01T00:00:00Z",
		NativeTokens: []genesis.NativeTokenSpec{
			{Symbol: "NHB", Name: "NHBCoin", Decimals: 18},
			{Symbol: "ZNHB", Name: "zNHBCoin", Decimals: 18},
		},
		Validators: []genesis.ValidatorSpec{{Address: validator, Power: 1}},
		Alloc: map[string]map[string]string{
			validator: {"NHB": "1000", "ZNHB": "1000"},
		},
		AdminWallet:            treasuryTestAddrString(0x01),
		BuybackSigners:         []string{treasuryTestAddrString(0x11), treasuryTestAddrString(0x12), treasuryTestAddrString(0x13)},
		BuybackSignerThreshold: 2,
	}
}

func writeTreasuryTestSpec(t *testing.T, spec genesis.GenesisSpec) string {
	t.Helper()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal genesis spec: %v", err)
	}
	path := filepath.Join(t.TempDir(), "genesis.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write genesis file: %v", err)
	}
	return path
}

func startTreasuryTestNode(t *testing.T, db storage.Database, genesisPath string) (*Node, error) {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate node key: %v", err)
	}
	return NewNode(db, key, genesisPath, false, false)
}

func TestTreasuryConfigEncodingIgnoresSignerOrder(t *testing.T) {
	a, b, c := treasuryTestAddr(0x11), treasuryTestAddr(0x12), treasuryTestAddr(0x13)
	admin := treasuryTestAddr(0x01)

	one := newTreasuryConfig(admin, true, [][20]byte{a, b, c}, 2, true)
	two := newTreasuryConfig(admin, true, [][20]byte{c, a, b}, 2, true)
	if !bytes.Equal(one.encode(), two.encode()) || one.digest() != two.digest() {
		t.Fatalf("the same signer set in another order must give the same record")
	}
	if len(one.differences(two)) != 0 {
		t.Fatalf("the same signer set in another order must not differ: %v", one.differences(two))
	}

	decoded, err := decodeTreasuryConfig(one.encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !bytes.Equal(decoded.encode(), one.encode()) {
		t.Fatalf("a record must decode to the configuration that was encoded")
	}

	// A configuration with nothing set is still a record, and differs from one
	// with an admin wallet.
	none := newTreasuryConfig([20]byte{}, false, nil, 0, false)
	if _, err := decodeTreasuryConfig(none.encode()); err != nil {
		t.Fatalf("decode empty configuration: %v", err)
	}
	if none.digest() == one.digest() {
		t.Fatalf("different configurations must have different digests")
	}
	// The address of an unset admin wallet is not part of the record.
	if got := newTreasuryConfig(admin, false, nil, 0, false); !bytes.Equal(got.encode(), none.encode()) {
		t.Fatalf("an unset admin wallet must not carry an address")
	}
}

func TestTreasuryConfigDecodeRejectsMalformedRecords(t *testing.T) {
	good := newTreasuryConfig(treasuryTestAddr(0x01), true,
		[][20]byte{treasuryTestAddr(0x11), treasuryTestAddr(0x12)}, 2, true).encode()

	mutate := func(f func(b []byte) []byte) []byte {
		return f(append([]byte(nil), good...))
	}
	cases := map[string][]byte{
		"empty":         nil,
		"truncated":     good[:len(good)-1],
		"trailing byte": append(append([]byte(nil), good...), 0),
		"wrong version": mutate(func(b []byte) []byte { b[0] = 9; return b }),
		"unknown flags": mutate(func(b []byte) []byte { b[1] |= 0x80; return b }),
		"signers out of order": mutate(func(b []byte) []byte {
			first := append([]byte(nil), b[30:50]...)
			copy(b[30:50], b[50:70])
			copy(b[50:70], first)
			return b
		}),
		"admin address with the flag clear": mutate(func(b []byte) []byte { b[1] &^= 1; return b }),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeTreasuryConfig(raw); err == nil {
				t.Fatalf("expected the record to be rejected")
			}
		})
	}
}

func TestTreasuryConfigDifferencesNameWhatChanged(t *testing.T) {
	signers := [][20]byte{treasuryTestAddr(0x11), treasuryTestAddr(0x12), treasuryTestAddr(0x13)}
	base := newTreasuryConfig(treasuryTestAddr(0x01), true, signers, 2, true)

	cases := []struct {
		name string
		cfg  treasuryConfig
		want string
	}{
		{"admin changed", newTreasuryConfig(treasuryTestAddr(0x02), true, signers, 2, true), "admin wallet was"},
		{"admin removed", newTreasuryConfig([20]byte{}, false, signers, 2, true), "admin wallet was " + treasuryTestAddrString(0x01) + " and is now unset"},
		{"signer replaced", newTreasuryConfig(treasuryTestAddr(0x01), true, [][20]byte{signers[0], signers[1], treasuryTestAddr(0x14)}, 2, true), "signer set differs"},
		{"signer dropped", newTreasuryConfig(treasuryTestAddr(0x01), true, signers[:2], 2, true), "signer set differs"},
		{"threshold changed", newTreasuryConfig(treasuryTestAddr(0x01), true, signers, 3, true), "threshold was 2 and is now 3"},
		{"signers removed", newTreasuryConfig(treasuryTestAddr(0x01), true, nil, 0, false), "are now unset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := strings.Join(base.differences(tc.cfg), "; ")
			if !strings.Contains(got, tc.want) {
				t.Fatalf("differences = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// A database is started with one treasury configuration; a later start with a
// different genesis file or environment is refused, the same configuration
// goes through, and the stored chain is untouched either way.
func TestNewNodeRefusesTreasuryConfigDifferentFromTheRecord(t *testing.T) {
	signers := func(b ...byte) []string {
		out := make([]string, len(b))
		for i := range b {
			out[i] = treasuryTestAddrString(b[i])
		}
		return out
	}

	type variant struct {
		name string
		// edit changes the spec the second start is given. A nil edit leaves it
		// unchanged. path, when set, replaces the genesis path altogether.
		edit    func(s *genesis.GenesisSpec)
		path    func(t *testing.T) string
		env     map[string]string
		wantErr string // substring of the error; empty means the start must succeed
	}
	variants := []variant{
		{name: "same file"},
		{name: "same signers in another order", edit: func(s *genesis.GenesisSpec) { s.BuybackSigners = signers(0x13, 0x11, 0x12) }},
		{name: "admin wallet changed", edit: func(s *genesis.GenesisSpec) { s.AdminWallet = treasuryTestAddrString(0x02) }, wantErr: "admin wallet was"},
		{name: "admin wallet removed", edit: func(s *genesis.GenesisSpec) { s.AdminWallet = "" }, wantErr: "admin wallet was " + treasuryTestAddrString(0x01) + " and is now unset"},
		{name: "signer replaced", edit: func(s *genesis.GenesisSpec) { s.BuybackSigners = signers(0x11, 0x12, 0x14) }, wantErr: "signer set differs"},
		{name: "signer dropped", edit: func(s *genesis.GenesisSpec) { s.BuybackSigners = signers(0x11, 0x12) }, wantErr: "signer set differs"},
		{name: "threshold changed", edit: func(s *genesis.GenesisSpec) { s.BuybackSignerThreshold = 3 }, wantErr: "threshold was 2 and is now 3"},
		{name: "signers removed", edit: func(s *genesis.GenesisSpec) { s.BuybackSigners, s.BuybackSignerThreshold = nil, 0 }, wantErr: "are now unset"},
		{
			name: "file cannot be parsed",
			path: func(t *testing.T) string {
				p := filepath.Join(t.TempDir(), "broken.json")
				if err := os.WriteFile(p, []byte("{ not json"), 0o644); err != nil {
					t.Fatal(err)
				}
				return p
			},
			wantErr: "is now unset",
		},
		{name: "file is missing", path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.json") }, wantErr: "is now unset"},
		{name: "no genesis path", path: func(t *testing.T) string { return "" }, wantErr: "is now unset"},
		{name: "treasury override to another address", env: map[string]string{"NHB_MASTER_TREASURY": treasuryTestAddrString(0x02)}, wantErr: "admin wallet was"},
		{name: "treasury override equal to the genesis admin", env: map[string]string{"NHB_MASTER_TREASURY": treasuryTestAddrString(0x01)}},
		{name: "treasury override that does not parse", env: map[string]string{"NHB_MASTER_TREASURY": "not-an-address"}},
	}

	for _, tc := range variants {
		t.Run(tc.name, func(t *testing.T) {
			spec := treasuryTestSpec(t)
			firstPath := writeTreasuryTestSpec(t, spec)
			db := storage.NewMemDB()
			t.Cleanup(db.Close)

			first, err := startTreasuryTestNode(t, db, firstPath)
			if err != nil {
				t.Fatalf("first start: %v", err)
			}
			firstTip := first.chain.CurrentHeader().StateRoot

			secondPath := firstPath
			if tc.edit != nil {
				edited := spec
				tc.edit(&edited)
				secondPath = writeTreasuryTestSpec(t, edited)
			}
			if tc.path != nil {
				secondPath = tc.path(t)
			}
			for k, v := range tc.env {
				t.Setenv(k, v)
			}

			_, err = startTreasuryTestNode(t, db, secondPath)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("restart must succeed: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("restart with a different treasury configuration must be refused")
				}
				if !errors.Is(err, ErrTreasuryConfigMismatch) {
					t.Fatalf("error %q does not wrap ErrTreasuryConfigMismatch", err)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tc.wantErr)
				}
				if !strings.Contains(err.Error(), treasuryConfigRerecordEnv) {
					t.Fatalf("error %q does not say how to record a deliberate change", err)
				}
			}

			// Whatever happened, the stored chain is the one the first start made.
			after, err := NewBlockchain(db, "", false)
			if err != nil {
				t.Fatalf("reload chain: %v", err)
			}
			if !bytes.Equal(after.CurrentHeader().StateRoot, firstTip) {
				t.Fatalf("stored state root changed")
			}
		})
	}
}

// A database that has no record (made before the record existed, or by a tool
// that only opens the chain) takes the first configuration it is started with
// and holds every later start to it.
func TestNewNodeRecordsTreasuryConfigOnceForADatabaseWithoutARecord(t *testing.T) {
	spec := treasuryTestSpec(t)
	path := writeTreasuryTestSpec(t, spec)
	db := storage.NewMemDB()
	t.Cleanup(db.Close)

	// Creates the chain the way a tool that does not start a node does.
	if _, err := NewBlockchain(db, path, false); err != nil {
		t.Fatalf("create chain: %v", err)
	}
	if _, err := db.Get(treasuryConfigKey); err == nil {
		t.Fatalf("opening the chain alone must not write a treasury record")
	}

	if _, err := startTreasuryTestNode(t, db, path); err != nil {
		t.Fatalf("first node start on a database without a record: %v", err)
	}
	recorded, err := db.Get(treasuryConfigKey)
	if err != nil {
		t.Fatalf("the first node start must record the configuration: %v", err)
	}

	other := spec
	other.AdminWallet = treasuryTestAddrString(0x02)
	if _, err := startTreasuryTestNode(t, db, writeTreasuryTestSpec(t, other)); !errors.Is(err, ErrTreasuryConfigMismatch) {
		t.Fatalf("a later start with another admin wallet: got %v, want a mismatch", err)
	}
	if got, _ := db.Get(treasuryConfigKey); !bytes.Equal(got, recorded) {
		t.Fatalf("a refused start must leave the record as it was")
	}
	if _, err := startTreasuryTestNode(t, db, path); err != nil {
		t.Fatalf("the recorded configuration must still start: %v", err)
	}
}

// What a node runs with, an environment override included, is what is
// recorded, so a node first started with an override is held to it too.
func TestNewNodeRecordsTheEffectiveTreasury(t *testing.T) {
	spec := treasuryTestSpec(t)
	spec.AdminWallet = ""
	path := writeTreasuryTestSpec(t, spec)
	db := storage.NewMemDB()
	t.Cleanup(db.Close)

	t.Setenv("NHB_MASTER_TREASURY", treasuryTestAddrString(0x02))
	if _, err := startTreasuryTestNode(t, db, path); err != nil {
		t.Fatalf("first start with an override: %v", err)
	}
	if _, err := startTreasuryTestNode(t, db, path); err != nil {
		t.Fatalf("restart with the same override: %v", err)
	}

	t.Setenv("NHB_MASTER_TREASURY", "")
	if _, err := startTreasuryTestNode(t, db, path); !errors.Is(err, ErrTreasuryConfigMismatch) {
		t.Fatalf("restart without the override: got %v, want a mismatch", err)
	}
}

// A deliberate change can be recorded, once, with the environment switch, and
// is then enforced like any other record.
func TestNewNodeReplacesTheRecordOnlyWhenAsked(t *testing.T) {
	spec := treasuryTestSpec(t)
	oldPath := writeTreasuryTestSpec(t, spec)
	changed := spec
	changed.AdminWallet = treasuryTestAddrString(0x02)
	newPath := writeTreasuryTestSpec(t, changed)

	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	if _, err := startTreasuryTestNode(t, db, oldPath); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if _, err := startTreasuryTestNode(t, db, newPath); !errors.Is(err, ErrTreasuryConfigMismatch) {
		t.Fatalf("changed configuration without the switch: got %v, want a mismatch", err)
	}

	t.Setenv(treasuryConfigRerecordEnv, "1")
	if _, err := startTreasuryTestNode(t, db, newPath); err != nil {
		t.Fatalf("changed configuration with the switch: %v", err)
	}
	t.Setenv(treasuryConfigRerecordEnv, "")

	if _, err := startTreasuryTestNode(t, db, newPath); err != nil {
		t.Fatalf("the replaced record must accept the new configuration: %v", err)
	}
	if _, err := startTreasuryTestNode(t, db, oldPath); !errors.Is(err, ErrTreasuryConfigMismatch) {
		t.Fatalf("the replaced record must refuse the old configuration: got %v", err)
	}
}

func TestNewNodeReplacesAnUnreadableRecordOnlyWhenAsked(t *testing.T) {
	path := writeTreasuryTestSpec(t, treasuryTestSpec(t))
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	if _, err := startTreasuryTestNode(t, db, path); err != nil {
		t.Fatalf("first start: %v", err)
	}
	if err := db.Put(treasuryConfigKey, []byte("garbage")); err != nil {
		t.Fatalf("damage the record: %v", err)
	}
	if _, err := startTreasuryTestNode(t, db, path); !errors.Is(err, ErrTreasuryConfigMismatch) {
		t.Fatalf("an unreadable record: got %v, want a refusal", err)
	}
	t.Setenv(treasuryConfigRerecordEnv, "true")
	if _, err := startTreasuryTestNode(t, db, path); err != nil {
		t.Fatalf("an unreadable record with the switch: %v", err)
	}
	t.Setenv(treasuryConfigRerecordEnv, "")
	if _, err := startTreasuryTestNode(t, db, path); err != nil {
		t.Fatalf("the rewritten record must be readable: %v", err)
	}
}

func TestTreasuryConfigRerecordSwitchParsing(t *testing.T) {
	for value, want := range map[string]bool{
		"": false, " ": false, "0": false, "false": false, "no": false, "garbage": false,
		"1": true, "true": true, "TRUE": true, " 1 ": true,
	} {
		got := treasuryConfigRerecordRequested(func(string) string { return value })
		if got != want {
			t.Errorf("%q: got %v want %v", value, got, want)
		}
	}
}

// The record is not chain state: it is not in the state trie, so the genesis
// block, its state root and the state a started node holds are the same
// whether or not a node has recorded anything.
func TestTreasuryConfigRecordDoesNotTouchChainState(t *testing.T) {
	spec := treasuryTestSpec(t)
	path := writeTreasuryTestSpec(t, spec)

	plainDB := storage.NewMemDB()
	t.Cleanup(plainDB.Close)
	plain, err := NewBlockchain(plainDB, path, false)
	if err != nil {
		t.Fatalf("create chain: %v", err)
	}
	if _, err := plainDB.Get(treasuryConfigKey); err == nil {
		t.Fatalf("a chain opened without a node must have no record")
	}

	nodeDB := storage.NewMemDB()
	t.Cleanup(nodeDB.Close)
	node, err := startTreasuryTestNode(t, nodeDB, path)
	if err != nil {
		t.Fatalf("start node: %v", err)
	}
	if _, err := nodeDB.Get(treasuryConfigKey); err != nil {
		t.Fatalf("the node must have recorded its configuration: %v", err)
	}

	if !bytes.Equal(plain.GenesisHash(), node.chain.GenesisHash()) || plain.ChainID() != node.chain.ChainID() {
		t.Fatalf("the genesis hash and chain id must not depend on the record")
	}
	plainRoot := plain.CurrentHeader().StateRoot
	if !bytes.Equal(plainRoot, node.chain.CurrentHeader().StateRoot) {
		t.Fatalf("the genesis state root must not depend on the record")
	}
	if got := node.PendingStateRoot(); !bytes.Equal(got, plainRoot) {
		t.Fatalf("recording the configuration changed the node's state root: %x vs %x", got, plainRoot)
	}

	// Recording again, or replacing the record, leaves the state alone as well.
	t.Setenv(treasuryConfigRerecordEnv, "1")
	changed := spec
	changed.AdminWallet = treasuryTestAddrString(0x02)
	again, err := startTreasuryTestNode(t, nodeDB, writeTreasuryTestSpec(t, changed))
	if err != nil {
		t.Fatalf("restart with the switch: %v", err)
	}
	if got := again.chain.CurrentHeader().StateRoot; !bytes.Equal(got, plainRoot) {
		t.Fatalf("replacing the record changed the stored state root")
	}
}
