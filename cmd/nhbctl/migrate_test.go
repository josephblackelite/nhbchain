package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testLegacyKeyHex = "0101010101010101010101010101010101010101010101010101010101010101"

// A config as an operator keeps it: comments, many settings, tables, a network id above the
// largest 64-bit signed integer, CRLF line endings on one line.
func legacyConfig(network string) string {
	return strings.Join([]string{
		`# validator config, edited by hand`,
		`ListenAddress = "0.0.0.0:7000"`,
		`RPCAddress = "0.0.0.0:9000"`,
		`DataDir = "./data"`,
		`GenesisFile = "genesis.json"`,
		`ValidatorKey = "0x` + testLegacyKeyHex + `"   # legacy, to be migrated`,
		`ValidatorKeystorePath = ""`,
		`NetworkName = "` + network + `"`,
		`PeerBanSeconds = 120`,
		`RPCAllowInsecure = true`,
		`QuorumCertActivationHeight = 1380200`,
		``,
		`[p2p]`,
		`NetworkId = 18346390202490284624`,
		`MaxPeers = 42`,
		``,
	}, "\n")
}

// Ledger PL-EXT-76: migrate-keystore decoded the config into an eleven-field struct and wrote that
// back, so every other setting and section was lost, no backup was kept, and the node could not
// load what was left. Now one line changes and a copy is kept.
func TestMigrateKeystoreChangesOneLineAndKeepsTheRest(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	original := legacyConfig("testnet")
	if err := os.WriteFile(configPath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_MIGRATE_PASS", "a-passphrase")
	keystorePath := filepath.Join(dir, "validator.keystore")

	if err := migrateKeystore(configPath, keystorePath, "TEST_MIGRATE_PASS", false); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	gotLines := strings.Split(string(got), "\n")
	wantLines := strings.Split(original, "\n")
	// The ValidatorKey line became the keystore line and the empty keystore line is gone: one line fewer.
	if len(gotLines) != len(wantLines)-1 {
		t.Fatalf("config has %d lines, want %d:\n%s", len(gotLines), len(wantLines)-1, got)
	}
	for _, keep := range []string{
		`# validator config, edited by hand`, `ListenAddress = "0.0.0.0:7000"`, `NetworkName = "testnet"`,
		`PeerBanSeconds = 120`, `QuorumCertActivationHeight = 1380200`, `[p2p]`, `NetworkId = 18346390202490284624`, `MaxPeers = 42`,
	} {
		if !strings.Contains(string(got), keep+"\n") {
			t.Errorf("the line %q is gone from the migrated config", keep)
		}
	}
	if strings.Contains(string(got), "ValidatorKey ") || strings.Contains(string(got), testLegacyKeyHex) {
		t.Errorf("the plaintext key is still in the config:\n%s", got)
	}
	if strings.Count(string(got), "ValidatorKeystorePath") != 1 {
		t.Errorf("want exactly one ValidatorKeystorePath line:\n%s", got)
	}
	// The part from the first table on is byte for byte the original.
	if !strings.HasSuffix(string(got), original[strings.Index(original, "[p2p]"):]) {
		t.Errorf("the tables changed")
	}
	if info, err := os.Stat(configPath); err != nil || info.Mode().Perm() != 0o640 && os.PathSeparator != '\\' {
		t.Errorf("the config's permission bits changed: %v %v", info, err)
	}

	backups, _ := filepath.Glob(configPath + ".bak-premigrate-*")
	if len(backups) != 1 {
		t.Fatalf("want one backup, found %v", backups)
	}
	if b, _ := os.ReadFile(backups[0]); string(b) != original {
		t.Errorf("the backup is not the original config")
	}
	if _, err := os.Stat(keystorePath); err != nil {
		t.Errorf("no keystore written: %v", err)
	}
}

// If the finished config does not load, the operator's own file must be put back.
func TestMigrateKeystoreRestoresTheOriginalWhenTheResultDoesNotLoad(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.toml")
	// A production network name without swap secrets is refused by config.Load.
	original := legacyConfig("mainnet")
	if err := os.WriteFile(configPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_MIGRATE_PASS", "a-passphrase")

	err := migrateKeystore(configPath, filepath.Join(dir, "validator.keystore"), "TEST_MIGRATE_PASS", false)
	if err == nil || !strings.Contains(err.Error(), "original config was put back") {
		t.Fatalf("want a verification failure that says the original was put back, got %v", err)
	}
	if got, _ := os.ReadFile(configPath); string(got) != original {
		t.Fatalf("the original config was not restored:\n%s", got)
	}
}

func TestScanTopLevelKeys(t *testing.T) {
	cases := []struct {
		name         string
		text         string
		wantKey      string
		wantKeystore bool
		wantErr      bool
	}{
		{"double quoted with comment", "ValidatorKey = \"0xabc\" # note\n", "0xabc", false, false},
		{"single quoted", "ValidatorKey = '0xabc'\n", "0xabc", false, false},
		{"absent", "DataDir = \"x\"\n", "", false, false},
		{"keystore already set", "ValidatorKeystorePath = \"k\"\n", "", true, false},
		{"empty keystore is not set", "ValidatorKeystorePath = \"\"\nValidatorKey = \"0xabc\"\n", "0xabc", false, false},
		{"a key inside a table is not the top-level key", "[p2p]\nValidatorKey = \"0xabc\"\n", "", false, false},
		{"not a string", "ValidatorKey = 5\n", "", false, true},
		{"crlf", "ValidatorKey = \"0xabc\"\r\nDataDir = \"x\"\r\n", "0xabc", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, ks, err := scanTopLevelKeys(tc.text)
			if (err != nil) != tc.wantErr || key != tc.wantKey || ks != tc.wantKeystore {
				t.Fatalf("got key %q keystore %v err %v; want key %q keystore %v err %v", key, ks, err, tc.wantKey, tc.wantKeystore, tc.wantErr)
			}
		})
	}
}

func TestReplaceLegacyKeyLineKeepsLineEndingsAndOtherTables(t *testing.T) {
	in := "A = 1\r\nValidatorKey = \"0xabc\"\r\n\r\n[t]\r\nValidatorKey = \"inner\"\r\n"
	out, err := replaceLegacyKeyLine(in, `C:\keys\v.keystore`)
	if err != nil {
		t.Fatal(err)
	}
	want := "A = 1\r\nValidatorKeystorePath = \"C:\\\\keys\\\\v.keystore\"\r\n\r\n[t]\r\nValidatorKey = \"inner\"\r\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
	if _, err := replaceLegacyKeyLine("A = 1\n", "k"); err == nil {
		t.Fatalf("a config without the key must be an error")
	}
}
