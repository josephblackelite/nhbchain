package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"nhbchain/crypto"

	"github.com/BurntSushi/toml"
)

type Config struct {
	ListenAddress         string   `toml:"ListenAddress"`
	RPCAddress            string   `toml:"RPCAddress"`
	DataDir               string   `toml:"DataDir"`
	GenesisFile           string   `toml:"GenesisFile"`
	ValidatorKeystorePath string   `toml:"ValidatorKeystorePath"`
	ValidatorKMSURI       string   `toml:"ValidatorKMSURI"`
	ValidatorKMSEnv       string   `toml:"ValidatorKMSEnv"`
	NetworkName           string   `toml:"NetworkName"`
	Bootnodes             []string `toml:"Bootnodes"`
	PersistentPeers       []string `toml:"PersistentPeers"`
	BootstrapPeers        []string `toml:"BootstrapPeers,omitempty"`
}

// Load loads the configuration from the given path.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return createDefault(path)
	}

	meta, err := toml.DecodeFile(path, cfg)
	if err != nil {
		return nil, err
	}

	for _, undecoded := range meta.Undecoded() {
		if len(undecoded) == 1 && undecoded[0] == "ValidatorKey" {
			return nil, fmt.Errorf("config file %s uses deprecated ValidatorKey field; run nhbctl migrate-keystore", path)
		}
	}

	if cfg.ValidatorKMSURI == "" && cfg.ValidatorKMSEnv == "" {
		if err := ensureKeystore(path, cfg); err != nil {
			return nil, err
		}
	}

	if strings.TrimSpace(cfg.NetworkName) == "" {
		cfg.NetworkName = "nhb-local"
	}
	if cfg.Bootnodes == nil {
		cfg.Bootnodes = []string{}
	}
	if cfg.PersistentPeers == nil {
		cfg.PersistentPeers = []string{}
	}
	if len(cfg.Bootnodes) == 0 && len(cfg.BootstrapPeers) > 0 {
		cfg.Bootnodes = append([]string{}, cfg.BootstrapPeers...)
	}
	cfg.BootstrapPeers = nil

	return cfg, nil
}

func ensureKeystore(configPath string, cfg *Config) error {
	keystorePath := cfg.ValidatorKeystorePath
	if keystorePath == "" {
		keystorePath = defaultKeystorePath(configPath)
	}

	if _, err := os.Stat(keystorePath); os.IsNotExist(err) {
		key, genErr := crypto.GeneratePrivateKey()
		if genErr != nil {
			return genErr
		}
		if err := crypto.SaveToKeystore(keystorePath, key, ""); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}

	if cfg.ValidatorKeystorePath != keystorePath {
		cfg.ValidatorKeystorePath = keystorePath
		return persist(configPath, cfg)
	}

	return nil
}

// createDefault creates and saves a default configuration file.
func createDefault(path string) (*Config, error) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}

	keystorePath := defaultKeystorePath(path)
	if err := crypto.SaveToKeystore(keystorePath, key, ""); err != nil {
		return nil, err
	}

	cfg := &Config{
		ListenAddress:   ":6001",
		RPCAddress:      ":8080",
		DataDir:         "./nhb-data",
		GenesisFile:     "",
		NetworkName:     "nhb-local",
		Bootnodes:       []string{},
		PersistentPeers: []string{},
	}
	cfg.ValidatorKeystorePath = keystorePath

	if err := persist(path, cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}

func persist(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()

	return toml.NewEncoder(f).Encode(cfg)
}

func defaultKeystorePath(configPath string) string {
	dir := filepath.Dir(configPath)
	if dir == "." || dir == "" {
		dir = ""
	}
	return filepath.Join(dir, "validator.keystore")
}
