package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// VerifyIPNHMAC ensures the webhook payload integrity against the shared secret.
func VerifyIPNHMAC(secret string, body []byte, provided string) bool {
	if secret == "" {
		return true
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	cleaned := strings.TrimSpace(provided)
	cleaned = strings.TrimPrefix(strings.ToLower(cleaned), "0x")
	if len(cleaned) == 0 {
		return false
	}
	got, err := hex.DecodeString(cleaned)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, got)
}
