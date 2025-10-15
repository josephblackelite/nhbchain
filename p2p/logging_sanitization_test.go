package p2p

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"nhbchain/observability/logging"
)

func TestConnManagerRedactsSeedIdentifiers(t *testing.T) {
	buf := &bytes.Buffer{}
	cm := &connManager{
		store:  &Peerstore{},
		now:    time.Now,
		logger: slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{})),
	}

	cm.markFailure(seedEndpoint{NodeID: "seedXYZ"})

	if buf.Len() == 0 {
		t.Fatalf("expected log entry for failed seed record")
	}

	if logging.IsAllowlisted("seed_id") {
		t.Fatalf("seed_id should not be allowlisted: %v", logging.RedactionAllowlist())
	}

	raw := buf.Bytes()
	if bytes.Contains(raw, []byte("seedXYZ")) {
		t.Fatalf("log output leaked seed identifier: %s", raw)
	}

	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil {
		t.Fatalf("failed to unmarshal log entry: %v", err)
	}
	value, ok := entry["seed_id"].(string)
	if !ok {
		t.Fatalf("expected string seed_id attribute, got %T", entry["seed_id"])
	}
	if value != logging.RedactedValue {
		t.Fatalf("expected redacted seed identifier, got %q", value)
	}
}
