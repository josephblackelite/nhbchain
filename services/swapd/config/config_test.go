package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdminConfigNormaliseRequiresClientCAForMTLS(t *testing.T) {
	cfg := AdminConfig{
		MTLS: MTLSConfig{
			Enabled: true,
		},
		TLS: AdminTLSConfig{
			CertPath: "cert.pem",
			KeyPath:  "key.pem",
		},
	}

	err := cfg.normalise(false)
	if err == nil {
		t.Fatalf("expected error when mTLS is enabled without client CA")
	}
	if got, want := err.Error(), "mtls.client_ca must be configured when mTLS is enabled"; got != want {
		t.Fatalf("unexpected error: got %q, want %q", got, want)
	}
}

func TestAdminConfigNormaliseAllowsMTLSWithClientCA(t *testing.T) {
	cfg := AdminConfig{
		MTLS: MTLSConfig{
			Enabled:      true,
			ClientCAPath: "ca.pem",
		},
		TLS: AdminTLSConfig{
			CertPath: "cert.pem",
			KeyPath:  "key.pem",
		},
	}

	if err := cfg.normalise(false); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.MTLS.Enabled {
		t.Fatalf("expected mTLS to remain enabled")
	}
	if cfg.MTLS.ClientCAPath != "ca.pem" {
		t.Fatalf("unexpected client CA path: %q", cfg.MTLS.ClientCAPath)
	}
}

func TestAdminConfigNormaliseRequiresTLSEnabledForBearer(t *testing.T) {
	cfg := AdminConfig{
		BearerToken: "secret",
		TLS: AdminTLSConfig{
			Disable: true,
		},
	}

	err := cfg.normalise(false)
	if err == nil {
		t.Fatalf("expected error when bearer token is set without TLS")
	}
	if got, want := err.Error(), "admin bearer_token requires TLS to be enabled"; got != want {
		t.Fatalf("unexpected error: got %q, want %q", got, want)
	}
}

func TestAdminConfigNormaliseAllowsInsecureOverride(t *testing.T) {
	cfg := AdminConfig{
		BearerToken: "secret",
		TLS: AdminTLSConfig{
			Disable: true,
		},
	}

	if err := cfg.normalise(true); err != nil {
		t.Fatalf("expected insecure override to bypass TLS requirement, got %v", err)
	}
	if cfg.BearerToken != "secret" {
		t.Fatalf("expected bearer token to persist, got %q", cfg.BearerToken)
	}
}

func TestValidateRequiresTLSWhenStableEnabled(t *testing.T) {
	cfg := Config{
		Pairs:   []Pair{{Base: "ZNHB", Quote: "USD"}},
		Sources: []Source{{Name: "oracle", Type: "mock"}},
		Stable: StableConfig{
			Paused: false,
			Assets: []StableAsset{{Symbol: "ZNHB"}},
			Partners: []StablePartner{{
				ID:     "desk-1",
				APIKey: "api-key",
				Secret: "secret",
			}},
		},
		Admin: AdminConfig{TLS: AdminTLSConfig{Disable: true}},
	}

	err := validate(cfg)
	if err == nil {
		t.Fatalf("expected error when stable runtime is enabled without TLS")
	}
	if got, want := err.Error(), "stable runtime requires admin TLS to be enabled"; got != want {
		t.Fatalf("unexpected error: got %q want %q", got, want)
	}
}

func TestValidateRequiresPartners(t *testing.T) {
	cfg := Config{
		Pairs:   []Pair{{Base: "ZNHB", Quote: "USD"}},
		Sources: []Source{{Name: "oracle", Type: "mock"}},
		Stable: StableConfig{
			Paused: false,
			Assets: []StableAsset{{Symbol: "ZNHB"}},
		},
		Admin: AdminConfig{TLS: AdminTLSConfig{Disable: false, CertPath: "cert", KeyPath: "key"}},
	}

	err := validate(cfg)
	if err == nil {
		t.Fatalf("expected error when partners not configured")
	}
	if got, want := err.Error(), "stable partners must be configured when stable engine is enabled"; got != want {
		t.Fatalf("unexpected error: got %q want %q", got, want)
	}
}

func baseStableConfig() Config {
	return Config{
		Pairs:   []Pair{{Base: "ZNHB", Quote: "USD"}},
		Sources: []Source{{Name: "oracle", Type: "mock"}},
		Stable: StableConfig{
			Paused: false,
			Assets: []StableAsset{{Symbol: "ZNHB"}},
			Partners: []StablePartner{{
				ID: "desk-1", APIKey: "api-key", Secret: "secret",
			}},
		},
		Admin: AdminConfig{TLS: AdminTLSConfig{Disable: false, CertPath: "cert", KeyPath: "key"}},
	}
}

