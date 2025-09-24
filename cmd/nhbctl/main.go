package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"nhbchain/config"
	"nhbchain/crypto"

	"github.com/BurntSushi/toml"
)

const (
	migrateCommand  = "migrate-keystore"
	defaultPassEnv  = "NHB_VALIDATOR_PASS"
	defaultConfig   = "./config.toml"
	defaultKeystore = "validator.keystore"
)

type fileConfig struct {
	ListenAddress         string   `toml:"ListenAddress"`
	RPCAddress            string   `toml:"RPCAddress"`
	DataDir               string   `toml:"DataDir"`
	ValidatorKey          string   `toml:"ValidatorKey"`
	ValidatorKeystorePath string   `toml:"ValidatorKeystorePath"`
	ValidatorKMSURI       string   `toml:"ValidatorKMSURI"`
	ValidatorKMSEnv       string   `toml:"ValidatorKMSEnv"`
	NetworkName           string   `toml:"NetworkName"`
	Bootnodes             []string `toml:"Bootnodes"`
	PersistentPeers       []string `toml:"PersistentPeers"`
	BootstrapPeers        []string `toml:"BootstrapPeers"`
}

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

func migrateKeystore(configPath, keystorePath, passEnv string, force bool) error {
	var cfg fileConfig
	if _, err := toml.DecodeFile(configPath, &cfg); err != nil {
		return fmt.Errorf("failed to read config: %w", err)
	}

	if cfg.ValidatorKey == "" {
		return fmt.Errorf("config %s does not contain a ValidatorKey field to migrate", configPath)
	}
	if cfg.ValidatorKeystorePath != "" {
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
		passphrase = val
	}

	key, err := parseLegacyKey(cfg.ValidatorKey)
	if err != nil {
		return err
	}

	if err := crypto.SaveToKeystore(keystorePath, key, passphrase); err != nil {
		return fmt.Errorf("failed to write keystore: %w", err)
	}

	cfg.ValidatorKey = ""
	cfg.ValidatorKeystorePath = keystorePath
	if cfg.ValidatorKMSURI == "" {
		cfg.ValidatorKMSURI = ""
	}
	if cfg.ValidatorKMSEnv == "" {
		cfg.ValidatorKMSEnv = ""
	}
	if cfg.Bootnodes == nil {
		cfg.Bootnodes = []string{}
	}
	if cfg.PersistentPeers == nil {
		cfg.PersistentPeers = []string{}
	}
	if cfg.BootstrapPeers == nil {
		cfg.BootstrapPeers = []string{}
	}
	if len(cfg.Bootnodes) == 0 && len(cfg.BootstrapPeers) > 0 {
		cfg.Bootnodes = append([]string{}, cfg.BootstrapPeers...)
	}
	cfg.BootstrapPeers = nil
	if strings.TrimSpace(cfg.NetworkName) == "" {
		cfg.NetworkName = "nhb-local"
	}

	if err := writeConfig(configPath, cfg); err != nil {
		return err
	}

	if _, err := config.Load(configPath); err != nil {
		return fmt.Errorf("verification failed after migration: %w", err)
	}

	fmt.Printf("Wrote keystore to %s and updated %s\n", keystorePath, configPath)
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

func writeConfig(path string, cfg fileConfig) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	return toml.NewEncoder(f).Encode(cfg)
}

func usage() {
	fmt.Println("nhbctl <command>")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Printf("  %s    Convert a plaintext ValidatorKey to an encrypted keystore\n", migrateCommand)
}
