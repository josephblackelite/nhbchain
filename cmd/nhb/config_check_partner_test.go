package main

import (
	"strings"
	"testing"
)

// A partner key with no PartnerProviders entry reads no swap voucher (PL-EXT-32).
// That is the safe default, but an operator who has just added the key would
// otherwise see an empty ledger and no word of why, so each such key is named.
func TestCheckConfigurationNamesAPartnerKeyThatReadsNothing(t *testing.T) {
	cfg := loadShippedConfig(t)
	cfg.RPCSwapAuth.Secrets = map[string]string{"alpha": "s1-secret", "beta": "s2-secret", "gamma": "s3-secret"}
	cfg.RPCSwapAuth.PartnerProviders = map[string][]string{"alpha": {"alpha-pay"}, "gamma": {"*"}}
	logger, buf := capturedLogger()
	found := checkConfiguration(logger, cfg)
	got := warnings(t, buf)
	if found != 1 || len(got) != 1 {
		t.Fatalf("found %d problems and %d warnings, want 1 of each: %s", found, len(got), buf.String())
	}
	problem, _ := got[0]["problem"].(string)
	if !strings.Contains(problem, `"beta"`) || strings.Contains(problem, `"alpha"`) || strings.Contains(problem, `"gamma"`) {
		t.Fatalf("the warning should name beta only: %q", problem)
	}
	if strings.Contains(buf.String(), "secret") {
		t.Fatalf("the warning must not print a secret: %s", buf.String())
	}
}

func TestCheckConfigurationIsQuietWhenEveryPartnerKeyHasProviders(t *testing.T) {
	cfg := loadShippedConfig(t)
	cfg.RPCSwapAuth.Secrets = map[string]string{"alpha": "s1-secret"}
	cfg.RPCSwapAuth.PartnerProviders = map[string][]string{"alpha": {"alpha-pay"}}
	logger, buf := capturedLogger()
	if found := checkConfiguration(logger, cfg); found != 0 {
		t.Fatalf("%d problems for a fully mapped partner key: %s", found, buf.String())
	}
}

// Keys written with stray space in the file are matched the way the node
// matches them when it signs in a partner (trimmed).
func TestCheckConfigurationMatchesPartnerKeysTrimmed(t *testing.T) {
	cfg := loadShippedConfig(t)
	cfg.RPCSwapAuth.Secrets = map[string]string{" alpha ": "s1-secret"}
	cfg.RPCSwapAuth.PartnerProviders = map[string][]string{"alpha": {"alpha-pay"}}
	logger, buf := capturedLogger()
	if found := checkConfiguration(logger, cfg); found != 0 {
		t.Fatalf("%d problems for a key that differs only by space: %s", found, buf.String())
	}
}
