package rpc

// DC-24 regression tests. ServerConfig.TrustProxyHeaders used to be OR-ed with
// the TrustedProxies list (trusted := flag || listed), so enabling the flag
// trusted EVERY caller: any client that could reach the port could send its own
// X-Forwarded-For / X-Real-IP and choose the address that per-IP rate limits
// and AllowlistCIDRs are applied to. The forwarded address is now honoured only
// from a peer listed in TrustedProxies.

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func proxyRequest(remoteAddr string, headers map[string]string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = remoteAddr
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return req
}

func bothProxyHeaders(mode ProxyHeaderMode) ProxyHeadersConfig {
	return ProxyHeadersConfig{XForwardedFor: mode, XRealIP: mode}
}

func TestTrustProxyHeadersFlagAloneTrustsNoCaller(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		TrustProxyHeaders: true,
		ProxyHeaders:      bothProxyHeaders(ProxyHeaderModeSingle),
	})

	for _, headers := range []map[string]string{
		{"X-Forwarded-For": "198.51.100.8"},
		{"X-Real-IP": "198.51.100.8"},
	} {
		req := proxyRequest("192.0.2.10:7000", headers)
		if ip, err := server.resolveClientIP(req); err == nil || !strings.Contains(err.Error(), "untrusted") {
			t.Fatalf("SECURITY: %v from an unlisted peer was honoured (got %q, err %v)", headers, ip, err)
		}
	}

	// Without forwarding headers the client is simply the connecting peer.
	req := proxyRequest("192.0.2.10:7000", nil)
	if ip, err := server.resolveClientIP(req); err != nil || ip != "192.0.2.10" {
		t.Fatalf("expected the peer address, got %q, %v", ip, err)
	}
}

func TestTrustProxyHeadersFlagOnlyTrustsListedProxies(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		TrustProxyHeaders: true,
		TrustedProxies:    []string{"10.0.0.1"},
		ProxyHeaders:      bothProxyHeaders(ProxyHeaderModeSingle),
	})

	for _, headers := range []map[string]string{
		{"X-Forwarded-For": "198.51.100.8"},
		{"X-Real-IP": "198.51.100.8"},
	} {
		listed := proxyRequest("10.0.0.1:8080", headers)
		if ip, err := server.resolveClientIP(listed); err != nil || ip != "198.51.100.8" {
			t.Fatalf("expected the listed proxy's forwarded client for %v, got %q, %v", headers, ip, err)
		}
		for _, remote := range []string{"10.0.0.2:8080", "192.0.2.10:7000", "127.0.0.1:9000"} {
			unlisted := proxyRequest(remote, headers)
			if ip, err := server.resolveClientIP(unlisted); err == nil || !strings.Contains(err.Error(), "untrusted") {
				t.Fatalf("SECURITY: %v from unlisted peer %s was honoured (got %q, err %v)", headers, remote, ip, err)
			}
		}
	}
}

// TestShippedLoopbackProxyConfigStillWorks mirrors the shipped config.toml: the
// node listens on loopback behind a proxy on the same host, with
// RPCTrustProxyHeaders = true, RPCTrustedProxies = ["127.0.0.1"] and both
// forwarding headers in single-address mode.
func TestShippedLoopbackProxyConfigStillWorks(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		TrustProxyHeaders: true,
		TrustedProxies:    []string{"127.0.0.1"},
		ProxyHeaders:      bothProxyHeaders(ProxyHeaderModeSingle),
	})

	// The proxy relays the real client address.
	if ip, err := server.resolveClientIP(proxyRequest("127.0.0.1:41000", map[string]string{"X-Forwarded-For": "203.0.113.5"})); err != nil || ip != "203.0.113.5" {
		t.Fatalf("expected the proxy-relayed client 203.0.113.5, got %q, %v", ip, err)
	}
	if ip, err := server.resolveClientIP(proxyRequest("127.0.0.1:41000", map[string]string{"X-Real-IP": "203.0.113.6"})); err != nil || ip != "203.0.113.6" {
		t.Fatalf("expected the proxy-relayed client 203.0.113.6, got %q, %v", ip, err)
	}
	// A proxy that appends to a client-supplied header produces two addresses,
	// which single mode rejects rather than trusting the leftmost.
	if _, err := server.resolveClientIP(proxyRequest("127.0.0.1:41000", map[string]string{"X-Forwarded-For": "203.0.113.5, 198.51.100.99"})); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected a two-address chain to be rejected, got %v", err)
	}
	// A client that reaches the listener directly (not through the proxy) cannot
	// choose its identity.
	if ip, err := server.resolveClientIP(proxyRequest("203.0.113.77:52000", map[string]string{"X-Forwarded-For": "10.1.2.3"})); err == nil {
		t.Fatalf("SECURITY: a direct client's X-Forwarded-For was honoured (got %q)", ip)
	}
	// Local callers with no forwarding header are just themselves.
	if ip, err := server.resolveClientIP(proxyRequest("127.0.0.1:41000", nil)); err != nil || ip != "127.0.0.1" {
		t.Fatalf("expected 127.0.0.1, got %q, %v", ip, err)
	}
}

// TestForwardedForCannotBypassAllowlistFromUnlistedPeer drives the real
// dispatch: with the flag enabled and an allowlist configured, a client outside
// the allowlist must not get in by claiming an allowlisted address.
func TestForwardedForCannotBypassAllowlistFromUnlistedPeer(t *testing.T) {
	server := newTestServer(t, nil, nil, ServerConfig{
		TrustProxyHeaders: true,
		AllowlistCIDRs:    []string{"10.0.0.0/8"},
		ProxyHeaders:      bothProxyHeaders(ProxyHeaderModeSingle),
	})

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader([]byte("{}")))
	req.RemoteAddr = "198.51.100.7:1234"
	req.Header.Set("X-Forwarded-For", "10.1.2.3")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("SECURITY: a spoofed X-Forwarded-For got past the allowlist (HTTP %d: %s)", rec.Code, rec.Body.String())
	}
}

type proxyTrustLogCapture struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *proxyTrustLogCapture) Enabled(context.Context, slog.Level) bool { return true }
func (h *proxyTrustLogCapture) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r)
	return nil
}
func (h *proxyTrustLogCapture) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *proxyTrustLogCapture) WithGroup(string) slog.Handler      { return h }
func (h *proxyTrustLogCapture) warnings() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.Level >= slog.LevelWarn {
			out = append(out, r.Message)
		}
	}
	return out
}

func TestNewServerWarnsWhenTrustFlagHasNoProxyList(t *testing.T) {
	capture := &proxyTrustLogCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(previous) })

	newTestServer(t, nil, nil, ServerConfig{TrustProxyHeaders: true, TrustedProxies: []string{"127.0.0.1"}})
	newTestServer(t, nil, nil, ServerConfig{})
	if got := capture.warnings(); len(got) != 0 {
		t.Fatalf("unexpected warnings for a configured or disabled proxy setup: %v", got)
	}

	newTestServer(t, nil, nil, ServerConfig{TrustProxyHeaders: true})
	warnings := capture.warnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "TrustedProxies is empty") {
		t.Fatalf("expected one warning that TrustedProxies is empty, got %v", warnings)
	}
}
