package config

import "testing"

func withEnv(t *testing.T, vars map[string]string) {
	t.Helper()
	for k, v := range vars {
		t.Setenv(k, v)
	}
}

func validEnv() map[string]string {
	return map[string]string{
		envRPCURL:                 "https://127.0.0.1:8081",
		envRPCBearerToken:         "secret-token",
		envThreshold:              "2",
		envKeystorePaths:          "/keys/one.json,/keys/two.json",
		envKeystorePassphraseEnvs: "SIGNER1_PASS,SIGNER2_PASS",
		envOraclePriority:         "manual",
		envManualRate:             "0.05",
	}
}

func TestLoadFromEnv_DefaultsApplied(t *testing.T) {
	withEnv(t, validEnv())
	cfg := LoadFromEnv()
	if cfg.Pair != defaultPair {
		t.Fatalf("pair = %q, want default %q", cfg.Pair, defaultPair)
	}
	if cfg.RPCTimeout.Seconds() != defaultRPCTimeoutSeconds {
		t.Fatalf("rpc timeout = %v, want %ds", cfg.RPCTimeout, defaultRPCTimeoutSeconds)
	}
	if len(cfg.Signers) != 2 {
		t.Fatalf("expected 2 signers, got %d", len(cfg.Signers))
	}
	if cfg.Signers[0].KeystorePath != "/keys/one.json" || cfg.Signers[0].PassphraseEnv != "SIGNER1_PASS" {
		t.Fatalf("unexpected signer[0]: %+v", cfg.Signers[0])
	}
	if cfg.Signers[1].KeystorePath != "/keys/two.json" || cfg.Signers[1].PassphraseEnv != "SIGNER2_PASS" {
		t.Fatalf("unexpected signer[1]: %+v", cfg.Signers[1])
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestValidate_RejectsMissingRPCURL(t *testing.T) {
	// LoadFromEnv always applies a default RPC URL when the env var is
	// unset or empty, so this constructs a Config directly to exercise
	// Validate's own defense against a caller that builds one by hand.
	withEnv(t, validEnv())
	cfg := LoadFromEnv()
	cfg.RPCBaseURL = ""
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected an error for empty RPC URL")
	}
}

func TestValidate_RejectsMissingBearerToken(t *testing.T) {
	env := validEnv()
	env[envRPCBearerToken] = ""
	withEnv(t, env)
	cfg := LoadFromEnv()
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected an error for empty bearer token")
	}
}

func TestValidate_RejectsFewerSignersThanThreshold(t *testing.T) {
	env := validEnv()
	env[envThreshold] = "3"
	withEnv(t, env)
	cfg := LoadFromEnv()
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected an error when fewer signers than threshold are configured")
	}
}

func TestValidate_RejectsSignerMissingPassphraseEnv(t *testing.T) {
	env := validEnv()
	env[envKeystorePassphraseEnvs] = "SIGNER1_PASS"
	withEnv(t, env)
	cfg := LoadFromEnv()
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected an error when a keystore has no matching passphrase env var")
	}
}

func TestValidate_RejectsManualPriorityWithoutManualRate(t *testing.T) {
	env := validEnv()
	env[envManualRate] = ""
	withEnv(t, env)
	cfg := LoadFromEnv()
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected an error when \"manual\" is prioritized but no manual rate is set")
	}
}

func TestValidate_RejectsInvalidPair(t *testing.T) {
	env := validEnv()
	env[envPair] = "ZNHB-USD"
	withEnv(t, env)
	cfg := LoadFromEnv()
	if err := cfg.Validate(); err == nil {
		t.Fatalf("expected an error for a malformed pair")
	}
}

// TestLoadFromEnv_DefaultOraclePriorityWithoutNHBPortal confirms the
// pre-existing default order (manual, nowpayments, coingecko) is unchanged
// when BUYBACKD_ORACLE_PRIORITY and BUYBACKD_NHBPORTAL_ORACLE_URL are both
// unset -- the common case, and the one the nhbportal-aware default must
// never alter.
func TestLoadFromEnv_DefaultOraclePriorityWithoutNHBPortal(t *testing.T) {
	env := validEnv()
	delete(env, envOraclePriority)
	withEnv(t, env)
	cfg := LoadFromEnv()
	want := []string{"manual", "nowpayments", "coingecko"}
	if !equalStringSlices(cfg.OraclePriority, want) {
		t.Fatalf("oracle priority = %v, want %v", cfg.OraclePriority, want)
	}
}

// TestLoadFromEnv_DefaultOraclePriorityPutsNHBPortalFirstWhenConfigured is
// the regression test for the real finding: buildQuoteSource only ever
// registers "nhbportal" with the oracle aggregator when
// BUYBACKD_NHBPORTAL_ORACLE_URL is set, but the default OraclePriority list
// never included "nhbportal" at all, so the aggregator would never actually
// consult it even when configured as the intended primary source. When the
// URL is set and the operator has not explicitly overridden
// BUYBACKD_ORACLE_PRIORITY, "nhbportal" must lead the default order.
func TestLoadFromEnv_DefaultOraclePriorityPutsNHBPortalFirstWhenConfigured(t *testing.T) {
	env := validEnv()
	delete(env, envOraclePriority)
	env[envNHBPortalOracleURL] = "https://portal.example.com/admin/finance/znhb-rate"
	withEnv(t, env)
	cfg := LoadFromEnv()
	want := []string{"nhbportal", "manual", "nowpayments", "coingecko"}
	if !equalStringSlices(cfg.OraclePriority, want) {
		t.Fatalf("oracle priority = %v, want %v", cfg.OraclePriority, want)
	}
}

// TestLoadFromEnv_ExplicitOraclePriorityOverridesNHBPortalDefault confirms
// an operator's own explicit BUYBACKD_ORACLE_PRIORITY always wins over the
// nhbportal-aware default, even when BUYBACKD_NHBPORTAL_ORACLE_URL is also
// set.
func TestLoadFromEnv_ExplicitOraclePriorityOverridesNHBPortalDefault(t *testing.T) {
	env := validEnv()
	env[envOraclePriority] = "coingecko,manual"
	env[envNHBPortalOracleURL] = "https://portal.example.com/admin/finance/znhb-rate"
	withEnv(t, env)
	cfg := LoadFromEnv()
	want := []string{"coingecko", "manual"}
	if !equalStringSlices(cfg.OraclePriority, want) {
		t.Fatalf("oracle priority = %v, want %v (explicit operator override must not be reordered)", cfg.OraclePriority, want)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestSanitized_MasksBearerToken(t *testing.T) {
	withEnv(t, validEnv())
	cfg := LoadFromEnv()
	sanitized := cfg.Sanitized()
	if sanitized.RPCBearerToken != "***" {
		t.Fatalf("expected masked bearer token, got %q", sanitized.RPCBearerToken)
	}
	if cfg.RPCBearerToken == "***" {
		t.Fatalf("original config should not be mutated by Sanitized")
	}
}
