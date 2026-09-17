package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"nhbchain/crypto"
)

// Environment variable names read by `nhb-cli keystore import`. Both are
// deliberately environment variables, never bare CLI arguments: an argument
// value leaks into shell history (~/.bash_history and similar) and into any
// process listing (`ps aux`, /proc/<pid>/cmdline) for as long as the
// process runs. Setting these via `export` in a shell with history
// disabled, or as a one-shot `VAR=... nhb-cli keystore import ...` prefix
// (which bash does not record in history), avoids both.
const (
	// NHB_KEYSTORE_IMPORT_PRIVATE_KEY must hold the raw hex-encoded private
	// key to import (with or without a "0x" prefix).
	keystoreImportPrivateKeyEnv = "NHB_KEYSTORE_IMPORT_PRIVATE_KEY"
	// NHB_KEYSTORE_IMPORT_PASSPHRASE must hold the passphrase used to
	// encrypt the resulting keystore file. This is the same passphrase an
	// operator later points services/swapd's price_proof.signer.passphrase_env
	// at (see localsigner.Config) or supplies to any other consumer of the
	// resulting file.
	keystoreImportPassphraseEnv = "NHB_KEYSTORE_IMPORT_PASSPHRASE"
)

// runKeystoreCommand dispatches `nhb-cli keystore <subcommand>`.
func runKeystoreCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, keystoreUsage())
		return 1
	}
	switch args[0] {
	case "import":
		return runKeystoreImport(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown keystore subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, keystoreUsage())
		return 1
	}
}

func keystoreUsage() string {
	return strings.TrimSpace(`Usage:
  nhb-cli keystore import --out <path>

Subcommands:
  import --out <path>   Encrypt a raw hex private key into a local Ethereum
                         V3 keystore file (the same encryption already used
                         for validator keys). Reads the key from
                         NHB_KEYSTORE_IMPORT_PRIVATE_KEY and the encryption
                         passphrase from NHB_KEYSTORE_IMPORT_PASSPHRASE --
                         both environment variables, never CLI arguments.
                         Writes to --out, then immediately re-reads and
                         decrypts that file to confirm the write succeeded,
                         and prints the resulting public address so you can
                         visually confirm it matches the wallet you meant to
                         import before trusting the file for anything (e.g.
                         pointing services/swapd's local price-proof signer
                         at it).
`)
}

// runKeystoreImport implements `nhb-cli keystore import`. See keystoreUsage
// above for the operator-facing summary; the steps below are the exact
// sequence it performs:
//
//  1. Read the raw hex private key from NHB_KEYSTORE_IMPORT_PRIVATE_KEY and
//     the encryption passphrase from NHB_KEYSTORE_IMPORT_PASSPHRASE.
//  2. Derive the key pair from the hex bytes and compute its address.
//  3. Call crypto.SaveToKeystore (the exact same Ethereum V3 keystore
//     encryption this repo already uses for validator keys -- no new
//     encryption scheme) to write the encrypted file to --out.
//  4. Immediately call crypto.LoadFromKeystore on the freshly-written file
//     with the same passphrase, and confirm the recovered address matches
//     the address derived in step 2. This catches a corrupted or
//     unexpectedly-wrong write right here, at import time, instead of
//     leaving it to surface later as a cryptic decrypt failure when
//     something like swapd tries to start with this file.
//  5. Print the public address. The operator is expected to visually
//     compare it against the address they expect for the wallet being
//     imported -- this command has no independent way to know whether the
//     correct key was supplied, only whether the file it wrote can be
//     decrypted back to whatever key it was given.
func runKeystoreImport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("keystore import", flag.ContinueOnError)
	fs.SetOutput(stderr)
	outPath := fs.String("out", "", "output path for the encrypted keystore file (required)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	path := strings.TrimSpace(*outPath)
	if path == "" {
		fmt.Fprintln(stderr, "Error: --out <path> is required")
		fmt.Fprintln(stderr, keystoreUsage())
		return 1
	}

	rawHex, ok := os.LookupEnv(keystoreImportPrivateKeyEnv)
	if !ok || strings.TrimSpace(rawHex) == "" {
		fmt.Fprintf(stderr, "Error: %s must be set to the hex-encoded private key to import\n", keystoreImportPrivateKeyEnv)
		return 1
	}
	passphrase, ok := os.LookupEnv(keystoreImportPassphraseEnv)
	if !ok || strings.TrimSpace(passphrase) == "" {
		fmt.Fprintf(stderr, "Error: %s must be set to the keystore encryption passphrase\n", keystoreImportPassphraseEnv)
		return 1
	}

	trimmedHex := strings.TrimSpace(rawHex)
	trimmedHex = strings.TrimPrefix(trimmedHex, "0x")
	trimmedHex = strings.TrimPrefix(trimmedHex, "0X")
	keyBytes, err := hex.DecodeString(trimmedHex)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %s is not valid hex: %v\n", keystoreImportPrivateKeyEnv, err)
		return 1
	}
	privKey, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %s does not decode to a valid private key: %v\n", keystoreImportPrivateKeyEnv, err)
		return 1
	}
	expectedAddr := privKey.PubKey().Address().String()

	if err := crypto.SaveToKeystore(path, privKey, passphrase); err != nil {
		fmt.Fprintf(stderr, "Error: failed to write keystore to %s: %v\n", path, err)
		return 1
	}

	// Write-then-verify: immediately decrypt what was just written and
	// confirm it recovers the same address, so a corrupted or unexpectedly
	// wrong file is caught here, not weeks later when a service tries to
	// load it and fails.
	reloaded, err := crypto.LoadFromKeystore(path, passphrase)
	if err != nil {
		fmt.Fprintf(stderr, "Error: wrote keystore to %s but failed to decrypt it back: %v\n", path, err)
		fmt.Fprintln(stderr, "Do not trust this file -- delete it and try again.")
		return 1
	}
	recoveredAddr := reloaded.PubKey().Address().String()
	if recoveredAddr != expectedAddr {
		fmt.Fprintf(stderr, "Error: keystore round-trip address mismatch: wrote %s, read back %s\n", expectedAddr, recoveredAddr)
		fmt.Fprintln(stderr, "Do not trust this file -- delete it and try again.")
		return 1
	}

	fmt.Fprintf(stdout, "Keystore written to: %s\n", path)
	fmt.Fprintf(stdout, "Public address:      %s\n", recoveredAddr)
	fmt.Fprintln(stdout, "Verified: decrypting the written file recovers the same address.")
	fmt.Fprintln(stdout, "")
	fmt.Fprintln(stdout, "CONFIRM the address above matches the wallet you intended to import")
	fmt.Fprintln(stdout, "before pointing anything (e.g. services/swapd's local price-proof")
	fmt.Fprintln(stdout, "signer) at this file.")
	return 0
}
