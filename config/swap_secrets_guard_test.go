package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ledger PL-EXT-74: the refusal to start without swap RPC secrets named only
// "mainnet", so a node named nhbchain-1 (the live validators' name) started
// without them. The test fails on the code before the change for nhbchain-1.
func TestLoadRefusesEmptySwapSecretsOnProductionNetworkNames(t *testing.T) {
	cases := []struct {
		name       string
		network    string
		secrets    string
		wantRefuse bool
	}{
		{"mainnet without secrets", "mainnet", "", true},
		{"nhbchain-1 without secrets", "nhbchain-1", "", true},
		{"nhbchain-1 with secrets", "nhbchain-1", "[RPCSwapAuth.Secrets]\nacme = \"0123456789abcdef0123456789abcdef\"\n", false},
		{"local network without secrets", "nhb-local", "", false},
		{"testnet without secrets", "testnet", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			contents := fmt.Sprintf(`ListenAddress = "0.0.0.0:7000"
RPCAddress = "0.0.0.0:9000"
DataDir = "./data"
GenesisFile = "genesis.json"
ValidatorKeystorePath = %s
NetworkName = %s
RPCAllowInsecure = true

%s`, tomlQuoted(filepath.Join(dir, "validator.keystore")), tomlQuoted(tc.network), tc.secrets)
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			_, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
			refused := err != nil && strings.Contains(err.Error(), "swap RPC authentication secrets are required")
			if refused != tc.wantRefuse {
				t.Fatalf("network %q: refused for missing swap secrets = %v, want %v (error: %v)", tc.network, refused, tc.wantRefuse, err)
			}
		})
	}
}

func TestIsProductionNetworkName(t *testing.T) {
	for _, name := range []string{"mainnet", "nhbchain-1"} {
		if !isProductionNetworkName(name) {
			t.Errorf("%q must be a production network name", name)
		}
	}
	for _, name := range []string{"", "nhb-local", "testnet", "Mainnet", "mainnet "} {
		if isProductionNetworkName(name) {
			t.Errorf("%q must not be a production network name", name)
		}
	}
}
