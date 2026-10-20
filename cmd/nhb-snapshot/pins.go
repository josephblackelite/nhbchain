package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"

	"nhbchain/core/genesis"
	"nhbchain/crypto"
)

// liveQuorumCertActivationHeight is the height above which both live validators require a quorum certificate on every block that reaches them through peer
// sync (config QuorumCertActivationHeight). It is the same on every validator, so a node that follows the live network carries the same value: with 0 it would
// commit synced blocks without ever checking who signed them. Change it here, in config.toml and in the tests together, in the release that changes the live value.
const liveQuorumCertActivationHeight uint64 = 1380200

// A node that starts from a snapshot executes every later block itself, so the
// consensus-relevant values of its config must be the ones the live validators
// run with. Most of them are addresses of the network's treasury, which the
// genesis file names; the check below reads them from there instead of
// repeating them, so a config can only pass by agreeing with the genesis.
//
// liveConfigView is the part of config.toml the check reads.
type liveConfigView struct {
	QuorumCertActivationHeight uint64 `toml:"QuorumCertActivationHeight"`
	Potso                      struct {
		Rewards struct {
			TreasuryAddress string `toml:"TreasuryAddress"`
		} `toml:"rewards"`
	} `toml:"potso"`
	Subscriptions struct {
		Treasury string `toml:"Treasury"`
	} `toml:"subscriptions"`
	Global struct {
		Fees struct {
			OwnerWallet string `toml:"OwnerWallet"`
			Assets      []struct {
				Asset       string `toml:"Asset"`
				OwnerWallet string `toml:"OwnerWallet"`
			} `toml:"Assets"`
		} `toml:"Fees"`
	} `toml:"global"`
}

// The node's config loader quotes a bare NetworkId first (config.Load): a
// chain id above 2^63 is not a TOML integer.
var bareNetworkID = regexp.MustCompile(`(?m)^([ \t]*NetworkId[ \t]*=[ \t]*)(\d+)([ \t]*)$`)

func sameAddress(want, got string) bool {
	a, err := crypto.DecodeAddress(strings.TrimSpace(want))
	if err != nil {
		return false
	}
	b, err := crypto.DecodeAddress(strings.TrimSpace(got))
	if err != nil {
		return false
	}
	return a.Prefix() == b.Prefix() && string(a.Bytes()) == string(b.Bytes())
}

// checkLiveConfig returns what is wrong with the consensus-relevant values of
// the config at configPath, given the genesis file the node is started from.
// An empty result means the config passes. It reads both files and writes
// nothing.
func checkLiveConfig(configPath, genesisPath string) ([]string, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var cfg liveConfigView
	if _, err := toml.Decode(string(bareNetworkID.ReplaceAll(raw, []byte(`$1"$2"$3`))), &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", configPath, err)
	}
	spec, err := genesis.LoadGenesisSpec(genesisPath)
	if err != nil {
		return nil, fmt.Errorf("load genesis %s: %w", genesisPath, err)
	}
	adminBytes, ok := spec.AdminWalletAddress()
	if !ok {
		return nil, fmt.Errorf("genesis %s names no adminWallet", genesisPath)
	}
	admin, err := crypto.NewAddress(crypto.NHBPrefix, adminBytes[:])
	if err != nil {
		return nil, err
	}
	if spec.LoyaltyGlobal == nil || strings.TrimSpace(spec.LoyaltyGlobal.Treasury) == "" {
		return nil, fmt.Errorf("genesis %s names no loyaltyGlobal treasury", genesisPath)
	}
	znhbTreasury := strings.TrimSpace(spec.LoyaltyGlobal.Treasury)

	var bad []string
	expect := func(name, got, want string) {
		if !sameAddress(want, got) {
			bad = append(bad, fmt.Sprintf("%s is %q, the genesis treasury is %s", name, got, want))
		}
	}
	if cfg.QuorumCertActivationHeight != liveQuorumCertActivationHeight {
		bad = append(bad, fmt.Sprintf("QuorumCertActivationHeight is %d, the live validators run with %d", cfg.QuorumCertActivationHeight, liveQuorumCertActivationHeight))
	}
	expect("[global.Fees] OwnerWallet", cfg.Global.Fees.OwnerWallet, admin.String())
	assets := map[string]string{}
	for _, a := range cfg.Global.Fees.Assets {
		assets[strings.ToUpper(strings.TrimSpace(a.Asset))] = a.OwnerWallet
	}
	for asset, want := range map[string]string{"NHB": admin.String(), "ZNHB": znhbTreasury} {
		got, present := assets[asset]
		if !present {
			bad = append(bad, fmt.Sprintf("[[global.Fees.Assets]] has no %s entry", asset))
			continue
		}
		expect("[[global.Fees.Assets]] "+asset+" OwnerWallet", got, want)
	}
	expect("[subscriptions] Treasury", cfg.Subscriptions.Treasury, admin.String())
	expect("[potso.rewards] TreasuryAddress", cfg.Potso.Rewards.TreasuryAddress, znhbTreasury)
	return bad, nil
}
