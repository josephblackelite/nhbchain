package config

import (
	"net"
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
node_rpc_url: " https://127.0.0.1:8081 "
shared_secret_header: " X-NHB-Shared-Secret "
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
	if cfg.NodeRPCURL != "https://127.0.0.1:8081" {
		t.Fatalf("unexpected node rpc url: %q", cfg.NodeRPCURL)
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

func TestLoadConfigDefaultsNodeRPCURLWhenBlank(t *testing.T) {
	path := writeConfig(t, `
listen: ":50053"
node_rpc_url: " "
tls:
  allow_insecure: true
auth:
  api_tokens: [token]
`)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.NodeRPCURL != "https://127.0.0.1:8081" {
		t.Fatalf("expected default node rpc url, got %q", cfg.NodeRPCURL)
	}
}

// The configuration the daemon reads when it is started without flags must pass
// its own checks and bind a loopback address, so that the default start works
// without NHB_ENV=dev (PL-DC-42). It carries a placeholder token that is only
// meaningful on a loopback listener.
func TestShippedDefaultConfigLoadsAndListensOnLoopbackOnly(t *testing.T) {
	cfg, err := Load("../../lending/config.yaml")
	if err != nil {
		t.Fatalf("the shipped default configuration does not load: %v", err)
	}
	host, _, err := net.SplitHostPort(cfg.ListenAddress)
	if err != nil {
		t.Fatalf("listen %q: %v", cfg.ListenAddress, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("the shipped default must listen on a loopback address, got %q", cfg.ListenAddress)
	}
	if !cfg.TLS.AllowInsecure {
		t.Fatalf("the shipped default is plaintext on loopback and must say so")
	}
}
