package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"nhbchain/config"
	"nhbchain/crypto"
)

const (
	migrateCommand  = "migrate-keystore"
	defaultPassEnv  = "NHB_VALIDATOR_PASS"
	defaultConfig   = "./config.toml"
	defaultKeystore = "validator.keystore"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case migrateCommand:
		runMigrate(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func runMigrate(args []string) {
	fs := flag.NewFlagSet(migrateCommand, flag.ExitOnError)
	configPath := fs.String("config", defaultConfig, "Path to the NHB config file")
	keystorePath := fs.String("keystore", "", "Output path for the generated keystore file")
	passEnv := fs.String("pass-env", defaultPassEnv, "Environment variable containing the keystore passphrase")
	force := fs.Bool("force", false, "Overwrite an existing keystore file")
	fs.Parse(args)

	if err := migrateKeystore(*configPath, *keystorePath, *passEnv, *force); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

// migrateKeystore moves a plaintext ValidatorKey out of the config into an
// encrypted keystore. It changes ONE line of the config file (the ValidatorKey
// line becomes the ValidatorKeystorePath line) and leaves every other byte as it
// was; it used to decode the file into an eleven-field struct and write that
// back, which dropped every other setting and section (ledger PL-EXT-76). A copy
// of the original is kept beside the config before anything is written, and if
// the finished config does not load, the original is put back.
func migrateKeystore(configPath, keystorePath, passEnv string, force bool) error {
	rawBytes, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}
	raw := string(rawBytes)

	legacyKey, keystoreSet, err := scanTopLevelKeys(raw)
	if err != nil {
		return fmt.Errorf("failed to read config %s: %w", configPath, err)
	}
	if legacyKey == "" {
		return fmt.Errorf("config %s does not contain a ValidatorKey field to migrate", configPath)
	}
	if keystoreSet {
		return fmt.Errorf("config %s already references a keystore", configPath)
	}

	if keystorePath == "" {
		dir := filepath.Dir(configPath)
		if dir == "." || dir == "" {
			keystorePath = defaultKeystore
		} else {
			keystorePath = filepath.Join(dir, defaultKeystore)
		}
	}

	if !force {
		if _, err := os.Stat(keystorePath); err == nil {
			return fmt.Errorf("keystore file %s already exists (use --force to overwrite)", keystorePath)
		} else if !os.IsNotExist(err) {
			return err
		}
	}

	passphrase := ""
	if passEnv != "" {
		val, ok := os.LookupEnv(passEnv)
		if !ok {
			return fmt.Errorf("environment variable %s is not set", passEnv)
		}
		if strings.TrimSpace(val) == "" {
			return fmt.Errorf("environment variable %s must not be empty", passEnv)
		}
		passphrase = val
	}
	if strings.TrimSpace(passphrase) == "" {
		return fmt.Errorf("validator keystore passphrase must be provided via %s", passEnv)
	}

	key, err := parseLegacyKey(legacyKey)
	if err != nil {
		return err
	}

	updated, err := replaceLegacyKeyLine(raw, keystorePath)
	if err != nil {
		return err
	}

	// The copy holds the plaintext key, so it is as private as the key itself.
	backupPath := configPath + ".bak-premigrate-" + time.Now().UTC().Format("20060102T150405Z")
	if err := os.WriteFile(backupPath, rawBytes, 0o600); err != nil {
		return fmt.Errorf("failed to write the backup of the config: %w", err)
	}

	if err := crypto.SaveToKeystore(keystorePath, key, passphrase); err != nil {
		return fmt.Errorf("failed to write keystore: %w", err)
	}

	if err := writeFileKeepingMode(configPath, []byte(updated)); err != nil {
		return fmt.Errorf("failed to write the updated config (the original is intact or is in %s): %w", backupPath, err)
	}

	if _, err := config.Load(configPath, config.WithKeystorePassphrase(passphrase)); err != nil {
		if restoreErr := writeFileKeepingMode(configPath, rawBytes); restoreErr != nil {
			return fmt.Errorf("verification failed after migration (%v) and the original config could not be put back (%v); the original is in %s", err, restoreErr, backupPath)
		}
		return fmt.Errorf("verification failed after migration, the original config was put back (the keystore %s was written; the copy is %s): %w", keystorePath, backupPath, err)
	}

	fmt.Printf("Wrote keystore to %s and updated %s (one line changed; the original is kept in %s)\n", keystorePath, configPath, backupPath)
	return nil
}

func parseLegacyKey(value string) (*crypto.PrivateKey, error) {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "0x")
	if trimmed == "" {
		return nil, fmt.Errorf("validator key is empty")
	}
	bytes, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("invalid validator key encoding: %w", err)
	}
	return crypto.PrivateKeyFromBytes(bytes)
}

func usage() {
	fmt.Println("nhbctl <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Printf("  %s    Convert a plaintext ValidatorKey to an encrypted keystore\n", migrateCommand)
}
