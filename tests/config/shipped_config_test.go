package config_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"nhbchain/config"
	"nhbchain/crypto"
)

// The loader silently ignores every key it has no field for (a misspelt
// owner_wallet, or a key placed below the wrong table header), and the node
// then runs on the default instead of the value the file appears to set.
// No shipped node config may contain such a key.
var bareNetworkID = regexp.MustCompile(`(?m)^([ \t]*NetworkId[ \t]*=[ \t]*)(\d+)([ \t]*)$`)

func TestShippedConfigsHaveNoIgnoredKeys(t *testing.T) {
	for _, rel := range []string{
		"config.toml",
		"config/prod.toml",
		"deploy/compose/config/consensus.toml",
		"deploy/compose/config/p2p.toml",
		"examples/compose/mininet/config/consensus.toml",
		"examples/compose/mininet/config/p2p.toml",
	} {
		t.Run(rel, func(t *testing.T) {
			// config.Load quotes a bare NetworkId first: the id can exceed
			// TOML's signed 64-bit integer range.
			quoted := bareNetworkID.ReplaceAll(readRepoFile(t, rel), []byte(`$1"$2"$3`))
			meta, err := toml.Decode(string(quoted), &config.Config{})
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if undecoded := meta.Undecoded(); len(undecoded) != 0 {
				t.Fatalf("the node ignores these keys: %v", undecoded)
			}
		})
	}
}

// Both shipped configs must load, and everything the node parses from them at
// startup must be valid: an empty treasury or fee wallet here used to make a
// node panic or route fees nowhere.
func TestShippedConfigsLoadAndParseAtStartup(t *testing.T) {
	for _, rel := range []string{"config.toml", "config/prod.toml"} {
		t.Run(rel, func(t *testing.T) {
			content := string(readRepoFile(t, rel))
			if rel == "config.toml" {
				// It keeps the validator key in a keystore file, which Load
				// would create on first use; read the key from the
				// environment instead so the test touches no files.
				if strings.Count(content, `ValidatorKMSEnv = ""`) != 1 {
					t.Fatalf("expected exactly one empty ValidatorKMSEnv in %s", rel)
				}
				content = strings.Replace(content, `ValidatorKMSEnv = ""`, `ValidatorKMSEnv = "NHB_TEST_VALIDATOR_KEY"`, 1)
			}
			path := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("write config: %v", err)
			}
			cfg, err := config.Load(path)
			if err != nil {
				t.Fatalf("load: %v", err)
			}

			if _, err := cfg.PotsoRewardConfig(); err != nil {
				t.Errorf("POTSO rewards: %v", err)
			}
			if _, err := cfg.PotsoWeightConfig(); err != nil {
				t.Errorf("POTSO weights: %v", err)
			}
			if _, err := cfg.Governance.Policy(); err != nil {
				t.Errorf("governance policy: %v", err)
			}
			if err := config.ValidateConfig(cfg.Global); err != nil {
				t.Errorf("global config: %v", err)
			}
			if _, err := cfg.Global.PaymasterLimits(); err != nil {
				t.Errorf("paymaster limits: %v", err)
			}
			if _, err := cfg.Global.PaymasterAutoTopUpConfig(); err != nil {
				t.Errorf("paymaster auto top-up: %v", err)
			}

			wallets := map[string]string{
				"global.Fees.OwnerWallet":       cfg.Global.Fees.OwnerWallet,
				"potso.rewards.TreasuryAddress": cfg.Potso.Rewards.TreasuryAddress,
			}
			for asset, wallet := range cfg.Global.Fees.RouteWalletByAsset() {
				wallets["global.Fees.Assets["+asset+"].OwnerWallet"] = wallet
			}
			if len(cfg.Global.Fees.RouteWalletByAsset()) == 0 {
				t.Errorf("no fee asset has an owner wallet")
			}
			for name, wallet := range wallets {
				if strings.TrimSpace(wallet) == "" {
					t.Errorf("%s is empty", name)
					continue
				}
				if _, err := crypto.DecodeAddress(wallet); err != nil {
					t.Errorf("%s %q: %v", name, wallet, err)
				}
			}
		})
	}
}

// config/prod.toml is the production template: the node terminates TLS for the
// RPC, so the settings that say so must be ones the node reads.
func TestProductionTemplateTerminatesRPCTLSAtTheNode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, readRepoFile(t, "config/prod.toml"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.RPCAllowInsecure {
		t.Errorf("RPCAllowInsecure must be false in the production template")
	}
	if cfg.RPCTLSCertFile == "" || cfg.RPCTLSKeyFile == "" || cfg.RPCTLSClientCAFile == "" {
		t.Errorf("RPC TLS settings are not read: cert=%q key=%q clientCA=%q", cfg.RPCTLSCertFile, cfg.RPCTLSKeyFile, cfg.RPCTLSClientCAFile)
	}
	if cfg.ValidatorKMSEnv == "" {
		t.Errorf("the validator key must come from an environment variable the node can read")
	}
	if cfg.P2P.NetworkID != liveChainID {
		t.Errorf("NetworkId %d, want %d", cfg.P2P.NetworkID, liveChainID)
	}
}