func TestValidateRejectsUnknownSettlementRail(t *testing.T) {
	cfg := baseStableConfig()
	cfg.Stable.Settlement.DefaultRail = "bitcoin_atm"
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error for unknown default_rail")
	}

	cfg2 := baseStableConfig()
	cfg2.Stable.Partners[0].SettlementRail = "carrier_pigeon"
	if err := validate(cfg2); err == nil {
		t.Fatalf("expected error for unknown partner settlement_rail")
	}
}

func TestValidateRequiresNowPaymentsCredentialsWhenRailUsed(t *testing.T) {
	cfg := baseStableConfig()
	cfg.Stable.Settlement.DefaultRail = "nowpayments"
	err := validate(cfg)
	if err == nil {
		t.Fatalf("expected error when nowpayments rail used without credentials")
	}

	cfg.Stable.Settlement.NowPayments = NowPaymentsSettlementConfig{
		Email: "ops@example.com", Password: "secret", APIKey: "key",
	}
	if err := validate(cfg); err != nil {
		t.Fatalf("unexpected error once credentials configured: %v", err)
	}
}

func TestValidateRequiresNowPaymentsCredentialsForPartnerOverrideOnly(t *testing.T) {
	cfg := baseStableConfig()
	// Default stays manual_treasury; only this partner opts into nowpayments.
	cfg.Stable.Partners[0].SettlementRail = "nowpayments"
	if err := validate(cfg); err == nil {
		t.Fatalf("expected error when a partner overrides to nowpayments without credentials configured")
	}
}

func TestValidateAllowsManualTreasuryWithoutNowPaymentsCredentials(t *testing.T) {
	cfg := baseStableConfig()
	cfg.Stable.Settlement.DefaultRail = "manual_treasury"
	if err := validate(cfg); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestNowPaymentsSettlementConfigNormaliseReadsSecretFiles(t *testing.T) {
	dir := t.TempDir()
	emailPath := filepath.Join(dir, "email")
	passwordPath := filepath.Join(dir, "password")
	apiKeyPath := filepath.Join(dir, "api_key")
	if err := os.WriteFile(emailPath, []byte("ops@example.com\n"), 0o600); err != nil {
		t.Fatalf("write email file: %v", err)
	}
	if err := os.WriteFile(passwordPath, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatalf("write password file: %v", err)
	}
	if err := os.WriteFile(apiKeyPath, []byte("api-key-123\n"), 0o600); err != nil {
		t.Fatalf("write api key file: %v", err)
	}

	cfg := NowPaymentsSettlementConfig{EmailFile: emailPath, PasswordFile: passwordPath, APIKeyFile: apiKeyPath}
	if err := cfg.normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if cfg.Email != "ops@example.com" || cfg.Password != "s3cret" || cfg.APIKey != "api-key-123" {
		t.Fatalf("unexpected normalised config: %+v", cfg)
	}
}

func TestNowPaymentsSettlementConfigNormaliseFailsOnMissingFile(t *testing.T) {
	cfg := NowPaymentsSettlementConfig{EmailFile: "/nonexistent/path/email"}
	if err := cfg.normalise(); err == nil {
		t.Fatalf("expected error for unreadable email_file")
	}
}

func TestSourceNormaliseReadsAPIKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "api_key")
	if err := os.WriteFile(keyPath, []byte("real-key-value\n"), 0o600); err != nil {
		t.Fatalf("write api key file: %v", err)
	}
	src := Source{Name: "now", APIKeyFile: keyPath}
	if err := src.normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if src.APIKey != "real-key-value" {
		t.Fatalf("expected api key loaded from file, got %q", src.APIKey)
	}
}

func TestSourceNormaliseLeavesInlineAPIKeyUntouchedWhenNoFile(t *testing.T) {
	src := Source{Name: "gecko", APIKey: "inline-key"}
	if err := src.normalise(); err != nil {
		t.Fatalf("normalise: %v", err)
	}
	if src.APIKey != "inline-key" {
		t.Fatalf("expected inline api key preserved, got %q", src.APIKey)
	}
}

func TestSourceNormaliseFailsOnMissingFile(t *testing.T) {
	src := Source{Name: "now", APIKeyFile: "/nonexistent/path/key"}
	if err := src.normalise(); err == nil {
		t.Fatalf("expected error for unreadable api_key_file")
	}
}

