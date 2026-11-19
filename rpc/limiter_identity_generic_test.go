package rpc

// The per-identity quotas on the generic request path (PL-EXT-37). The generic
// limiter used to read the caller's identity from the request context, where
// only requireAuth puts it, and requireAuth runs later and only inside the
// methods that ask for it; so on this path the identity was always empty and
// MaxTxPerIdentity never applied to a call to any method except
// nhb_sendTransaction. These tests use only the server's public behaviour (the
// same probe as limiter_keys_behaviour_test.go), so they can be pointed at the
// tree before the fix: the first of them fails there.

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// A trusted proxy forwards a different client address for each call, so every
// call has an address bucket of its own and only a per-identity quota can stop
// the sequence. The method is a public read that answers without a node.
func identityProbe(srv *Server, i int, bearer string) (int, int) {
	headers := map[string]string{"X-Forwarded-For": fmt.Sprintf("203.0.113.%d", 10+i)}
	if bearer != "" {
		headers["Authorization"] = "Bearer " + bearer
	}
	return rateProbe(srv, "127.0.0.1:41000", headers)
}

func TestIdentityQuotaAppliesToACredentialedCallerOnTheGenericPath(t *testing.T) {
	srv := proxiedServer(t, ServerConfig{MaxTxPerWindow: 100, MaxTxPerIP: 100, MaxTxPerIdentity: 3})
	token := signSubjectJWT(t, "gateway-a", time.Now().Add(time.Hour))

	// Each call comes from another address, so no address bucket is near its
	// limit: three calls are inside the identity's quota...
	for i := 0; i < 3; i++ {
		if code, _ := identityProbe(srv, i, token); code != http.StatusOK {
			t.Fatalf("call %d from one credentialed identity: HTTP %d, want 200", i, code)
		}
	}
	// ... and the fourth is over it, whichever address it comes from.
	code, rpcCode := identityProbe(srv, 3, token)
	if code != http.StatusTooManyRequests || rpcCode != codeRateLimited {
		t.Fatalf("the fourth call under one identity: HTTP %d code %d, want 429 / rate limited (the identity quota did not apply)", code, rpcCode)
	}
}

func TestIdentityQuotaIsPerIdentity(t *testing.T) {
	srv := proxiedServer(t, ServerConfig{MaxTxPerWindow: 100, MaxTxPerIP: 100, MaxTxPerIdentity: 2})
	a := signSubjectJWT(t, "gateway-a", time.Now().Add(time.Hour))
	b := signSubjectJWT(t, "gateway-b", time.Now().Add(time.Hour))
	for i := 0; i < 2; i++ {
		if code, _ := identityProbe(srv, i, a); code != http.StatusOK {
			t.Fatalf("identity a, call %d: HTTP %d", i, code)
		}
	}
	if code, _ := identityProbe(srv, 2, a); code != http.StatusTooManyRequests {
		t.Fatalf("identity a, third call: HTTP %d, want 429", code)
	}
	// Another identity has a quota of its own.
	for i := 0; i < 2; i++ {
		if code, _ := identityProbe(srv, 10+i, b); code != http.StatusOK {
			t.Fatalf("identity b, call %d: HTTP %d, want 200 (a quota is not shared between identities)", i, code)
		}
	}
	// The subject is compared without regard to case and spacing, the way the
	// limiter's key is built, so a changed spelling is not a fresh quota.
	shouting := signSubjectJWT(t, "  GATEWAY-A ", time.Now().Add(time.Hour))
	if code, _ := identityProbe(srv, 20, shouting); code != http.StatusTooManyRequests {
		t.Fatalf("the same identity spelled differently: HTTP %d, want 429", code)
	}
}

func TestACallWithNoTokenIsLimitedByItsAddressAlone(t *testing.T) {
	// No credential, no identity: the identity quota (here 1) must not apply,
	// and authentication is not required for a public read.
	srv := proxiedServer(t, ServerConfig{MaxTxPerWindow: 100, MaxTxPerIP: 100, MaxTxPerIdentity: 1})
	for i := 0; i < 8; i++ {
		if code, _ := identityProbe(srv, i, ""); code != http.StatusOK {
			t.Fatalf("anonymous call %d from its own address: HTTP %d, want 200", i, code)
		}
	}
}

func TestATokenThatDoesNotVerifyGivesNoIdentity(t *testing.T) {
	srv := proxiedServer(t, ServerConfig{MaxTxPerWindow: 100, MaxTxPerIP: 100, MaxTxPerIdentity: 1})
	expired := signSubjectJWT(t, "gateway-a", time.Now().Add(-time.Hour))
	for _, bad := range []string{expired, "not-a-token", strings.Repeat("a", 40) + "." + strings.Repeat("b", 40) + "." + strings.Repeat("c", 40)} {
		for i := 0; i < 4; i++ {
			// A forged or expired credential must not open an identity bucket
			// (it would be a way to make quotas for names nobody holds).
			if code, _ := identityProbe(srv, i, bad); code != http.StatusOK {
				t.Fatalf("a token that does not verify, call %d: HTTP %d, want 200 and no identity", i, code)
			}
		}
	}
	srv.mu.Lock()
	defer srv.mu.Unlock()
	for key := range srv.rateLimiters {
		if strings.HasPrefix(key, limiterKeyPrefixIdentity) {
			t.Fatalf("a token that does not verify left an identity bucket behind: %s", key)
		}
	}
}

func TestTheIdentityQuotaDoesNotLoosenTheAddressQuota(t *testing.T) {
	// A credential adds a quota; it never takes the address quota away.
	srv := proxiedServer(t, ServerConfig{MaxTxPerWindow: 2, MaxTxPerIP: 2, MaxTxPerIdentity: 1000})
	token := signSubjectJWT(t, "gateway-a", time.Now().Add(time.Hour))
	headers := map[string]string{"X-Forwarded-For": "203.0.113.50", "Authorization": "Bearer " + token}
	for i := 0; i < 2; i++ {
		if code, _ := rateProbe(srv, "127.0.0.1:41000", headers); code != http.StatusOK {
			t.Fatalf("call %d from one address: HTTP %d", i, code)
		}
	}
	if code, rpcCode := rateProbe(srv, "127.0.0.1:41000", headers); code != http.StatusTooManyRequests || rpcCode != codeRateLimited {
		t.Fatalf("a credentialed caller over its address quota: HTTP %d code %d, want 429", code, rpcCode)
	}
}

func TestIdentityQuotaDoesNotChangeWhatAnUnauthenticatedMethodAnswers(t *testing.T) {
	// The identity is only the limiter's key: a public read still answers a
	// credentialed caller the same way it answers an anonymous one.
	srv := proxiedServer(t, ServerConfig{MaxTxPerWindow: 100, MaxTxPerIP: 100, MaxTxPerIdentity: 100})
	token := signSubjectJWT(t, "gateway-a", time.Now().Add(time.Hour))
	anon, anonRPC := identityProbe(srv, 0, "")
	cred, credRPC := identityProbe(srv, 1, token)
	if anon != cred || anonRPC != credRPC {
		t.Fatalf("a credential changed the answer of a public read: anonymous HTTP %d code %d, credentialed HTTP %d code %d", anon, anonRPC, cred, credRPC)
	}
}
