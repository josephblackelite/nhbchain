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

	err := cfg.normalise()
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

	if err := cfg.normalise(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.MTLS.Enabled {
		t.Fatalf("expected mTLS to remain enabled")
	}
	if cfg.MTLS.ClientCAPath != "ca.pem" {
		t.Fatalf("unexpected client CA path: %q", cfg.MTLS.ClientCAPath)
	}
}
