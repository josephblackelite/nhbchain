package config_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"nhbchain/native/lending"
)

// Every lending chain rule is read from the node's config and handed to the node by
// both entry points, the validator and the split-process consensus daemon. A rule
// that one of them forgets to hand over is silently off on that validator while
// another runs it on, which is a fork at the activation height. The two lists are
// the settings of the lending work (ledger PL-DC-19, PL-R5T-13, 50, 52, 53, PL-EXT-13,
// PL-EXT-16); a setting is added to the list in the commit that introduces it.
var bareNetworkIDLine = regexp.MustCompile(`(?m)^([ 	]*NetworkId[ 	]*=[ 	]*)(\d+)([ 	]*)$`)

var lendingChainRuleSettings = []string{
	"LendingAccountingFixesActivationHeight",
	"LendingPoolLimitsActivationHeight",
	"LendingMaxPools",
	"LendingPoolIDMaxLength",
	"LendingRefPriceGuardsActivationHeight",
	"LendingRefPriceMaxAheadSeconds",
	"LendingRefPriceMaxAgeSeconds",
	"LendingRiskActivationHeight",
	"LendingFeeWithdrawActivationHeight",
}

func TestLendingChainRulesAreHandedToTheNodeByBothEntryPoints(t *testing.T) {
	for _, file := range []string{"cmd/nhb/main.go", "cmd/consensusd/main.go"} {
		src := string(readRepoFile(t, file))
		for _, name := range lendingChainRuleSettings {
			if !strings.Contains(src, "cfg."+name) {
				t.Errorf("%s never reads cfg.%s: the setting would be off on a validator started this way", file, name)
			}
		}
	}
}

// The shipped config names each lending chain rule and leaves it off.
func TestLendingChainRulesAreInTheShippedConfigAndOff(t *testing.T) {
	cfg := string(readRepoFile(t, "config.toml"))
	for _, name := range lendingChainRuleSettings {
		if !strings.Contains(cfg, "\n"+name+" = 0\n") {
			t.Errorf("config.toml does not carry %s = 0", name)
		}
	}
}

// Both entry points build the engine's limits from the one mapping, so the validator
// and the split-process daemon cannot disagree about them (PL-DC-19).
func TestBothEntryPointsBuildTheLendingLimitsFromTheOneMapping(t *testing.T) {
	for _, file := range []string{"cmd/nhb/main.go", "cmd/consensusd/main.go"} {
		src := string(readRepoFile(t, file))
		if !strings.Contains(src, "cfg.Lending.RiskParameters()") {
			t.Errorf("%s does not build the lending risk limits with cfg.Lending.RiskParameters()", file)
		}
		if strings.Contains(src, "lending.RiskParameters{") {
			t.Errorf("%s builds lending risk limits by hand again", file)
		}
	}
}

// The shipped config names every limit and every one is off.
func TestTheShippedConfigNamesNoLendingLimit(t *testing.T) {
	var wrapper struct {
		Lending lending.Config `toml:"lending"`
	}
	// The loader quotes a bare NetworkId first: a chain id above 2^63 is not a TOML integer.
	text := bareNetworkIDLine.ReplaceAllString(string(readRepoFile(t, "config.toml")), "${1}\"${2}\"${3}")
	if _, err := toml.Decode(text, &wrapper); err != nil {
		t.Fatalf("decode config.toml: %v", err)
	}
	wrapper.Lending.EnsureDefaults()
	params, err := wrapper.Lending.RiskParameters()
	if err != nil {
		t.Fatalf("the shipped lending limits do not validate: %v", err)
	}
	if params.LiquidationBonus != 0 || params.CircuitBreakerActive || params.Pauses != (lending.ActionPauses{}) ||
		params.BorrowCaps.PerBlock != nil || params.BorrowCaps.Total != nil || params.BorrowCaps.UtilisationBps != 0 ||
		params.MaxTotalSupply != nil || params.MaxTotalCollateral != nil {
		t.Fatalf("the shipped config turns a lending limit on: %+v", params)
	}
	text = string(readRepoFile(t, "config.toml"))
	for _, key := range []string{"LiquidationBonusBps", "BorrowCapUtilisationBps", "PauseSupply", "PauseBorrow", "PauseRepay", "PauseLiquidate", "CircuitBreakerActive", "BorrowCapPerBlockWei", "MaxTotalSupplyWei", "MaxTotalBorrowWei", "MaxTotalCollateralWei"} {
		if !strings.Contains(text, "\n    "+key+" = ") {
			t.Errorf("config.toml does not name %s under [lending.risk] or [lending.breaker]", key)
		}
	}
}
