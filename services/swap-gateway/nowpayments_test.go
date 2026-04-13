package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestVerifyIPNHMAC(t *testing.T) {
	secret := "super-secret"
	body := []byte(`{"orderId":"SWP_1"}`)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := hex.EncodeToString(mac.Sum(nil))

	if !VerifyIPNHMAC(secret, body, sig) {
		t.Fatalf("expected verification to pass")
	}

	if VerifyIPNHMAC(secret, body, "deadbeef") {
		t.Fatalf("expected verification to fail")
	}
}
