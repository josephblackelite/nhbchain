package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestConfigDecodesQueryPoolSettings(t *testing.T) {
	var cfg Config
	contents := `RPCQueryMaxConcurrent = 2
RPCQueryMaxPerClient = 3
RPCQueryQueueDepth = 16
RPCQueryQueueWaitMS = 150
RPCQueryTimeoutSeconds = 20
RPCDisableExplorerLoop = true
`
	if _, err := toml.Decode(contents, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.RPCQueryMaxConcurrent != 2 || cfg.RPCQueryMaxPerClient != 3 || cfg.RPCQueryQueueDepth != 16 ||
		cfg.RPCQueryQueueWaitMS != 150 || cfg.RPCQueryTimeoutSeconds != 20 || !cfg.RPCDisableExplorerLoop {
		t.Fatalf("unexpected query pool settings: %+v", cfg)
	}
}

// The knobs are optional: a config that does not mention them (the live one
// does not) must load with every one at zero, which the RPC server reads as its
// default.
func TestQueryPoolSettingsDefaultToZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("RPCAddress = \"127.0.0.1:8545\"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	var cfg Config
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if cfg.RPCQueryMaxConcurrent != 0 || cfg.RPCQueryMaxPerClient != 0 || cfg.RPCQueryQueueDepth != 0 ||
		cfg.RPCQueryQueueWaitMS != 0 || cfg.RPCQueryTimeoutSeconds != 0 || cfg.RPCDisableExplorerLoop {
		t.Fatalf("a config without the knobs must leave them unset (the explorer loop stays on): %+v", cfg)
	}
}
