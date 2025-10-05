package gateway_test

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	gatewayauth "nhbchain/gateway/auth"
)

func TestHMACReplayRejected(t *testing.T) {
	now := time.Unix(1700000000, 0).UTC()
	auth := gatewayauth.NewAuthenticator(map[string]string{"test": "secret"}, 2*time.Minute, 0, 0, func() time.Time {
		return now
	})

	body := []byte(`{"payload":"example"}`)
	req := httptest.NewRequest(http.MethodPost, "/payments?id=1&sort=desc", bytes.NewReader(body))
	timestamp := strconv.FormatInt(now.Unix(), 10)
	nonce := "nonce-original"
	sig := hex.EncodeToString(gatewayauth.ComputeSignature("secret", timestamp, nonce, http.MethodPost, gatewayauth.CanonicalRequestPath(req), body))

	req.Header.Set(gatewayauth.HeaderAPIKey, "test")
	req.Header.Set(gatewayauth.HeaderTimestamp, timestamp)
	req.Header.Set(gatewayauth.HeaderNonce, nonce)
	req.Header.Set(gatewayauth.HeaderSignature, sig)

	principal, err := auth.Authenticate(req, body)
	if err != nil {
		t.Fatalf("authenticate first request: %v", err)
	}
	if principal.APIKey != "test" {
		t.Fatalf("unexpected principal: %+v", principal)
	}

	replay := httptest.NewRequest(http.MethodPost, "/payments?sort=desc&id=1", bytes.NewReader(body))
	replay.Header = req.Header.Clone()
	if _, err := auth.Authenticate(replay, body); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("expected nonce replay rejection, got %v", err)
	}

	newNonce := "nonce-retry"
	sig2 := hex.EncodeToString(gatewayauth.ComputeSignature("secret", timestamp, newNonce, http.MethodPost, gatewayauth.CanonicalRequestPath(replay), body))
	retry := httptest.NewRequest(http.MethodPost, "/payments?sort=desc&id=1", bytes.NewReader(body))
	retry.Header.Set(gatewayauth.HeaderAPIKey, "test")
	retry.Header.Set(gatewayauth.HeaderTimestamp, timestamp)
	retry.Header.Set(gatewayauth.HeaderNonce, newNonce)
	retry.Header.Set(gatewayauth.HeaderSignature, sig2)

	if _, err := auth.Authenticate(retry, body); err == nil || !strings.Contains(err.Error(), "timestamp") {
		t.Fatalf("expected timestamp replay rejection, got %v", err)
	}
}
