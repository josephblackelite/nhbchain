package rpc

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestParseClientAddr(t *testing.T) {
	valid := map[string]string{
		"198.51.100.7":                 "198.51.100.7",
		" 198.51.100.7 ":               "198.51.100.7",
		"198.51.100.7:443":             "198.51.100.7",
		"2001:db8::1":                  "2001:db8::1",
		"[2001:db8::1]":                "2001:db8::1",
		"[2001:db8::1]:8443":           "2001:db8::1",
		"2001:0db8:0000:0000::0001":    "2001:db8::1",
		"::ffff:198.51.100.7":          "198.51.100.7",
		"127.0.0.1":                    "127.0.0.1",
		"0.0.0.0":                      "0.0.0.0",
		"2001:db8:85a3::8a2e:370:7334": "2001:db8:85a3::8a2e:370:7334",
	}
	for in, want := range valid {
		if got := parseClientAddr(in); got != want {
			t.Errorf("parseClientAddr(%q) = %q, want %q", in, got, want)
		}
	}
	for _, in := range []string{
		"", " ", "notanip!!", "1.2.3", "1.2.3.4.5", "example.com", "example.com:80", "::gggg", "999.1.1.1",
		"1.2.3.4/24", "fe80::1%eth0", "unknown", "_hidden", "-", "1.2.3.4, 5.6.7.8", "null", "::1::2",
		strings.Repeat("a", 300),
	} {
		if got := parseClientAddr(in); got != "" {
			t.Errorf("parseClientAddr(%q) = %q, want it rejected", in, got)
		}
	}
}

func TestLimiterAddressKey(t *testing.T) {
	for in, want := range map[string]string{
		"198.51.100.7":                     "198.51.100.7",
		"127.0.0.1":                        "127.0.0.1",
		"2001:db8:1:2::1":                  "2001:db8:1:2::/64",
		"2001:db8:1:2:ffff:ffff:ffff:ffff": "2001:db8:1:2::/64",
		"2001:db8:1:3::1":                  "2001:db8:1:3::/64",
		"::1":                              "::/64",
	} {
		if got := limiterAddressKey(in); got != want {
			t.Errorf("limiterAddressKey(%q) = %q, want %q", in, got, want)
		}
	}
	// A key built from an address must survive the limiter's own normalisation.
	if got := canonicalHost(limiterAddressKey("2001:db8:1:2::1")); got != "2001:db8:1:2::/64" {
		t.Errorf("the limiter mangled an IPv6 key: %q", got)
	}
}

func TestOnlyAVerifiedCredentialFromAListedProxyGetsAServiceKey(t *testing.T) {
	srv := proxiedServer(t, ServerConfig{})
	keyOf := func(remote string, headers map[string]string) (string, bool) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = remote
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		client, err := srv.resolveClient(req)
		if err != nil {
			t.Fatalf("resolve: %v", err)
		}
		return srv.limiterKeyFor(req, client), client.unattributed
	}
	good := "Bearer " + signSubjectJWT(t, "Service-A", time.Now().Add(time.Hour))

	if key, shared := keyOf("127.0.0.1:1", map[string]string{"Authorization": good}); key != "svc/service-a" || !shared {
		t.Fatalf("a valid token from a listed proxy address with no client address: key %q shared %v", key, shared)
	}
	if key, shared := keyOf("127.0.0.1:1", nil); key != "127.0.0.1" || !shared {
		t.Fatalf("no credential: key %q shared %v, want the shared address", key, shared)
	}
	// A token that does not verify earns nothing.
	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{Issuer: "rpc-tests", Subject: "service-a", Audience: jwt.ClaimStrings([]string{"unit-tests"}), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))})
	forgedSigned, _ := forged.SignedString([]byte("some-other-secret"))
	if key, _ := keyOf("127.0.0.1:1", map[string]string{"Authorization": "Bearer " + forgedSigned}); key != "127.0.0.1" {
		t.Fatalf("a forged token got the key %q", key)
	}
	expired := "Bearer " + signSubjectJWT(t, "service-a", time.Now().Add(-time.Hour))
	if key, _ := keyOf("127.0.0.1:1", map[string]string{"Authorization": expired}); key != "127.0.0.1" {
		t.Fatalf("an expired token got the key %q", key)
	}
	// A token without a subject identifies nobody.
	noSubject := signSubjectJWT(t, "", time.Now().Add(time.Hour))
	if key, _ := keyOf("127.0.0.1:1", map[string]string{"Authorization": "Bearer " + noSubject}); key != "127.0.0.1" {
		t.Fatalf("a token with no subject got the key %q", key)
	}
	// A request that carries a client address is that client, token or not.
	if key, shared := keyOf("127.0.0.1:1", map[string]string{"Authorization": good, "X-Forwarded-For": "203.0.113.5"}); key != "203.0.113.5" || shared {
		t.Fatalf("a relayed client: key %q shared %v", key, shared)
	}
	// A caller that is not a listed proxy is its own address, token or not.
	if key, shared := keyOf("203.0.113.77:5000", map[string]string{"Authorization": good}); key != "203.0.113.77" || shared {
		t.Fatalf("a direct caller: key %q shared %v", key, shared)
	}
}
