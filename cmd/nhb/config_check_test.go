package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nhbchain/config"
)

// loadShippedConfig loads the checked-in config.toml, keeping the validator key
// in the environment so the test touches no keystore.
func loadShippedConfig(t *testing.T) *config.Config {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config.toml"))
	if err != nil {
		t.Fatalf("read config.toml: %v", err)
	}
	content := strings.Replace(string(raw), `ValidatorKMSEnv = ""`, `ValidatorKMSEnv = "NHB_TEST_VALIDATOR_KEY"`, 1)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return cfg
}

func capturedLogger() (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	return slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})), &buf
}

func warnings(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if rec["level"] == "WARN" {
			out = append(out, rec)
		}
	}
	return out
}

// Both live validators write the four round timers as "0s" (the engine then uses
// its defaults) and a minimum block interval of 2s. The check must say nothing
// about that, or it would be a warning on every start of a healthy validator.
func TestCheckConfigurationIsQuietForTheLiveValidatorsConfig(t *testing.T) {
	cfg := loadShippedConfig(t)
	cfg.Consensus = config.Consensus{MinBlockInterval: 2 * time.Second}
	logger, buf := capturedLogger()
	if found := checkConfiguration(logger, cfg); found != 0 {
		t.Fatalf("%d problems reported for the live configuration: %s", found, buf.String())
	}
	if got := warnings(t, buf); len(got) != 0 {
		t.Fatalf("warnings for the live configuration: %v", got)
	}
}

func TestCheckConfigurationIsQuietForTheShippedConfig(t *testing.T) {
	logger, buf := capturedLogger()
	if found := checkConfiguration(logger, loadShippedConfig(t)); found != 0 {
		t.Fatalf("%d problems reported for the shipped configuration: %s", found, buf.String())
	}
}

// Every problem is its own structured WARN, and the check returns: it is not a
// reason to stop the node.
func TestCheckConfigurationLogsEveryProblemAndDoesNotStop(t *testing.T) {
	cfg := loadShippedConfig(t)
	cfg.Global.Mempool.MaxBytes = 0
	cfg.Global.Governance.VotingPeriodSecs = 1
	cfg.Consensus = config.Consensus{
		CommitTimeout:    4 * time.Second,
		MinBlockInterval: 3 * time.Second,
	}
	logger, buf := capturedLogger()
	found := checkConfiguration(logger, cfg)

	got := warnings(t, buf)
	if found != 3 || len(got) != 3 {
		t.Fatalf("found %d problems and %d warnings, want 3 of each: %s", found, len(got), buf.String())
	}
	sections := map[string]int{}
	for _, rec := range got {
		if rec["msg"] != "configuration problem" || rec["problem"] == nil {
			t.Fatalf("warning without the message and a problem: %v", rec)
		}
		sections[rec["section"].(string)]++
	}
	if sections["global"] != 2 || sections["consensus"] != 1 {
		t.Fatalf("warnings by section: %v, want 2 global and 1 consensus", sections)
	}
}
