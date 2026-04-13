package config

import (
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
