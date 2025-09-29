package rpc

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestClientSourceCanonicalizesForwardedFor(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{TrustedProxies: []string{"10.0.0.1"}})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:8000"
	req.Header.Set("X-Forwarded-For", " 198.51.100.9:443 ")

	if source := server.clientSource(req); source != "198.51.100.9" {
		t.Fatalf("expected canonical forwarded client, got %q", source)
	}
}

func TestClientSourceCapsForwardedForChain(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{TrustedProxies: []string{"10.0.0.1"}})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "10.0.0.1:8000"
	parts := make([]string, maxForwardedForAddrs+1)
	for i := range parts {
		parts[i] = " "
	}
	parts[len(parts)-1] = "198.51.100.10"
	req.Header.Set("X-Forwarded-For", strings.Join(parts, ","))

	if source := server.clientSource(req); source != "10.0.0.1" {
		t.Fatalf("expected proxy address fallback when forwarded chain exceeds limit, got %q", source)
	}
}

func TestRateLimiterNormalizesSources(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{})
	now := time.Now()

	if !server.allowSource(" 198.51.100.11 ", now) {
		t.Fatalf("expected first request to be allowed")
	}
	if !server.allowSource("198.51.100.11", now) {
		t.Fatalf("expected normalized source to use same limiter")
	}
	server.mu.Lock()
	limiterCount := len(server.rateLimiters)
	server.mu.Unlock()
	if limiterCount != 1 {
		t.Fatalf("expected a single limiter entry, got %d", limiterCount)
	}
}

func TestRateLimiterEvictsStaleEntries(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{})
	now := time.Now()
	staleTime := now.Add(-rateLimiterStaleAfter - time.Second)

	for i := 0; i < 3; i++ {
		source := fmt.Sprintf("198.51.100.%d", i)
		if !server.allowSource(source, staleTime) {
			t.Fatalf("expected stale source %d to be tracked", i)
		}
	}
	server.mu.Lock()
	if len(server.rateLimiters) != 3 {
		server.mu.Unlock()
		t.Fatalf("expected three limiter entries before eviction, got %d", len(server.rateLimiters))
	}
	server.mu.Unlock()

	if !server.allowSource("new-source", now) {
		t.Fatalf("expected request from new source to be allowed")
	}

	server.mu.Lock()
	if len(server.rateLimiters) != 1 {
		t.Fatalf("expected stale limiters to be evicted, got %d entries", len(server.rateLimiters))
	}
	if _, ok := server.rateLimiters["new-source"]; !ok {
		t.Fatalf("expected new source limiter to remain")
	}
	server.mu.Unlock()
}

func TestRateLimiterEvictsOldestWhenCapacityExceeded(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{})
	now := time.Now()

	for i := 0; i < rateLimiterMaxEntries; i++ {
		source := fmt.Sprintf("client-%d", i)
		if !server.allowSource(source, now) {
			t.Fatalf("expected initial requests to be allowed")
		}
	}

	if !server.allowSource("extra-client", now) {
		t.Fatalf("expected extra client to be allowed after eviction")
	}

	server.mu.Lock()
	if len(server.rateLimiters) != rateLimiterMaxEntries {
		count := len(server.rateLimiters)
		server.mu.Unlock()
		t.Fatalf("expected limiter map to cap at %d entries, got %d", rateLimiterMaxEntries, count)
	}
	if _, ok := server.rateLimiters["extra-client"]; !ok {
		server.mu.Unlock()
		t.Fatalf("expected extra client limiter to be stored")
	}
	evictedInitial := false
	for i := 0; i < rateLimiterMaxEntries; i++ {
		if _, ok := server.rateLimiters[fmt.Sprintf("client-%d", i)]; !ok {
			evictedInitial = true
			break
		}
	}
	server.mu.Unlock()
	if !evictedInitial {
		t.Fatalf("expected at least one initial limiter to be evicted")
	}
}

func TestRateLimiterChurnEnforcesLimits(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{})
	now := time.Now()
	source := "198.51.100.200"

	for i := 0; i < maxTxPerWindow; i++ {
		if !server.allowSource(source, now) {
			t.Fatalf("expected request %d to be allowed", i)
		}
	}

	for i := 0; i < rateLimiterMaxEntries-1; i++ {
		churnSource := fmt.Sprintf("churn-%d", i)
		if !server.allowSource(churnSource, now) {
			t.Fatalf("expected churn source %d to be allowed", i)
		}
	}

	if server.allowSource(source, now) {
		t.Fatalf("expected churned source to remain rate limited within same window")
	}
}

func TestServerRememberTxRejectsDuplicateWithinTTL(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{})
	now := time.Now()

	if !server.rememberTx("tx-1", now) {
		t.Fatalf("expected first occurrence to be accepted")
	}
	if server.rememberTx("tx-1", now.Add(time.Second)) {
		t.Fatalf("expected duplicate within TTL to be rejected")
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.txSeen) != 1 {
		t.Fatalf("expected a single entry to remain, got %d", len(server.txSeen))
	}
	if len(server.txSeenQueue) != 1 {
		t.Fatalf("expected a single queue entry to remain, got %d", len(server.txSeenQueue))
	}
}

func TestServerRememberTxEvictsExpired(t *testing.T) {
	server := NewServer(nil, nil, ServerConfig{})
	base := time.Now().Add(-2 * txSeenTTL)

	if !server.rememberTx("tx-old", base) {
		t.Fatalf("expected initial transaction to be accepted")
	}

	advanced := base.Add(txSeenTTL + time.Minute)
	if !server.rememberTx("tx-new", advanced) {
		t.Fatalf("expected transaction after TTL to be accepted")
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if _, exists := server.txSeen["tx-old"]; exists {
		t.Fatalf("expected expired transaction to be evicted")
	}
	if _, exists := server.txSeen["tx-new"]; !exists {
		t.Fatalf("expected new transaction to be recorded")
	}
	if len(server.txSeenQueue) != 1 {
		t.Fatalf("expected queue to contain only the fresh transaction, got %d entries", len(server.txSeenQueue))
	}
}
