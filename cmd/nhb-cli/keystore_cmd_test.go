package main

import (
	"bytes"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"nhbchain/crypto"
)

func TestRunKeystoreImportWriteThenVerifyRoundTrip(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	expectedAddr := key.PubKey().Address().String()

	t.Setenv(keystoreImportPrivateKeyEnv, hex.EncodeToString(key.Bytes()))
	t.Setenv(keystoreImportPassphraseEnv, "correct horse battery staple")

	outPath := filepath.Join(t.TempDir(), "swapd-price-signer.keystore.json")
	var stdout, stderr bytes.Buffer
	code := runKeystoreImport([]string{"--out", outPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected success, got code %d, stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), expectedAddr) {
		t.Fatalf("expected stdout to print address %s, got: %s", expectedAddr, stdout.String())
	}
	if !strings.Contains(stdout.String(), "Verified") {
		t.Fatalf("expected stdout to confirm the write-then-verify round trip, got: %s", stdout.String())
	}

	// Independently confirm the file on disk actually decrypts to the same
	// key, using the package this command wraps directly (not just trusting
	// the command's own claimed success).
	reloaded, err := crypto.LoadFromKeystore(outPath, "correct horse battery staple")
	if err != nil {
		t.Fatalf("expected written keystore to be independently decryptable: %v", err)
	}
	if got := reloaded.PubKey().Address().String(); got != expectedAddr {
		t.Fatalf("independent reload address mismatch: got %s want %s", got, expectedAddr)
	}
}

func TestRunKeystoreImportRequiresOutFlag(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(keystoreImportPrivateKeyEnv, hex.EncodeToString(key.Bytes()))
	t.Setenv(keystoreImportPassphraseEnv, "some-passphrase")

	var stdout, stderr bytes.Buffer
	code := runKeystoreImport(nil, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected failure when --out is missing")
	}
}

func TestRunKeystoreImportRequiresPrivateKeyEnv(t *testing.T) {
	t.Setenv(keystoreImportPassphraseEnv, "some-passphrase")
	outPath := filepath.Join(t.TempDir(), "out.json")

	var stdout, stderr bytes.Buffer
	code := runKeystoreImport([]string{"--out", outPath}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected failure when %s is unset", keystoreImportPrivateKeyEnv)
	}
	if !strings.Contains(stderr.String(), keystoreImportPrivateKeyEnv) {
		t.Fatalf("expected error to mention %s, got: %s", keystoreImportPrivateKeyEnv, stderr.String())
	}
}

func TestRunKeystoreImportRequiresPassphraseEnv(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	t.Setenv(keystoreImportPrivateKeyEnv, hex.EncodeToString(key.Bytes()))
	outPath := filepath.Join(t.TempDir(), "out.json")

	var stdout, stderr bytes.Buffer
	code := runKeystoreImport([]string{"--out", outPath}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected failure when %s is unset", keystoreImportPassphraseEnv)
	}
	if !strings.Contains(stderr.String(), keystoreImportPassphraseEnv) {
		t.Fatalf("expected error to mention %s, got: %s", keystoreImportPassphraseEnv, stderr.String())
	}
}

func TestRunKeystoreImportRejectsInvalidHex(t *testing.T) {
	t.Setenv(keystoreImportPrivateKeyEnv, "not-valid-hex")
	t.Setenv(keystoreImportPassphraseEnv, "some-passphrase")
	outPath := filepath.Join(t.TempDir(), "out.json")

	var stdout, stderr bytes.Buffer
	code := runKeystoreImport([]string{"--out", outPath}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected failure for invalid hex private key")
	}
}

func TestRunKeystoreImportAcceptsHexWith0xPrefix(t *testing.T) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	expectedAddr := key.PubKey().Address().String()

	t.Setenv(keystoreImportPrivateKeyEnv, "0x"+hex.EncodeToString(key.Bytes()))
	t.Setenv(keystoreImportPassphraseEnv, "some-passphrase")
	outPath := filepath.Join(t.TempDir(), "out.json")

	var stdout, stderr bytes.Buffer
	code := runKeystoreImport([]string{"--out", outPath}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected success with 0x-prefixed hex, stderr: %s", stderr.String())
	}
	if !strings.Contains(stdout.String(), expectedAddr) {
		t.Fatalf("expected stdout to print address %s, got: %s", expectedAddr, stdout.String())
	}
}

func TestKeystoreUsageMentionsBothEnvVars(t *testing.T) {
	usage := keystoreUsage()
	if !strings.Contains(usage, keystoreImportPrivateKeyEnv) {
		t.Fatalf("expected usage text to mention %s", keystoreImportPrivateKeyEnv)
	}
	if !strings.Contains(usage, keystoreImportPassphraseEnv) {
		t.Fatalf("expected usage text to mention %s", keystoreImportPassphraseEnv)
	}
}

func TestRunKeystoreCommandUnknownSubcommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runKeystoreCommand([]string{"bogus"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected failure for unknown subcommand")
	}
}

func TestRunKeystoreCommandNoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runKeystoreCommand(nil, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected failure with no subcommand")
	}
}
