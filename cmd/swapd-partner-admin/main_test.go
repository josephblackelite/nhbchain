package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"nhbchain/services/swapd/config"
)

// starterConfig returns a minimal, valid-shaped config.Config to use as a
// starting fixture. It intentionally does not depend on
// services/swapd/config.yaml -- it's built directly from the config package's
// own types so it always matches the real shape.
func starterConfig() config.Config {
	return config.Config{
		ListenAddress: ":7074",
		DatabasePath:  "/var/data/swapd.sqlite",
		Oracle: config.OracleConfig{
			Interval: config.Duration{Duration: 30 * time.Second},
			MaxAge:   config.Duration{Duration: 2 * time.Minute},
			MinFeeds: 1,
		},
		Sources: []config.Source{
			{Name: "gecko", Type: "coingecko", Endpoint: "https://api.coingecko.com/api/v3/simple/price"},
		},
		Pairs: []config.Pair{
			{Base: "ZNHB", Quote: "USD"},
		},
		Policy: config.PolicyConfig{
			ID:          "default",
			MintLimit:   10,
			RedeemLimit: 5,
			Window:      config.Duration{Duration: time.Hour},
		},
		Stable: config.StableConfig{
			Paused:        true,
			QuoteTTL:      config.Duration{Duration: time.Minute},
			MaxSlippage:   50,
			SoftInventory: 1_000_000,
			Assets: []config.StableAsset{
				{Symbol: "ZNHB", BasePair: "ZNHB", QuotePair: "USD"},
			},
			Partners: []config.StablePartner{
				{
					ID:             "existing-partner",
					APIKey:         "existingapikey1234567890",
					Secret:         "existingsecret1234567890",
					Quota:          config.StablePartnerQuota{Daily: 100},
					SettlementRail: "manual_treasury",
				},
			},
			Settlement: config.SettlementConfig{
				DefaultRail: "manual_treasury",
			},
		},
		Admin: config.AdminConfig{
			BearerToken: "test-token",
			TLS: config.AdminTLSConfig{
				Disable: true,
			},
		},
	}
}

// writeStarterConfig marshals a starter config into a new file under the
// test's temp dir (via the same atomic-write path the CLI itself uses) and
// returns its path.
func writeStarterConfig(t *testing.T, cfg config.Config) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := writeConfigAtomic(path, cfg, 0o644); err != nil {
		t.Fatalf("write starter config: %v", err)
	}
	return path
}

func parseConfig(t *testing.T, path string) config.Config {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	var cfg config.Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("config at %s is not valid YAML / does not match config.Config: %v", path, err)
	}
	return cfg
}

