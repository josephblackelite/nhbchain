package auth

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// Reads share a second with each other and with writes; writes keep the rule
// that a key's timestamp must be later than the last one it had accepted.

func readTestAuthenticator(now *time.Time) *Authenticator {
	return NewAuthenticator(map[string]string{"partner": "secret"}, 2*time.Minute, 5*time.Minute, 16, func() time.Time { return *now }, nil)
}

func signedRequest(method string, unix int64, nonce string) *http.Request {
	req := httptest.NewRequest(method, "https://example.test/v1/resource", nil)
	ts := strconv.FormatInt(unix, 10)
	req.Header.Set(HeaderAPIKey, "partner")
	req.Header.Set(HeaderTimestamp, ts)
	req.Header.Set(HeaderNonce, nonce)
	req.Header.Set(HeaderSignature, hex.EncodeToString(ComputeSignature("secret", ts, nonce, method, CanonicalRequestPath(req), nil)))
	return req
}

func TestAuthenticateReadAcceptsAnyNumberOfReadsInOneSecond(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	auth := readTestAuthenticator(&now)
	for i := 0; i < 5; i++ {
		principal, err := auth.AuthenticateRead(signedRequest(http.MethodGet, now.Unix(), "read-"+strconv.Itoa(i)))
		if err != nil {
			t.Fatalf("read %d in the same second: %v", i, err)
		}
		if principal == nil || principal.APIKey != "partner" {
			t.Fatalf("read %d: principal %+v", i, principal)
		}
	}
}

func TestAuthenticateReadDoesNotMoveTheWriteMark(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	auth := readTestAuthenticator(&now)

	if _, err := auth.AuthenticateRead(signedRequest(http.MethodGet, now.Unix()+3, "read-ahead")); err != nil {
		t.Fatalf("read: %v", err)
	}
	// A write at or before the read's timestamp is still the key's first.
	if _, err := auth.Authenticate(signedRequest(http.MethodPost, now.Unix(), "write-one"), nil); err != nil {
		t.Fatalf("write after a read signed later: %v", err)
	}
	// Writes are held to the strict rule as before.
	if _, err := auth.Authenticate(signedRequest(http.MethodPost, now.Unix(), "write-two"), nil); err == nil || err.Error() != "timestamp not increasing" {
		t.Fatalf("second write in the same second: got %v, want timestamp not increasing", err)
	}
	if _, err := auth.Authenticate(signedRequest(http.MethodPost, now.Unix()+1, "write-three"), nil); err != nil {
		t.Fatalf("write a second later: %v", err)
	}
}

func TestAuthenticateReadRefusesATimestampBehindTheLastWrite(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	auth := readTestAuthenticator(&now)
	written := now.Unix() + 10

	if _, err := auth.Authenticate(signedRequest(http.MethodPost, written, "write"), nil); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := auth.AuthenticateRead(signedRequest(http.MethodGet, written-1, "read-behind")); err == nil {
		t.Fatalf("a read signed before the last write was accepted")
	}
	if _, err := auth.AuthenticateRead(signedRequest(http.MethodGet, written, "read-level")); err != nil {
		t.Fatalf("a read signed in the second of the last write: %v", err)
	}

	// Once the write is older than the skew window it no longer limits reads.
	now = now.Add(3 * time.Minute)
	if _, err := auth.AuthenticateRead(signedRequest(http.MethodGet, now.Unix(), "read-later")); err != nil {
		t.Fatalf("a read after the write mark expired: %v", err)
	}
}

func TestAuthenticateReadStillChecksKeySignatureNonceAndSkew(t *testing.T) {
	now := time.Unix(1_700_000_000, 0).UTC()
	auth := readTestAuthenticator(&now)

	if _, err := auth.AuthenticateRead(signedRequest(http.MethodGet, now.Unix(), "once")); err != nil {
		t.Fatalf("first read: %v", err)
	}
	if _, err := auth.AuthenticateRead(signedRequest(http.MethodGet, now.Unix(), "once")); err == nil || err.Error() != "nonce already used" {
		t.Fatalf("a reused nonce: got %v, want nonce already used", err)
	}
	if _, err := auth.AuthenticateRead(signedRequest(http.MethodGet, now.Unix()-600, "stale")); err == nil {
		t.Fatalf("a read far outside the skew was accepted")
	}

	unknown := signedRequest(http.MethodGet, now.Unix(), "unknown-key")
	unknown.Header.Set(HeaderAPIKey, "someone-else")
	if _, err := auth.AuthenticateRead(unknown); err == nil {
		t.Fatalf("a read with an unknown key was accepted")
	}

	forged := signedRequest(http.MethodGet, now.Unix(), "forged")
	forged.Header.Set(HeaderSignature, hex.EncodeToString(make([]byte, 32)))
	if _, err := auth.AuthenticateRead(forged); err == nil || err.Error() != "invalid signature" {
		t.Fatalf("a read with a wrong signature: got %v, want invalid signature", err)
	}

	unsigned := signedRequest(http.MethodGet, now.Unix(), "unsigned")
	unsigned.Header.Del(HeaderSignature)
	if _, err := auth.AuthenticateRead(unsigned); err == nil {
		t.Fatalf("a read with no signature was accepted")
	}
}
