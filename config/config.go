package config

import (
	"encoding/hex"
	"nhbchain/crypto"
	"os"

	"github.com/BurntSushi/toml"
)

type Config struct {
	ListenAddress  string   `toml:"ListenAddress"`
	RPCAddress     string   `toml:"RPCAddress"`
	DataDir        string   `toml:"DataDir"`
	ValidatorKey   string   `toml:"ValidatorKey"`
	BootstrapPeers []string `toml:"BootstrapPeers"` // THE MISSING FIELD
}

// Load loads the configuration from the given path.
func Load(path string) (*Config, error) {
	cfg := &Config{}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return createDefault(path)
	}

	if _, err := toml.DecodeFile(path, cfg); err != nil {
		return nil, err
	}

	if cfg.ValidatorKey == "" {
		key, err := crypto.GeneratePrivateKey()
		if err != nil {
			return nil, err
		}
		cfg.ValidatorKey = hex.EncodeToString(key.Bytes())

		f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, os.ModePerm)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		if err := toml.NewEncoder(f).Encode(cfg); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// createDefault creates and saves a default configuration file.
func createDefault(path string) (*Config, error) {
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		ListenAddress: ":6001",
		RPCAddress:    ":8080",
		DataDir:       "./nhb-data",
		ValidatorKey:  hex.EncodeToString(key.Bytes()),
		// Initialize with an empty list of peers by default.
		BootstrapPeers: []string{},
	}

	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
		return nil, err
	}

	return cfg, nil
}
