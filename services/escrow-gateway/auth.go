package main

import (
	"encoding/hex"
	"net/http"
	"time"

	gatewayauth "nhbchain/gateway/auth"
)

const (
	headerAPIKey    = gatewayauth.HeaderAPIKey
	headerTimestamp = gatewayauth.HeaderTimestamp
	headerNonce     = gatewayauth.HeaderNonce
	headerSignature = gatewayauth.HeaderSignature
	maxBodyForSig   = gatewayauth.MaxBodyForSignature
)

// Principal represents an authenticated API client.
type Principal = gatewayauth.Principal

// Authenticator verifies API key + HMAC signatures on incoming requests.
type Authenticator = gatewayauth.Authenticator

func NewAuthenticator(keys []APIKeyConfig, skew, nonceTTL time.Duration, nowFn func() time.Time) *Authenticator {
	secrets := make(map[string]string, len(keys))
	for _, key := range keys {
		secrets[key.Key] = key.Secret
	}
	auth := gatewayauth.NewAuthenticator(secrets, skew, nonceTTL, nowFn)
	return auth
}

func canonicalRequestPath(r *http.Request) string {
	return gatewayauth.CanonicalRequestPath(r)
}

func canonicalQuery(raw string) string {
	return gatewayauth.CanonicalQuery(raw)
}

func computeSignature(secret, timestamp, nonce, method, path string, body []byte) string {
	sig := gatewayauth.ComputeSignature(secret, timestamp, nonce, method, path, body)
	return hex.EncodeToString(sig)
}
