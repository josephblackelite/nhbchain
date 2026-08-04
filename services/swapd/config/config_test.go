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
