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

// liveRewardsByStakeFromEpoch is the first epoch whose emission the live validators pay by stake (config RewardsByStakeFromEpoch); 0 means they still pay the
// earlier split. A node that starts from a snapshot executes every later block itself, so it must carry the same value: with another it would compute other
// reward payouts at an epoch boundary, a different state root, and stop following the network. Change it here, in config.toml and in the tests together, in
// the release that changes the live value.
const liveRewardsByStakeFromEpoch uint64 = 0

// liveChainRules are the other settings that decide what a block leaves behind or
// whether a transaction is valid, each an activation height that is off (0) on the
// live chain. A node that starts from a snapshot executes every later block itself,
// so one set to another value computes another state root at the first block it
// applies to and stops following the network. A key left out of the file is the
// default, 0. Change a value here, in config.toml and in the tests together, in the
// release that changes the live value.
var liveChainRules = []struct {
	table string // "" for a top-level key
	key   string
	live  uint64
}{
	{"", "POSSweepSkipActivationHeight", 0},
	{"", "POSHoldUnstakeGuardActivationHeight", 0},
	{"", "GovCreditPreservationActivationHeight", 0},
	{"", "FeeRouteAliasActivationHeight", 0},
	{"", "LendingFixedTermIDActivationHeight", 0},
	{"swap", "PayoutReceiptActivationHeight", 0},
}

// A node that starts from a snapshot executes every later block itself, so the
// consensus-relevant values of its config must be the ones the live validators
// run with. Most of them are addresses of the network's treasury, which the
// genesis file names; the check below reads them from there instead of
// repeating them, so a config can only pass by agreeing with the genesis.
//
// liveConfigView is the part of config.toml the check reads.
type liveConfigView struct {
	QuorumCertActivationHeight uint64 `toml:"QuorumCertActivationHeight"`
	RewardsByStakeFromEpoch    uint64 `toml:"RewardsByStakeFromEpoch"`
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
	text := string(bareNetworkID.ReplaceAll(raw, []byte(`$1"$2"$3`)))
	if _, err := toml.Decode(text, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", configPath, err)
	}
	var generic map[string]any
	if _, err := toml.Decode(text, &generic); err != nil {
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
	if cfg.RewardsByStakeFromEpoch != liveRewardsByStakeFromEpoch {
		bad = append(bad, fmt.Sprintf("RewardsByStakeFromEpoch is %d, the live validators run with %d", cfg.RewardsByStakeFromEpoch, liveRewardsByStakeFromEpoch))
	}
	for _, rule := range liveChainRules {
		scope := generic
		label := rule.key
		if rule.table != "" {
			scope, _ = generic[rule.table].(map[string]any)
			label = "[" + rule.table + "] " + rule.key
		}
		got := uint64(0)
		switch v := scope[rule.key].(type) {
		case nil:
		case int64:
			got = uint64(v)
		default:
			bad = append(bad, fmt.Sprintf("%s is %v, not a whole number", label, v))
			continue
		}
		if got != rule.live {
			bad = append(bad, fmt.Sprintf("%s is %d, the live validators run with %d", label, got, rule.live))
		}
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
