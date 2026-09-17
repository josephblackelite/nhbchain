package seeds

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

type mockResolver struct {
	records map[string][]string
	err     error
}

func (m *mockResolver) LookupTXT(_ context.Context, name string) ([]string, error) {
	if m.err != nil {
		return nil, m.err
	}
	if m.records == nil {
		return nil, errors.New("no records")
	}
	if values, ok := m.records[name]; ok {
		return values, nil
	}
	return nil, errors.New("not found")
}

func mustRegistry(t *testing.T, payload interface{}) *Registry {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	reg, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return reg
}

func TestResolveIncludesStaticAndDnsSeeds(t *testing.T) {
	t.Parallel()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	dnsRecord := map[string]interface{}{
		"nodeId":    "0xabc123",
		"address":   "seed-1.example.org:46656",
		"notBefore": now.Add(-time.Minute).Unix(),
		"notAfter":  now.Add(time.Hour).Unix(),
	}
	message := buildSigningMessage("0xabc123", "seed-1.example.org:46656", dnsRecord["notBefore"].(int64), dnsRecord["notAfter"].(int64), "seeds.example.org")
	signature := ed25519.Sign(priv, message)
	dnsRecord["signature"] = base64.StdEncoding.EncodeToString(signature)
	payload, err := json.Marshal(dnsRecord)
	if err != nil {
		t.Fatalf("marshal dns record: %v", err)
	}
	txtValue := recordPrefix + base64.StdEncoding.EncodeToString(payload)

	reg := mustRegistry(t, map[string]interface{}{
		"version": 1,
		"authorities": []map[string]interface{}{
			{
				"domain":    "seeds.example.org",
				"algorithm": "ed25519",
				"publicKey": base64.StdEncoding.EncodeToString(pub),
			},
		},
		"static": []map[string]interface{}{
			{
				"nodeId":  "0xdeadbeef",
				"address": "static.example.org:46656",
			},
		},
	})

	resolver := &mockResolver{records: map[string][]string{
		"_nhbseed.seeds.example.org": {txtValue},
	}}

	seeds, err := reg.Resolve(context.Background(), now, resolver)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(seeds) != 2 {
		t.Fatalf("expected 2 seeds, got %d", len(seeds))
	}
	if seeds[0].Source != "registry.static" {
		t.Fatalf("expected first seed to be static, got %q", seeds[0].Source)
	}
	if seeds[1].Source != "dns:seeds.example.org" {
		t.Fatalf("unexpected source %q", seeds[1].Source)
	}
}

func TestResolvePropagatesVerificationErrors(t *testing.T) {
	t.Parallel()
	pub, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	now := time.Unix(1_700_000_000, 0)
	// Malformed record missing signature
	dnsRecord := map[string]interface{}{
		"nodeId":  "0xabc",
		"address": "seed-bad.example.org:46656",
	}
	payload, err := json.Marshal(dnsRecord)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	txtValue := recordPrefix + base64.StdEncoding.EncodeToString(payload)

	reg := mustRegistry(t, map[string]interface{}{
		"version": 1,
		"authorities": []map[string]interface{}{
			{
				"domain":    "faulty.example.org",
				"algorithm": "ed25519",
				"publicKey": base64.StdEncoding.EncodeToString(pub),
			},
		},
		"static": []map[string]interface{}{
			{
				"nodeId":  "0xbeef",
				"address": "static.example.org:46656",
			},
		},
	})

	resolver := &mockResolver{records: map[string][]string{
		"_nhbseed.faulty.example.org": {txtValue},
	}}

	seeds, err := reg.Resolve(context.Background(), now, resolver)
	if err == nil {
		t.Fatalf("expected error from invalid record")
	}
	if len(seeds) != 1 {
		t.Fatalf("expected only static seed, got %d", len(seeds))
	}
	if seeds[0].Source != "registry.static" {
		t.Fatalf("unexpected source %q", seeds[0].Source)
	}
}

func TestStaticRespectsActivationWindow(t *testing.T) {
	t.Parallel()
	now := time.Unix(1_700_000_000, 0)
	reg := mustRegistry(t, map[string]interface{}{
		"version": 1,
		"static": []map[string]interface{}{
			{
				"nodeId":    "0x123",
				"address":   "future.example.org:46656",
				"notBefore": now.Add(time.Hour).Unix(),
			},
		},
	})
	seeds := reg.Static(now)
	if len(seeds) != 0 {
		t.Fatalf("expected no active static seeds, got %d", len(seeds))
	}
}
