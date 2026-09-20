package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	repoConfig  = "../../config.toml"
	repoGenesis = "../../config/genesis.relaunch.json"
)

// The config a new validator is built from must carry the values the live
// validators run with. This is the guard that keeps config.toml from drifting
// away from the genesis it is shipped with (it once named the treasury of an
// earlier network, which made a node started from a snapshot diverge at the
// first reward epoch).
func TestShippedConfigCarriesTheLiveConsensusValues(t *testing.T) {
	bad, err := checkLiveConfig(repoConfig, repoGenesis)
	if err != nil {
		t.Fatal(err)
	}
	if len(bad) > 0 {
		t.Fatalf("config.toml disagrees with %s:\n  %s", repoGenesis, strings.Join(bad, "\n  "))
	}
}

func mutatedConfig(t *testing.T, edit func(string) string) string {
	t.Helper()
	raw, err := os.ReadFile(repoConfig)
	if err != nil {
		t.Fatal(err)
	}
	out := edit(string(raw))
	if out == string(raw) {
		t.Fatalf("the edit changed nothing")
	}
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestConfigWithADriftedValueIsRefused(t *testing.T) {
	const (
		oldAdmin    = "nhb10lephh6ffd79cc7lk6edc6rkxe9ha8xekt0y8h"
		oldTreasury = "znhb10lephh6ffd79cc7lk6edc6rkxe9ha8xemef0ak"
		liveAdmin   = "nhb1spruw63528zhhys2zxfgu2yf5ulcrlcltg3zdj"
		liveZNHB    = "znhb1spruw63528zhhys2zxfgu2yf5ulcrlclx6hfhn"
	)
	cases := map[string]struct {
		edit func(string) string
		want string
	}{
		"quorum certificate height": {
			func(s string) string {
				return strings.Replace(s, "QuorumCertActivationHeight = 0", "QuorumCertActivationHeight = 451949", 1)
			},
			"QuorumCertActivationHeight",
		},
		"potso treasury": {
			func(s string) string {
				return strings.Replace(s, "TreasuryAddress = \""+liveZNHB, "TreasuryAddress = \""+oldTreasury, 1)
			},
			"[potso.rewards] TreasuryAddress",
		},
		"subscriptions treasury": {
			func(s string) string {
				return strings.Replace(s, "Treasury = \""+liveAdmin, "Treasury = \""+oldAdmin, 1)
			},
			"[subscriptions] Treasury",
		},
		"default fee owner": {
			func(s string) string {
				return strings.Replace(s, "OwnerWallet = \""+liveAdmin, "OwnerWallet = \""+oldAdmin, 1)
			},
			"[global.Fees] OwnerWallet",
		},
		"znhb fee owner": {
			func(s string) string {
				return strings.Replace(s, "OwnerWallet = \""+liveZNHB, "OwnerWallet = \""+oldTreasury, 1)
			},
			"ZNHB OwnerWallet",
		},
		"nhb asset fee owner": {
			func(s string) string {
				i := strings.Index(s, "Asset = \"NHB\"")
				return s[:i] + strings.Replace(s[i:], "OwnerWallet = \""+liveAdmin, "OwnerWallet = \""+oldAdmin, 1)
			},
			"NHB OwnerWallet",
		},
		"not an address": {
			func(s string) string {
				return strings.Replace(s, "TreasuryAddress = \""+liveZNHB+"\"", "TreasuryAddress = \"\"", 1)
			},
			"[potso.rewards] TreasuryAddress",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			bad, err := checkLiveConfig(mutatedConfig(t, tc.edit), repoGenesis)
			if err != nil {
				t.Fatal(err)
			}
			if len(bad) == 0 || !strings.Contains(strings.Join(bad, "\n"), tc.want) {
				t.Fatalf("expected a complaint about %q, got %v", tc.want, bad)
			}
		})
	}
}

// Everything a node operator changes (paths, ports, keys, peers, the network
// name) is not consensus-relevant and must not trip the check.
func TestNodeLocalEditsPassTheConfigCheck(t *testing.T) {
	path := mutatedConfig(t, func(s string) string {
		s = strings.Replace(s, `ListenAddress = "127.0.0.1:6001"`, `ListenAddress = "0.0.0.0:7777"`, 1)
		s = strings.Replace(s, `DataDir = "./nhb-data"`, `DataDir = "/var/lib/nhbchain/nhb-data"`, 1)
		s = strings.Replace(s, `ValidatorKMSEnv = ""`, `ValidatorKMSEnv = "NHB_VALIDATOR_RAW_KEY"`, 1)
		s = strings.Replace(s, `NetworkName = "nhb-local"`, `NetworkName = "nhb-mainnet-validator"`, 1)
		s = strings.Replace(s, `ExternalAddress = ""`, `ExternalAddress = "203.0.113.7:6001"`, 1)
		s = strings.Replace(s, `Bootnodes = []`, `Bootnodes = ["198.51.100.1:6001"]`, 1)
		return s
	})
	bad, err := checkLiveConfig(path, repoGenesis)
	if err != nil || len(bad) > 0 {
		t.Fatalf("a config with only node-local edits was refused: %v %v", bad, err)
	}
}

func TestCheckConfigCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"check-config", "--config", repoConfig, "--genesis", repoGenesis}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "ok") {
		t.Fatalf("the shipped config: exit %d: %s %s", code, out.String(), errOut.String())
	}
	drifted := mutatedConfig(t, func(s string) string {
		return strings.Replace(s, "QuorumCertActivationHeight = 0", "QuorumCertActivationHeight = 12", 1)
	})
	out.Reset()
	errOut.Reset()
	if code := run([]string{"check-config", "--config", drifted, "--genesis", repoGenesis}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "QuorumCertActivationHeight") {
		t.Fatalf("a drifted config: exit %d: %s", code, errOut.String())
	}
	if code := run([]string{"check-config", "--config", filepath.Join(t.TempDir(), "missing.toml"), "--genesis", repoGenesis}, &out, &errOut); code != 1 {
		t.Fatalf("a missing config: exit %d", code)
	}
	if code := run([]string{"check-config", "--config", repoConfig}, &out, &errOut); code != 2 {
		t.Fatalf("without --genesis: exit %d", code)
	}
}
