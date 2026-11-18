package config_test

import (
	"reflect"
	"testing"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	"nhbchain/config"
)

// config/prod.toml is the template an operator starts a production validator
// from. The values below are read by the state processor, so a validator whose
// file differs from the other validators' computes different state and stops at
// a state-root mismatch (PL-R5T-71). Everything that is not on the host-local
// list must be identical to config.toml, the file the live validators run.
//
// The host-local list is named here on purpose: adding a key to it is a
// statement that the key is a property of the host (an address, a path, a
// certificate, a rate limit of the front door) and not of the chain's rules.
var prodTemplateHostLocal = map[string]string{
	"ListenAddress":            "this host's interface",
	"RPCAddress":               "this host's interface",
	"RPCTrustedProxies":        "front-door setting",
	"RPCTrustProxyHeaders":     "front-door setting",
	"RPCProxyHeaders":          "front-door setting",
	"RPCJWT":                   "this host's token settings",
	"RPCMaxTxPerWindow":        "front-door rate limit",
	"RPCMaxTxPerIP":            "front-door rate limit",
	"RPCMaxTxPerIdentity":      "front-door rate limit",
	"RPCMaxTxPerChain":         "front-door rate limit",
	"RPCMaxTxPerIdentityChain": "front-door rate limit",
	"RPCRateLimitWindow":       "front-door rate limit",
	"RPCCallerMetadataMaxTTL":  "front-door setting",
	"RPCAllowInsecure":         "this host's RPC transport",
	"RPCTLSCertFile":           "this host's certificate paths",
	"RPCTLSKeyFile":            "this host's certificate paths",
	"RPCTLSClientCAFile":       "this host's certificate paths",
	"DataDir":                  "this host's disk",
	"GenesisFile":              "this host's disk",
	"ValidatorKeystorePath":    "this host's key storage",
	"ValidatorKMSEnv":          "this host's key storage",
	"NetworkName":              "a label",
}

func decodeConfigFile(t *testing.T, rel string) config.Config {
	t.Helper()
	// The chain id can exceed TOML's signed 64-bit range, so config.Load quotes
	// a bare NetworkId first; do the same here.
	quoted := bareNetworkID.ReplaceAll(readRepoFile(t, rel), []byte(`$1"$2"$3`))
	var c config.Config
	if _, err := toml.Decode(string(quoted), &c); err != nil {
		t.Fatalf("decode %s: %v", rel, err)
	}
	return c
}

func TestProdTemplateHostLocalListNamesRealFields(t *testing.T) {
	typ := reflect.TypeOf(config.Config{})
	for name := range prodTemplateHostLocal {
		if _, ok := typ.FieldByName(name); !ok {
			t.Errorf("the host-local list names %q, which is not a field of config.Config", name)
		}
	}
}

func TestProdTemplateEqualsShippedConfigExceptHostLocalKeys(t *testing.T) {
	shipped, prod := decodeConfigFile(t, "config.toml"), decodeConfigFile(t, "config/prod.toml")
	// The two lines that name quote sources and voucher providers are left to the
	// operator to copy (the template does not repeat those names).
	for _, c := range []*config.Config{&shipped, &prod} {
		c.Swap.OraclePriority = nil
		c.Swap.Providers.Allow = nil
	}
	sv, pv := reflect.ValueOf(shipped), reflect.ValueOf(prod)
	for i := 0; i < sv.NumField(); i++ {
		name := sv.Type().Field(i).Name
		if _, local := prodTemplateHostLocal[name]; local || name == "UndecodedKeys" {
			continue
		}
		if !reflect.DeepEqual(sv.Field(i).Interface(), pv.Field(i).Interface()) {
			t.Errorf("config/prod.toml differs from config.toml in %s (not on the host-local list)", name)
		}
	}
}

// The wallets that receive fees and the treasuries the rewards and the
// subscription fee are paid from are the same in both files and are not empty.
func TestProdTemplateTreasuriesMatchTheShippedConfig(t *testing.T) {
	shipped, prod := decodeConfigFile(t, "config.toml"), decodeConfigFile(t, "config/prod.toml")
	if prod.Global.Fees.OwnerWallet == "" || prod.Potso.Rewards.TreasuryAddress == "" || prod.Subscriptions.Treasury == "" {
		t.Fatalf("the template leaves a treasury or fee wallet empty")
	}
	if prod.Global.Fees.OwnerWallet != shipped.Global.Fees.OwnerWallet {
		t.Errorf("fee owner wallet differs from config.toml")
	}
	if prod.Potso.Rewards.TreasuryAddress != shipped.Potso.Rewards.TreasuryAddress {
		t.Errorf("POTSO treasury differs from config.toml")
	}
	if prod.Subscriptions.Treasury != shipped.Subscriptions.Treasury {
		t.Errorf("subscriptions treasury differs from config.toml")
	}
	if len(prod.Global.Fees.Assets) != len(shipped.Global.Fees.Assets) {
		t.Fatalf("fee asset count differs")
	}
	for i := range shipped.Global.Fees.Assets {
		if prod.Global.Fees.Assets[i] != shipped.Global.Fees.Assets[i] {
			t.Errorf("fee asset %d differs from config.toml", i)
		}
	}
}

// The Kubernetes production values carry a node configuration too. A node built
// from them used to take the Go defaults for everything the values left out (a
// third pair of fee wallets, no module quotas, a different emission), which is a
// state-root mismatch with the other validators.
func TestHelmProdConsensusValuesEqualShippedConfigExceptHostLocalKeys(t *testing.T) {
	var values struct {
		Config struct {
			Content string `yaml:"content"`
		} `yaml:"config"`
	}
	if err := yaml.Unmarshal(readRepoFile(t, "deploy/helm/values/prod/consensusd.yaml"), &values); err != nil {
		t.Fatalf("parse the helm values: %v", err)
	}
	quoted := bareNetworkID.ReplaceAll([]byte(values.Config.Content), []byte(`$1"$2"$3`))
	var helm config.Config
	if _, err := toml.Decode(string(quoted), &helm); err != nil {
		t.Fatalf("decode config.content: %v", err)
	}
	shipped := decodeConfigFile(t, "config.toml")
	for _, c := range []*config.Config{&shipped, &helm} {
		c.Swap.OraclePriority = nil
		c.Swap.Providers.Allow = nil
	}
	// The pod's mempool and peer limits are sized for the pod, and its transport material is the pod's own.
	podLocal := map[string]bool{"Mempool": true, "P2P": true, "NetworkSecurity": true}
	sv, hv := reflect.ValueOf(shipped), reflect.ValueOf(helm)
	for i := 0; i < sv.NumField(); i++ {
		name := sv.Type().Field(i).Name
		if _, local := prodTemplateHostLocal[name]; local || podLocal[name] || name == "UndecodedKeys" {
			continue
		}
		if !reflect.DeepEqual(sv.Field(i).Interface(), hv.Field(i).Interface()) {
			t.Errorf("deploy/helm/values/prod/consensusd.yaml differs from config.toml in %s", name)
		}
	}
}
