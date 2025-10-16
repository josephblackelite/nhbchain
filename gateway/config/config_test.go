package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestLoadDefaultsAllowAnonymousDisabledByDefault(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Auth.AllowAnonymous {
		t.Fatalf("expected auth.allowAnonymous to default to false")
	}
}

func TestLoadDefaultsAllowAnonymousDisabledWhenAuthEnabled(t *testing.T) {
	path := writeConfig(t, "auth:\n  enabled: true\n")
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Auth.AllowAnonymous {
		t.Fatalf("expected auth.allowAnonymous to default to false when auth.enabled is true")
	}
}

func TestLoadRequiresOptionalPathsWhenAllowAnonymousEnabled(t *testing.T) {
	path := writeConfig(t, "auth:\n  enabled: true\n  allowAnonymous: true\n")
	if _, err := Load(path); err == nil {
		t.Fatalf("expected load to fail when auth.allowAnonymous is true without optional paths")
	}
}

func TestLoadRequiresAuthEnabledForSensitiveTLSConfig(t *testing.T) {
	yaml := "security:\n  tlsCertFile: /etc/gateway/cert.pem\n  tlsKeyFile: /etc/gateway/key.pem\n"
	path := writeConfig(t, yaml)
	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected load to fail when auth.enabled is omitted for sensitive TLS configuration")
	}
	if !strings.Contains(err.Error(), "auth.enabled must be explicitly set") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadAllowsExplicitAuthDisabledForSensitiveTLSConfig(t *testing.T) {
	yaml := "auth:\n  enabled: false\nsecurity:\n  tlsCertFile: /etc/gateway/cert.pem\n  tlsKeyFile: /etc/gateway/key.pem\n"
	path := writeConfig(t, yaml)
	if _, err := Load(path); err != nil {
		t.Fatalf("load config: %v", err)
	}
}

func TestLoadRequiresAuthEnabledForAutoUpgrade(t *testing.T) {
	yaml := "security:\n  autoUpgradeHTTP: true\n"
	path := writeConfig(t, yaml)
	if _, err := Load(path); err == nil {
		t.Fatalf("expected load to fail when auth.enabled is omitted with auto HTTPS enabled")
	}
}

func TestLoadNormalizesOptionalPaths(t *testing.T) {
	yaml := "auth:\n  enabled: true\n  allowAnonymous: true\n  optionalPaths:\n    - /v1/lending/markets\n    - \"   /v1/lending/markets/get   \"\n"
	path := writeConfig(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	expected := []string{"/v1/lending/markets", "/v1/lending/markets/get"}
	if len(cfg.Auth.OptionalPaths) != len(expected) {
		t.Fatalf("expected %d optional paths, got %d", len(expected), len(cfg.Auth.OptionalPaths))
	}
	for i, path := range expected {
		if cfg.Auth.OptionalPaths[i] != path {
			t.Fatalf("optional path %d mismatch: expected %q, got %q", i, path, cfg.Auth.OptionalPaths[i])
		}
	}
}

func TestLoadRejectsOptionalPathsWithoutLeadingSlash(t *testing.T) {
	yaml := "auth:\n  enabled: true\n  allowAnonymous: true\n  optionalPaths:\n    - v1/lending/markets\n"
	path := writeConfig(t, yaml)
	if _, err := Load(path); err == nil {
		t.Fatalf("expected validation error for optional path without leading slash")
	}
}
