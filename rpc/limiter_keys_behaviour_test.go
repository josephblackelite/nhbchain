package rpc

// What the rate limits are applied to, through the real dispatch. The key is the
// client's address as a trusted proxy reports it; it must never be text the
// client made up (a fresh bucket per request), an IPv6 client must not get a
// fresh bucket per address of its own network, and a local service that presents
// its credential must not share a bucket with anonymous traffic that reached the
// node through the same proxy address with no client address. These use only the
// server's public behaviour, so they can be pointed at an older tree.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func proxiedServer(t *testing.T, cfg ServerConfig) *Server {
	t.Helper()
	cfg.TrustProxyHeaders = true
	cfg.TrustedProxies = []string{"127.0.0.1"}
	cfg.ProxyHeaders = ProxyHeadersConfig{XForwardedFor: ProxyHeaderModeSingle, XRealIP: ProxyHeaderModeSingle}
	return newTestServer(t, nil, nil, cfg)
}

func rateProbe(srv *Server, remote string, headers map[string]string) (int, int) {
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "nhb_getLoyaltyBudgetStatus", "params": []any{}})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	var resp RPCResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	code := 0
	if resp.Error != nil {
		code = resp.Error.Code
	}
	return rec.Code, code
}

// TestNonAddressForwardedValueNeverBecomesABucket: "notanip!!" sent as
// X-Forwarded-For through the trusted proxy used to be accepted as a client of
// its own, so a client could name a new bucket for every request.
func TestNonAddressForwardedValueNeverBecomesABucket(t *testing.T) {
	srv := proxiedServer(t, ServerConfig{MaxTxPerWindow: 2, MaxTxPerIP: 2})
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP"} {
		for _, value := range []string{"notanip!!", "attacker-1", "attacker-2", "1.2.3", "example.com", "_", "unknown"} {
			code, rpcCode := rateProbe(srv, "127.0.0.1:41000", map[string]string{header: value})
			if code != http.StatusForbidden || rpcCode != codeUnauthorized {
				t.Fatalf("%s: %q was accepted (HTTP %d, code %d); it must be rejected", header, value, code, rpcCode)
			}
		}
	}
	srv.mu.Lock()
	buckets := len(srv.rateLimiters)
	srv.mu.Unlock()
	if buckets != 0 {
		t.Fatalf("rejected requests left %d limiter buckets behind", buckets)
	}

	// A real address still works, and is limited as itself.
	for i := 0; i < 2; i++ {
		if code, _ := rateProbe(srv, "127.0.0.1:41000", map[string]string{"X-Forwarded-For": "203.0.113.5"}); code != http.StatusOK {
			t.Fatalf("request %d from a real client address: HTTP %d", i, code)
		}
	}
	if code, rpcCode := rateProbe(srv, "127.0.0.1:41000", map[string]string{"X-Forwarded-For": "203.0.113.5"}); code != http.StatusTooManyRequests || rpcCode != codeRateLimited {
		t.Fatalf("the third request should be limited, got HTTP %d code %d", code, rpcCode)
	}
	if code, _ := rateProbe(srv, "127.0.0.1:41000", map[string]string{"X-Forwarded-For": "203.0.113.6"}); code != http.StatusOK {
		t.Fatalf("another client should have its own quota, got HTTP %d", code)
	}
}

func TestIPv6ClientsShareTheirNetworksBucket(t *testing.T) {
	srv := proxiedServer(t, ServerConfig{MaxTxPerWindow: 3, MaxTxPerIP: 3})
	sameNetwork := []string{"2001:db8:1:2::1", "2001:db8:1:2::2", "2001:db8:1:2:aaaa:bbbb:cccc:dddd"}
	for i, ip := range sameNetwork {
		if code, _ := rateProbe(srv, "127.0.0.1:41000", map[string]string{"X-Forwarded-For": ip}); code != http.StatusOK {
			t.Fatalf("request %d from %s: HTTP %d", i, ip, code)
		}
	}
	// Every address of that /64 is now over the quota, however it is spelled.
	for _, ip := range []string{"2001:db8:1:2::1", "2001:db8:1:2::9999", "[2001:db8:1:2::7]:443"} {
		if code, rpcCode := rateProbe(srv, "127.0.0.1:41000", map[string]string{"X-Forwarded-For": ip}); code != http.StatusTooManyRequests || rpcCode != codeRateLimited {
			t.Fatalf("%s should share the limited network's bucket, got HTTP %d code %d", ip, code, rpcCode)
		}
	}
	// A different network, and IPv4, are unaffected.
	for _, ip := range []string{"2001:db8:1:3::1", "203.0.113.9"} {
		if code, _ := rateProbe(srv, "127.0.0.1:41000", map[string]string{"X-Forwarded-For": ip}); code != http.StatusOK {
			t.Fatalf("%s should have its own bucket, got HTTP %d", ip, code)
		}
	}
}

