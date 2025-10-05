package gateway_test

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	gatewayauth "nhbchain/gateway/auth"
)

func TestAuthenticatorRejectsOlderTimestamp(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	current := base
	auth := gatewayauth.NewAuthenticator(map[string]string{"test": "secret"}, 0, 0, 0, func() time.Time {
		return current
	})

	body := []byte(`{"payload":"example"}`)
	req := httptest.NewRequest(http.MethodPost, "/payments?id=1", bytes.NewReader(body))
	ts := strconv.FormatInt(base.Unix(), 10)
	nonce := "nonce-1"
	sig := hex.EncodeToString(gatewayauth.ComputeSignature("secret", ts, nonce, http.MethodPost, gatewayauth.CanonicalRequestPath(req), body))
	req.Header.Set(gatewayauth.HeaderAPIKey, "test")
	req.Header.Set(gatewayauth.HeaderTimestamp, ts)
	req.Header.Set(gatewayauth.HeaderNonce, nonce)
	req.Header.Set(gatewayauth.HeaderSignature, sig)

	if _, err := auth.Authenticate(req, body); err != nil {
		t.Fatalf("authenticate first request: %v", err)
	}

	current = base.Add(30 * time.Second)
	older := strconv.FormatInt(base.Add(-30*time.Second).Unix(), 10)
	nonce2 := "nonce-2"
	replay := httptest.NewRequest(http.MethodPost, "/payments?id=1", bytes.NewReader(body))
	sig2 := hex.EncodeToString(gatewayauth.ComputeSignature("secret", older, nonce2, http.MethodPost, gatewayauth.CanonicalRequestPath(replay), body))
	replay.Header.Set(gatewayauth.HeaderAPIKey, "test")
	replay.Header.Set(gatewayauth.HeaderTimestamp, older)
	replay.Header.Set(gatewayauth.HeaderNonce, nonce2)
	replay.Header.Set(gatewayauth.HeaderSignature, sig2)

	if _, err := auth.Authenticate(replay, body); err == nil {
		t.Fatalf("expected older timestamp to be rejected")
	}
}

func TestAuthenticatorAcceptsNewerTimestamp(t *testing.T) {
	base := time.Unix(1700000000, 0).UTC()
	current := base
	auth := gatewayauth.NewAuthenticator(map[string]string{"test": "secret"}, 0, 0, 0, func() time.Time {
		return current
	})

	body := []byte(`{"payload":"example"}`)
	req := httptest.NewRequest(http.MethodPost, "/payments?id=1", bytes.NewReader(body))
	ts := strconv.FormatInt(base.Unix(), 10)
	nonce := "nonce-a"
	sig := hex.EncodeToString(gatewayauth.ComputeSignature("secret", ts, nonce, http.MethodPost, gatewayauth.CanonicalRequestPath(req), body))
	req.Header.Set(gatewayauth.HeaderAPIKey, "test")
	req.Header.Set(gatewayauth.HeaderTimestamp, ts)
	req.Header.Set(gatewayauth.HeaderNonce, nonce)
	req.Header.Set(gatewayauth.HeaderSignature, sig)

	if _, err := auth.Authenticate(req, body); err != nil {
		t.Fatalf("authenticate first request: %v", err)
	}

	current = base.Add(30 * time.Second)
	newer := strconv.FormatInt(current.Unix(), 10)
	nonce2 := "nonce-b"
	next := httptest.NewRequest(http.MethodPost, "/payments?id=1", bytes.NewReader(body))
	sig2 := hex.EncodeToString(gatewayauth.ComputeSignature("secret", newer, nonce2, http.MethodPost, gatewayauth.CanonicalRequestPath(next), body))
	next.Header.Set(gatewayauth.HeaderAPIKey, "test")
	next.Header.Set(gatewayauth.HeaderTimestamp, newer)
	next.Header.Set(gatewayauth.HeaderNonce, nonce2)
	next.Header.Set(gatewayauth.HeaderSignature, sig2)

	if _, err := auth.Authenticate(next, body); err != nil {
		t.Fatalf("expected newer timestamp to be accepted, got %v", err)
	}
}