func basePriceProofConfig() PriceProofConfig {
	return PriceProofConfig{
		Enabled:  true,
		Provider: "nowpayments",
		Pairs:    []string{"ZNHB/USD"},
		Signer: PriceProofSignerConfig{
			BaseURL:        "https://hsm.internal:8443",
			KeyLabel:       "swapd-price-signer",
			CACertPath:     "ca.pem",
			ClientCertPath: "client.pem",
			ClientKeyPath:  "client.key",
		},
		Partners: []StablePartner{{ID: "otc-gateway", APIKey: "key-1", Secret: "secret-1"}},
	}
}

func TestValidatePriceProofDisabledIsNoop(t *testing.T) {
	if err := validatePriceProof(PriceProofConfig{}); err != nil {
		t.Fatalf("expected no error when price proof disabled, got %v", err)
	}
}

func TestValidatePriceProofAcceptsCompleteConfig(t *testing.T) {
	if err := validatePriceProof(basePriceProofConfig()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidatePriceProofRequiresProvider(t *testing.T) {
	cfg := basePriceProofConfig()
	cfg.Provider = ""
	if err := validatePriceProof(cfg); err == nil {
		t.Fatalf("expected error for missing provider")
	}
}

func TestValidatePriceProofRequiresPairs(t *testing.T) {
	cfg := basePriceProofConfig()
	cfg.Pairs = nil
	if err := validatePriceProof(cfg); err == nil {
		t.Fatalf("expected error for missing pairs")
	}
}

func TestValidatePriceProofRejectsMalformedPair(t *testing.T) {
	cfg := basePriceProofConfig()
	cfg.Pairs = []string{"ZNHBUSD"}
	if err := validatePriceProof(cfg); err == nil {
		t.Fatalf("expected error for pair missing separator")
	}
}

func TestValidatePriceProofRequiresSignerFields(t *testing.T) {
	cases := []func(*PriceProofConfig){
		func(c *PriceProofConfig) { c.Signer.BaseURL = "" },
		func(c *PriceProofConfig) { c.Signer.KeyLabel = "" },
		func(c *PriceProofConfig) { c.Signer.CACertPath = "" },
		func(c *PriceProofConfig) { c.Signer.ClientCertPath = "" },
		func(c *PriceProofConfig) { c.Signer.ClientKeyPath = "" },
	}
	for i, mutate := range cases {
		cfg := basePriceProofConfig()
		mutate(&cfg)
		if err := validatePriceProof(cfg); err == nil {
			t.Fatalf("case %d: expected error for incomplete signer config", i)
		}
	}
}

func TestValidatePriceProofRequiresPartners(t *testing.T) {
	cfg := basePriceProofConfig()
	cfg.Partners = nil
	if err := validatePriceProof(cfg); err == nil {
		t.Fatalf("expected error for missing partners")
	}
}

func TestValidatePriceProofRejectsDuplicatePartnerCredentials(t *testing.T) {
	cfg := basePriceProofConfig()
	cfg.Partners = append(cfg.Partners, StablePartner{ID: "otc-gateway", APIKey: "key-2", Secret: "secret-2"})
	if err := validatePriceProof(cfg); err == nil {
		t.Fatalf("expected error for duplicate partner id")
	}

	cfg = basePriceProofConfig()
	cfg.Partners = append(cfg.Partners, StablePartner{ID: "other", APIKey: "key-1", Secret: "secret-2"})
	if err := validatePriceProof(cfg); err == nil {
		t.Fatalf("expected error for duplicate partner api_key")
	}
}

func TestValidateRunsPriceProofChecksEvenWhenStablePaused(t *testing.T) {
	cfg := Config{
		Pairs:      []Pair{{Base: "ZNHB", Quote: "USD"}},
		Sources:    []Source{{Name: "oracle", Type: "mock"}},
		Stable:     StableConfig{Paused: true},
		PriceProof: basePriceProofConfig(),
	}
	cfg.PriceProof.Provider = ""
	if err := validate(cfg); err == nil {
		t.Fatalf("expected price proof validation error even though stable engine is paused")
	}
}

func TestApplyDefaultsSetsDefaultPriceProofPair(t *testing.T) {
	cfg := &Config{PriceProof: PriceProofConfig{Enabled: true}}
	applyDefaults(cfg)
	if len(cfg.PriceProof.Pairs) != 1 || cfg.PriceProof.Pairs[0] != "ZNHB/USD" {
		t.Fatalf("expected default pair ZNHB/USD, got %#v", cfg.PriceProof.Pairs)
	}
}
