package config

import (
	"strings"
	"testing"
)

// PL-R5T-64: the automatic top-up refills any address that signs a sponsored
// transfer, and its daily cap is per address. Enabling it needs a role a sponsor must
// hold and a cap over all sponsors together; with it off neither is asked for.
func TestPaymasterAutoTopUpEligibilityIsRequiredToEnableIt(t *testing.T) {
	enabled := func() Global {
		g := defaultGlobalConfig()
		g.Paymaster.AutoTopUp.Enabled = true
		g.Paymaster.AutoTopUp.DailyCapWei = "1000"
		return g
	}

	// Off: nothing is required and nothing is set.
	g := defaultGlobalConfig()
	cfg, err := g.PaymasterAutoTopUpConfig()
	if err != nil {
		t.Fatalf("policy off: %v", err)
	}
	if cfg.SponsorRole != "" || cfg.AggregateDailyCapWei == nil || cfg.AggregateDailyCapWei.Sign() != 0 {
		t.Fatalf("policy off carries eligibility settings: %#v", cfg)
	}

	// On without a role: refused.
	g = enabled()
	g.Paymaster.AutoTopUp.AggregateDailyCapWei = "5000"
	if _, err := g.PaymasterAutoTopUpConfig(); err == nil || !strings.Contains(err.Error(), "SponsorRole") {
		t.Fatalf("enabled without a sponsor role: err = %v, want a SponsorRole error", err)
	}
	g.Paymaster.AutoTopUp.SponsorRole = "   "
	if _, err := g.PaymasterAutoTopUpConfig(); err == nil || !strings.Contains(err.Error(), "SponsorRole") {
		t.Fatalf("enabled with a blank sponsor role: err = %v, want a SponsorRole error", err)
	}

	// On without an aggregate cap: refused, whether unset or zero.
	for _, capValue := range []string{"", "0"} {
		g = enabled()
		g.Paymaster.AutoTopUp.SponsorRole = "ROLE_PAYMASTER_SPONSOR"
		g.Paymaster.AutoTopUp.AggregateDailyCapWei = capValue
		if _, err := g.PaymasterAutoTopUpConfig(); err == nil || !strings.Contains(err.Error(), "AggregateDailyCapWei") {
			t.Fatalf("enabled with aggregate cap %q: err = %v, want an AggregateDailyCapWei error", capValue, err)
		}
	}

	// On with both: accepted and carried (the role trimmed).
	g = enabled()
	g.Paymaster.AutoTopUp.SponsorRole = " ROLE_PAYMASTER_SPONSOR "
	g.Paymaster.AutoTopUp.AggregateDailyCapWei = "5000"
	cfg, err = g.PaymasterAutoTopUpConfig()
	if err != nil {
		t.Fatalf("enabled with both: %v", err)
	}
	if cfg.SponsorRole != "ROLE_PAYMASTER_SPONSOR" || cfg.AggregateDailyCapWei.String() != "5000" {
		t.Fatalf("settings not carried: role %q cap %v", cfg.SponsorRole, cfg.AggregateDailyCapWei)
	}

	// A value that is not a number is refused even with the policy off.
	g = defaultGlobalConfig()
	g.Paymaster.AutoTopUp.AggregateDailyCapWei = "lots"
	if _, err := g.PaymasterAutoTopUpConfig(); err == nil || !strings.Contains(err.Error(), "AggregateDailyCapWei") {
		t.Fatalf("a non-numeric aggregate cap: err = %v", err)
	}
}