func TestAddCreatesPartnerWithValidCredentials(t *testing.T) {
	path := writeStarterConfig(t, starterConfig())

	var stdout, stderr bytes.Buffer
	code := run([]string{"add", "--config", path, "--id", "new-partner", "--settlement-rail", "nowpayments", "--daily-quota", "250"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("add: exit code = %d, stderr = %s", code, stderr.String())
	}

	out := stdout.String()
	if !strings.Contains(out, "api_key:") || !strings.Contains(out, "secret:") {
		t.Fatalf("add: expected stdout to contain generated api_key/secret banner, got: %s", out)
	}

	cfg := parseConfig(t, path)
	idx := findPartnerIndex(cfg.Stable.Partners, "new-partner")
	if idx < 0 {
		t.Fatalf("add: partner %q not found in rewritten config; partners = %+v", "new-partner", cfg.Stable.Partners)
	}
	p := cfg.Stable.Partners[idx]

	if len(p.APIKey) != 64 {
		t.Errorf("add: expected 64-char hex api_key (32 bytes), got %d chars: %q", len(p.APIKey), p.APIKey)
	}
	if len(p.Secret) != 64 {
		t.Errorf("add: expected 64-char hex secret (32 bytes), got %d chars: %q", len(p.Secret), p.Secret)
	}
	if p.APIKey == p.Secret {
		t.Errorf("add: api_key and secret must not be identical")
	}
	if !isHex(p.APIKey) {
		t.Errorf("add: api_key is not valid hex: %q", p.APIKey)
	}
	if !isHex(p.Secret) {
		t.Errorf("add: secret is not valid hex: %q", p.Secret)
	}
	if p.SettlementRail != "nowpayments" {
		t.Errorf("add: settlement_rail = %q, want %q", p.SettlementRail, "nowpayments")
	}
	if p.Quota.Daily != 250 {
		t.Errorf("add: quota.daily = %v, want 250", p.Quota.Daily)
	}

	// Verify the pre-existing partner is untouched, i.e. this really is a
	// round-trip that preserves everything else in the file.
	existingIdx := findPartnerIndex(cfg.Stable.Partners, "existing-partner")
	if existingIdx < 0 {
		t.Fatalf("add: pre-existing partner disappeared after add")
	}
	if cfg.Stable.Partners[existingIdx].APIKey != "existingapikey1234567890" {
		t.Errorf("add: pre-existing partner api_key was mutated")
	}
	if cfg.ListenAddress != ":7074" {
		t.Errorf("add: unrelated config field ListenAddress was mutated: %q", cfg.ListenAddress)
	}
}

func TestAddFailsOnDuplicateID(t *testing.T) {
	path := writeStarterConfig(t, starterConfig())

	var stdout, stderr bytes.Buffer
	code := run([]string{"add", "--config", path, "--id", "existing-partner"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("add: expected non-zero exit code for duplicate id, got 0; stdout = %s", stdout.String())
	}
	if !strings.Contains(stderr.String(), "already exists") {
		t.Errorf("add: expected stderr to mention duplicate id, got: %s", stderr.String())
	}
	if strings.Contains(stdout.String(), "api_key:") {
		t.Errorf("add: must not print any generated credentials on failure, got: %s", stdout.String())
	}

	// Config file must still be valid and unchanged (one partner only).
	cfg := parseConfig(t, path)
	if len(cfg.Stable.Partners) != 1 {
		t.Errorf("add: expected config to still have exactly 1 partner after failed add, got %d", len(cfg.Stable.Partners))
	}
}

func TestAddRejectsInvalidSettlementRail(t *testing.T) {
	path := writeStarterConfig(t, starterConfig())

	var stdout, stderr bytes.Buffer
	code := run([]string{"add", "--config", path, "--id", "bad-rail-partner", "--settlement-rail", "bogus"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("add: expected non-zero exit code for invalid settlement rail")
	}

	cfg := parseConfig(t, path)
	if findPartnerIndex(cfg.Stable.Partners, "bad-rail-partner") >= 0 {
		t.Errorf("add: partner should not have been added when settlement rail is invalid")
	}
}

func TestListRedactsSecrets(t *testing.T) {
	starter := starterConfig()
	path := writeStarterConfig(t, starter)

	// Add a partner first so we know the exact generated secret to check
	// against the list output.
	var addOut, addErr bytes.Buffer
	if code := run([]string{"add", "--config", path, "--id", "listed-partner"}, &addOut, &addErr); code != 0 {
		t.Fatalf("add: exit code = %d, stderr = %s", code, addErr.String())
	}
	cfg := parseConfig(t, path)
	idx := findPartnerIndex(cfg.Stable.Partners, "listed-partner")
	if idx < 0 {
		t.Fatalf("setup: added partner not found")
	}
	newAPIKey := cfg.Stable.Partners[idx].APIKey
	newSecret := cfg.Stable.Partners[idx].Secret

	var stdout, stderr bytes.Buffer
	code := run([]string{"list", "--config", path}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("list: exit code = %d, stderr = %s", code, stderr.String())
	}

	out := stdout.String()

	// The raw secret must never appear anywhere in list output, for either
	// partner.
	if strings.Contains(out, newSecret) {
		t.Errorf("list: raw secret leaked into output: %s", out)
	}
	if strings.Contains(out, starter.Stable.Partners[0].Secret) {
		t.Errorf("list: raw secret of pre-existing partner leaked into output: %s", out)
	}
	// The raw full api_key must never appear unredacted either.
	if strings.Contains(out, newAPIKey) {
		t.Errorf("list: raw full api_key leaked into output unredacted: %s", out)
	}
	if !strings.Contains(out, "existing-partner") || !strings.Contains(out, "listed-partner") {
		t.Errorf("list: expected both partner ids in output, got: %s", out)
	}
	if !strings.Contains(out, redactAPIKey(newAPIKey)) {
		t.Errorf("list: expected redacted api_key %q in output, got: %s", redactAPIKey(newAPIKey), out)
	}
	if !strings.Contains(out, "(default)") {
		t.Errorf("list: expected settlement_rail default placeholder for partner without explicit rail, got: %s", out)
	}
}

func TestRotateChangesSecretNotAPIKey(t *testing.T) {
	starter := starterConfig()
	path := writeStarterConfig(t, starter)
	originalAPIKey := starter.Stable.Partners[0].APIKey
	originalSecret := starter.Stable.Partners[0].Secret

	var stdout, stderr bytes.Buffer
	code := run([]string{"rotate", "--config", path, "--id", "existing-partner"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("rotate: exit code = %d, stderr = %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "secret:") {
		t.Errorf("rotate: expected stdout to contain new secret banner, got: %s", stdout.String())
	}

	cfg := parseConfig(t, path)
	idx := findPartnerIndex(cfg.Stable.Partners, "existing-partner")
	if idx < 0 {
		t.Fatalf("rotate: partner disappeared after rotate")
	}
	p := cfg.Stable.Partners[idx]
	if p.APIKey != originalAPIKey {
		t.Errorf("rotate: api_key changed; want unchanged %q, got %q", originalAPIKey, p.APIKey)
	}
	if p.Secret == originalSecret {
		t.Errorf("rotate: secret was not changed")
	}
	if len(p.Secret) != 64 || !isHex(p.Secret) {
		t.Errorf("rotate: new secret does not look like a 32-byte hex value: %q", p.Secret)
	}
}

func TestRotateFailsOnUnknownID(t *testing.T) {
	path := writeStarterConfig(t, starterConfig())

	var stdout, stderr bytes.Buffer
	code := run([]string{"rotate", "--config", path, "--id", "does-not-exist"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("rotate: expected non-zero exit code for unknown id")
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Errorf("rotate: expected stderr to mention not found, got: %s", stderr.String())
	}

	cfg := parseConfig(t, path)
	if len(cfg.Stable.Partners) != 1 {
		t.Errorf("rotate: partner list should be unchanged after failed rotate, got %d partners", len(cfg.Stable.Partners))
	}
}

func TestRemoveDeletesEntry(t *testing.T) {
	path := writeStarterConfig(t, starterConfig())

	var stdout, stderr bytes.Buffer
	code := run([]string{"remove", "--config", path, "--id", "existing-partner"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("remove: exit code = %d, stderr = %s", code, stderr.String())
	}

	cfg := parseConfig(t, path)
	if findPartnerIndex(cfg.Stable.Partners, "existing-partner") >= 0 {
		t.Errorf("remove: partner still present after remove")
	}
	if len(cfg.Stable.Partners) != 0 {
		t.Errorf("remove: expected 0 partners remaining, got %d", len(cfg.Stable.Partners))
	}
}

func TestRemoveFailsOnUnknownID(t *testing.T) {
	path := writeStarterConfig(t, starterConfig())

	var stdout, stderr bytes.Buffer
	code := run([]string{"remove", "--config", path, "--id", "does-not-exist"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("remove: expected non-zero exit code for unknown id")
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Errorf("remove: expected stderr to mention not found, got: %s", stderr.String())
	}

	cfg := parseConfig(t, path)
	if len(cfg.Stable.Partners) != 1 {
		t.Errorf("remove: partner list should be unchanged after failed remove, got %d partners", len(cfg.Stable.Partners))
	}
}

func TestFileRemainsValidYAMLAfterEveryOperation(t *testing.T) {
	path := writeStarterConfig(t, starterConfig())

	steps := []struct {
		name string
		args []string
	}{
		{"add", []string{"add", "--config", path, "--id", "p1"}},
		{"rotate", []string{"rotate", "--config", path, "--id", "p1"}},
		{"add-second", []string{"add", "--config", path, "--id", "p2", "--settlement-rail", "manual_treasury"}},
		{"remove", []string{"remove", "--config", path, "--id", "p1"}},
		{"remove-second", []string{"remove", "--config", path, "--id", "p2"}},
	}

	for _, step := range steps {
		var stdout, stderr bytes.Buffer
		code := run(step.args, &stdout, &stderr)
		if code != 0 {
			t.Fatalf("%s: exit code = %d, stderr = %s", step.name, code, stderr.String())
		}
		// Must remain parseable as config.Config after every single step.
		_ = parseConfig(t, path)
		if !strings.Contains(stdout.String(), "restart") {
			t.Errorf("%s: expected restart reminder in stdout, got: %s", step.name, stdout.String())
		}
	}
}

func TestNoSubcommandPrintsUsageAndFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{}, &stdout, &stderr)
	if code == 0 {
		t.Errorf("no subcommand: expected non-zero exit code, got 0")
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Errorf("no subcommand: expected usage on stderr, got: %s", stderr.String())
	}
}

func TestHelpFlagPrintsUsageAndSucceeds(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-h"}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("-h: expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("-h: expected usage on stdout, got: %s", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Errorf("--help: expected exit code 0, got %d", code)
	}
	if !strings.Contains(stdout.String(), "list") || !strings.Contains(stdout.String(), "add") ||
		!strings.Contains(stdout.String(), "rotate") || !strings.Contains(stdout.String(), "remove") {
		t.Errorf("--help: expected usage to mention all four subcommands, got: %s", stdout.String())
	}
}

func TestUnknownSubcommandFails(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"bogus-subcommand"}, &stdout, &stderr)
	if code == 0 {
		t.Errorf("unknown subcommand: expected non-zero exit code, got 0")
	}
}

func isHex(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, r := range s {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return false
		}
	}
	return true
}
