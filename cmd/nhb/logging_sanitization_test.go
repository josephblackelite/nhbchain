package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"

	"nhbchain/observability/logging"
)

func TestSeedLogRedactsSensitiveValues(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{}))

	sensitiveSeed := "node123@192.0.2.10:26656"
	logger.Warn("Ignoring seed for test",
		logging.MaskField("seed", sensitiveSeed),
		slog.String("reason", "unit test"))

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to decode log payload: %v", err)
	}

	if logging.IsAllowlisted("seed") {
		t.Fatalf("seed should not be allowlisted for logging: %v", logging.RedactionAllowlist())
	}

	raw := buf.Bytes()
	if bytes.Contains(raw, []byte(sensitiveSeed)) {
		t.Fatalf("log output leaked sensitive seed: %s", raw)
	}

	value, ok := entry["seed"].(string)
	if !ok {
		t.Fatalf("expected string seed attribute, got %T", entry["seed"])
	}
	if value != logging.RedactedValue {
		t.Fatalf("expected redacted seed, got %q", value)
	}
}