func signSubjectJWT(t *testing.T, subject string, expires time.Time) string {
	t.Helper()
	claims := jwt.RegisteredClaims{
		Issuer:    "rpc-tests",
		Subject:   subject,
		Audience:  jwt.ClaimStrings([]string{"unit-tests"}),
		ExpiresAt: jwt.NewNumericDate(expires),
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Minute)),
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(testJWTSecret))
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signed
}

// TestLocalServiceWithACredentialDoesNotShareABucketWithAnonymousTraffic: the
// node's own callers reach it at 127.0.0.1 with no client address, which is
// also how anonymous traffic looks when the proxy forwards no address, so all of
// it used to share one bucket and anonymous traffic could spend the services'
// quota (and the reverse).
func TestLocalServiceWithACredentialDoesNotShareABucketWithAnonymousTraffic(t *testing.T) {
	srv := proxiedServer(t, ServerConfig{MaxTxPerWindow: 3, MaxTxPerIP: 3, MaxTxPerIdentity: 1000})
	bearer := func(subject string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + signSubjectJWT(t, subject, time.Now().Add(time.Hour))}
	}

	// Anonymous traffic with no client address spends the shared bucket.
	for i := 0; i < 3; i++ {
		if code, _ := rateProbe(srv, "127.0.0.1:41000", nil); code != http.StatusOK {
			t.Fatalf("anonymous request %d: HTTP %d", i, code)
		}
	}
	if code, rpcCode := rateProbe(srv, "127.0.0.1:41001", nil); code != http.StatusTooManyRequests || rpcCode != codeRateLimited {
		t.Fatalf("anonymous traffic should be limited, got HTTP %d code %d", code, rpcCode)
	}
	// A local service with a valid token still has its own quota...
	for i := 0; i < 3; i++ {
		if code, _ := rateProbe(srv, "127.0.0.1:41002", bearer("service-a")); code != http.StatusOK {
			t.Fatalf("credentialed request %d was refused although anonymous traffic is not its bucket: HTTP %d", i, code)
		}
	}
	// ... which is its own too: a second service is not held back by the first.
	if code, _ := rateProbe(srv, "127.0.0.1:41003", bearer("service-b")); code != http.StatusOK {
		t.Fatalf("a second service should have a bucket of its own, got HTTP %d", code)
	}
	// And the first service spending its quota does not touch anonymous traffic's
	// (still spent) or the other way round: it is limited as itself.
	if code, rpcCode := rateProbe(srv, "127.0.0.1:41004", bearer("service-a")); code != http.StatusTooManyRequests || rpcCode != codeRateLimited {
		t.Fatalf("a service should be limited under its own key, got HTTP %d code %d", code, rpcCode)
	}
}

// TestProxyTrustRulesAreUnchanged pins the behaviour the release introduced: a
// forwarded address counts only from a listed proxy.
func TestProxyTrustRulesAreUnchanged(t *testing.T) {
	srv := proxiedServer(t, ServerConfig{})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "203.0.113.77:5000"
	req.Header.Set("X-Forwarded-For", "10.1.2.3")
	if _, err := srv.resolveClientIP(req); err == nil || !strings.Contains(err.Error(), "untrusted") {
		t.Fatalf("a forwarded address from an unlisted peer must be rejected, got %v", err)
	}
	req = httptest.NewRequest(http.MethodPost, "/", nil)
	req.RemoteAddr = "127.0.0.1:5000"
	req.Header.Set("X-Forwarded-For", "203.0.113.5, 198.51.100.99")
	if _, err := srv.resolveClientIP(req); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("a two-address chain must still be rejected in single mode, got %v", err)
	}
}
