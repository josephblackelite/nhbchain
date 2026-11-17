package config

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The settings an operator turns on in the shipped config.toml (see its comments and docs/consensus/invariants.md) must be read by the real loader under the
// names the file gives them. A misspelt or misplaced key would be silently ignored and the setting would stay off on that validator only, so every validator
// would not carry the same value. This sets each of them in the shipped file, loads it and checks what the loader holds.
func TestShippedConfigReadsEveryActivationSettingOperatorsTurnOn(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "config.toml"))
	if err != nil {
		t.Fatalf("read the shipped config.toml: %v", err)
	}
	text := string(raw)

	set := func(key string, value string) {
		t.Helper()
		pattern := regexp.MustCompile(`(?m)^(\s*)` + regexp.QuoteMeta(key) + `\s*=\s*\d+\s*$`)
		if len(pattern.FindAllStringIndex(text, -1)) != 1 {
			t.Fatalf("expected exactly one line setting %s in the shipped config.toml, found %d", key, len(pattern.FindAllStringIndex(text, -1)))
		}
		text = pattern.ReplaceAllString(text, "${1}"+key+" = "+value)
	}
	set("RewardsByStakeFromEpoch", "17300")
	set("POSSweepSkipActivationHeight", "1722000")
	set("POSHoldUnstakeGuardActivationHeight", "1723000")
	set("GovCreditPreservationActivationHeight", "1724000")
	set("FeeRouteAliasActivationHeight", "1725000")
	set("LendingFixedTermIDActivationHeight", "1726000")
	set("PayoutReceiptActivationHeight", "1727000")
	set("RejectAmbiguousParamKeysFromUnix", "1900000000")

	dir := t.TempDir()
	keystorePath := filepath.Join(dir, "validator.keystore")
	text = regexp.MustCompile(`(?m)^ValidatorKeystorePath\s*=.*$`).ReplaceAllString(text, "ValidatorKeystorePath = "+tomlQuoted(keystorePath))
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := Load(path, WithKeystorePassphrase(testKeystorePassphrase))
	if err != nil {
		t.Fatalf("load the shipped config with every setting on: %v", err)
	}

	for name, got := range map[string][2]uint64{
		"RewardsByStakeFromEpoch":                       {cfg.RewardsByStakeFromEpoch, 17300},
		"POSSweepSkipActivationHeight":                  {cfg.POSSweepSkipActivationHeight, 1722000},
		"POSHoldUnstakeGuardActivationHeight":           {cfg.POSHoldUnstakeGuardActivationHeight, 1723000},
		"GovCreditPreservationActivationHeight":         {cfg.GovCreditPreservationActivationHeight, 1724000},
		"FeeRouteAliasActivationHeight":                 {cfg.FeeRouteAliasActivationHeight, 1725000},
		"LendingFixedTermIDActivationHeight":            {cfg.LendingFixedTermIDActivationHeight, 1726000},
		"[swap] PayoutReceiptActivationHeight":          {cfg.Swap.PayoutReceiptActivationHeight, 1727000},
		"[governance] RejectAmbiguousParamKeysFromUnix": {cfg.Governance.RejectAmbiguousParamKeysFromUnix, 1900000000},
	} {
		if got[0] != got[1] {
			t.Errorf("%s: the loader holds %d, the file says %d (a key in the wrong place is ignored)", name, got[0], got[1])
		}
	}
	if cfg.QuorumCertActivationHeight != 1380200 {
		t.Errorf("QuorumCertActivationHeight is %d, the shipped file says 1380200", cfg.QuorumCertActivationHeight)
	}
}
