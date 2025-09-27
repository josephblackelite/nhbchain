package rpc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientSourceIgnoresForwardedForWhenNotTrusted(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.5:1234"
	req.Header.Set("X-Forwarded-For", "203.0.113.9")

	if source := server.clientSource(req); source != "10.0.0.5" {
		t.Fatalf("expected remote address, got %q", source)
	}
}

func TestClientSourceHonorsForwardedForFromTrustedProxy(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{TrustedProxies: []string{"10.0.0.1"}})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:8080"
	req.Header.Set("X-Forwarded-For", "198.51.100.7")

	if source := server.clientSource(req); source != "198.51.100.7" {
		t.Fatalf("expected forwarded client, got %q", source)
	}
}

func TestClientSourceHonorsForwardedForWhenTrustFlagEnabled(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{TrustProxyHeaders: true})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "192.0.2.10:7000"
	req.Header.Set("X-Forwarded-For", "198.51.100.8")

	if source := server.clientSource(req); source != "198.51.100.8" {
		t.Fatalf("expected forwarded client, got %q", source)
	}
}

func TestRateLimitSpoofedForwardedFor(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{})
	now := time.Now()
	remoteAddr := "10.1.1.1:9000"

	for i := 0; i < maxTxPerWindow; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("198.51.100.%d", i))
		if !server.allowSource(server.clientSource(req), now) {
			t.Fatalf("request %d should not be rate limited", i)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set("X-Forwarded-For", "198.51.100.250")
	if server.allowSource(server.clientSource(req), now) {
		t.Fatalf("spoofed forwarded-for should not bypass rate limiting")
	}
}

func TestRateLimitTrustedProxyHonorsForwardedFor(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{TrustedProxies: []string{"10.0.0.1"}})
	now := time.Now()
	remoteAddr := "10.0.0.1:5000"

	forwarded := "198.51.100.1"
	for i := 0; i < maxTxPerWindow; i++ {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = remoteAddr
		req.Header.Set("X-Forwarded-For", forwarded)
		if !server.allowSource(server.clientSource(req), now) {
			t.Fatalf("trusted proxy request %d should be allowed", i)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set("X-Forwarded-For", forwarded)
	if server.allowSource(server.clientSource(req), now) {
		t.Fatalf("expected rate limit when exceeding window for same client")
	}

	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set("X-Forwarded-For", "198.51.100.2")
	if !server.allowSource(server.clientSource(req), now) {
		t.Fatalf("distinct client behind trusted proxy should be allowed")
	}
}
