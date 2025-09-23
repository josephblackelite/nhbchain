package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

const (
	headerAPIKey        = "X-Api-Key"
	headerTimestamp     = "X-Timestamp"
	headerSignature     = "X-Signature"
	maxBodyForSig   int = 1 << 20 // 1 MiB
)

// Principal represents an authenticated API client.
type Principal struct {
	APIKey string
}

// Authenticator verifies API key + HMAC signatures on incoming requests.
type Authenticator struct {
	secrets              map[string]string
	allowedTimestampSkew time.Duration
	nowFn                func() time.Time
}

func NewAuthenticator(keys []APIKeyConfig, skew time.Duration, nowFn func() time.Time) *Authenticator {
	secrets := make(map[string]string, len(keys))
	for _, key := range keys {
		secrets[key.Key] = key.Secret
	}
	if nowFn == nil {
		nowFn = time.Now
	}
	return &Authenticator{secrets: secrets, allowedTimestampSkew: skew, nowFn: nowFn}
}

// Authenticate validates headers and signature, returning the caller principal.
func (a *Authenticator) Authenticate(r *http.Request, body []byte) (*Principal, error) {
	if len(body) > maxBodyForSig {
		return nil, fmt.Errorf("request body exceeds %d bytes", maxBodyForSig)
	}
	apiKey := strings.TrimSpace(r.Header.Get(headerAPIKey))
	if apiKey == "" {
		return nil, errors.New("missing X-Api-Key header")
	}
	secret, ok := a.secrets[apiKey]
	if !ok {
		return nil, errors.New("unknown API key")
	}
	timestampHeader := strings.TrimSpace(r.Header.Get(headerTimestamp))
	if timestampHeader == "" {
		return nil, errors.New("missing X-Timestamp header")
	}
	ts, err := parseUnixTimestamp(timestampHeader)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp: %w", err)
	}
	now := a.nowFn().UTC()
	skew := now.Sub(ts)
	if skew < 0 {
		skew = -skew
	}
	if skew > a.allowedTimestampSkew {
		return nil, fmt.Errorf("timestamp outside allowed skew of %s", a.allowedTimestampSkew)
	}
	providedSig := strings.TrimSpace(r.Header.Get(headerSignature))
	if providedSig == "" {
		return nil, errors.New("missing X-Signature header")
	}
	expected := computeSignature(secret, timestampHeader, r.Method, canonicalRequestPath(r), body)
	if !hmac.Equal([]byte(strings.ToLower(providedSig)), []byte(expected)) {
		return nil, errors.New("invalid signature")
	}
	return &Principal{APIKey: apiKey}, nil
}

func canonicalRequestPath(r *http.Request) string {
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	if r.URL.RawQuery != "" {
		// Ensure consistent ordering of query params
		path += "?" + canonicalQuery(r.URL.RawQuery)
	}
	return path
}

func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

func computeSignature(secret, timestamp, method, path string, body []byte) string {
	payload := strings.Join([]string{timestamp, strings.ToUpper(method), path, string(body)}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}
