package server

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

func TestPartnerAuthenticatorMiddlewareSuccess(t *testing.T) {
	store := openStableTestStore(t, "partner_auth_success")
	creds := partnerCreds{id: "desk-1", apiKey: "test-key", secret: "test-secret"}
	auth, err := NewPartnerAuthenticator([]Partner{{
		ID:         creds.id,
		APIKey:     creds.apiKey,
		Secret:     creds.secret,
		DailyQuota: 0,
	}}, func() time.Time { return time.Now().UTC() }, store)
	if err != nil {
		t.Fatalf("new partner authenticator: %v", err)
	}
	payload := []byte(`{"asset":"ZNHB"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/stable/quote", bytes.NewReader(payload))
	signPartnerRequest(t, req, payload, &creds)
	recorder := httptest.NewRecorder()
	called := false
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		principal, ok := PartnerFromContext(r.Context())
		if !ok {
			t.Fatalf("partner context missing")
		}
		if principal.ID != creds.id {
			t.Fatalf("unexpected partner id: %s", principal.ID)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	handler.ServeHTTP(recorder, req)
	if !called {
		t.Fatalf("expected handler to be invoked")
	}
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("unexpected status: %d", recorder.Code)
	}
}

func TestPartnerAuthenticatorMiddlewareRejectsSignature(t *testing.T) {
	store := openStableTestStore(t, "partner_auth_invalid")
	creds := partnerCreds{id: "desk-1", apiKey: "test-key", secret: "test-secret"}
	auth, err := NewPartnerAuthenticator([]Partner{{
		ID:     creds.id,
		APIKey: creds.apiKey,
		Secret: creds.secret,
	}}, func() time.Time { return time.Now().UTC() }, store)
	if err != nil {
		t.Fatalf("new partner authenticator: %v", err)
	}
	payload := []byte(`{"asset":"ZNHB"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/stable/quote", bytes.NewReader(payload))
	signPartnerRequest(t, req, payload, &creds)
	req.Header.Set(gatewayauth.HeaderSignature, hex.EncodeToString([]byte("deadbeef")))
	recorder := httptest.NewRecorder()
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("handler should not be called")
	}))
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for invalid signature, got %d", recorder.Code)
	}
}

func TestPartnerAuthenticatorMiddlewareRejectsUnknownKey(t *testing.T) {
	store := openStableTestStore(t, "partner_auth_unknown")
	auth, err := NewPartnerAuthenticator([]Partner{{
		ID:     "desk-1",
		APIKey: "known-key",
		Secret: "known-secret",
	}}, func() time.Time { return time.Now().UTC() }, store)
	if err != nil {
		t.Fatalf("new partner authenticator: %v", err)
	}
	payload := []byte(`{"asset":"ZNHB"}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/stable/quote", bytes.NewReader(payload))
	now := time.Now().UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	nonce := "nonce-unknown"
	signature := gatewayauth.ComputeSignature("other-secret", timestamp, nonce, req.Method, gatewayauth.CanonicalRequestPath(req), payload)
	req.Header.Set(gatewayauth.HeaderAPIKey, "unknown-key")
	req.Header.Set(gatewayauth.HeaderTimestamp, timestamp)
	req.Header.Set(gatewayauth.HeaderNonce, nonce)
	req.Header.Set(gatewayauth.HeaderSignature, hex.EncodeToString(signature))
	recorder := httptest.NewRecorder()
	handler := auth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("handler should not be called")
	}))
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for unknown partner, got %d", recorder.Code)
	}
}
