package p2p

import (
	"bytes"
	"encoding/json"
	"errors"
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

func TestServerRedactsPeerLogs(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{}))

	cfg := baseConfig(bytes.Repeat([]byte{0xAA}, 32))
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	server.logger = logger
	server.metricsCollector = nil

	peer := &Peer{id: "peerXYZ", remoteAddr: "1.2.3.4:5555", inbound: true, server: server}
	server.mu.Lock()
	server.peers[peer.id] = peer
	server.inboundCount = 1
	server.mu.Unlock()

	server.removePeer(peer, false, errors.New("boom"))

	if buf.Len() == 0 {
		t.Fatalf("expected log entry for peer removal")
	}

	raw := buf.Bytes()
	if bytes.Contains(raw, []byte("peerXYZ")) {
		t.Fatalf("log output leaked peer identifier: %s", raw)
	}
	if bytes.Contains(raw, []byte("1.2.3.4:5555")) {
		t.Fatalf("log output leaked peer address: %s", raw)
	}

	var entry map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&entry); err != nil {
		t.Fatalf("failed to decode log entry: %v", err)
	}
	if value, ok := entry["peer_id"].(string); !ok || value != logging.RedactedValue {
		t.Fatalf("expected redacted peer_id, got %v", entry["peer_id"])
	}
	if value, ok := entry["peer_address"].(string); !ok || value != logging.RedactedValue {
		t.Fatalf("expected redacted peer_address, got %v", entry["peer_address"])
	}
}

func TestParseSeedListRedactsLoggedValues(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{}))

	_ = parseSeedList([]string{"invalid-seed-entry"}, logger)

	if buf.Len() == 0 {
		t.Fatalf("expected log entry for invalid seed")
	}

	raw := buf.Bytes()
	if bytes.Contains(raw, []byte("invalid-seed-entry")) {
		t.Fatalf("log output leaked seed endpoint: %s", raw)
	}

	var entry map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(raw), &entry); err != nil {
		t.Fatalf("failed to unmarshal log entry: %v", err)
	}
	if value, ok := entry["seed"].(string); !ok || value != logging.RedactedValue {
		t.Fatalf("expected redacted seed attribute, got %v", entry["seed"])
	}
}
