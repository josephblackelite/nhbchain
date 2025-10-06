package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadConfigDefaults(t *testing.T) {
	path := writeConfig(t, `
listen: " :6000 "
tls:
  allow_insecure: true
auth:
  api_tokens:
    - " token-one "
    - " "
    - "token-two"
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.ListenAddress != ":6000" {
		t.Fatalf("unexpected listen address: %q", cfg.ListenAddress)
	}
	if !cfg.TLS.AllowInsecure {
		t.Fatalf("expected allow_insecure to propagate")
	}
	if len(cfg.Auth.APITokens) != 2 {
		t.Fatalf("expected 2 trimmed api tokens, got %d", len(cfg.Auth.APITokens))
	}
}

func TestLoadConfigRequiresAuthenticators(t *testing.T) {
	path := writeConfig(t, `
listen: ":50053"
tls:
  cert: "server.crt"
  key: "server.key"
auth: {}
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error when no authenticators are configured")
	}
}

func TestLoadConfigValidatesTLS(t *testing.T) {
	path := writeConfig(t, `
listen: ":50053"
tls:
  cert: "server.crt"
auth:
  api_tokens:
    - token
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error when tls key is missing")
	}
}

func TestLoadConfigValidatesMTLSDependencies(t *testing.T) {
	path := writeConfig(t, `
listen: ":50053"
tls:
  cert: "server.crt"
  key: "server.key"
auth:
  mtls:
    allowed_common_names: [client]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error when mtls is configured without api tokens or client ca")
	}
}

func TestLoadConfigRequiresTLSMaterialUnlessInsecure(t *testing.T) {
	path := writeConfig(t, `
listen: ":50053"
auth:
  api_tokens: [token]
`)
	if _, err := Load(path); err == nil {
		t.Fatal("expected error when tls material missing without allow_insecure")
	}
}
