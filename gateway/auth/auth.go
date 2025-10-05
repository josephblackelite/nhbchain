package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// HeaderAPIKey is the header containing the caller's API key identifier.
	HeaderAPIKey = "X-Api-Key"
	// HeaderTimestamp is the unix timestamp (seconds) used when signing the request.
	HeaderTimestamp = "X-Timestamp"
	// HeaderNonce provides replay protection when combined with the timestamp.
	HeaderNonce = "X-Nonce"
	// HeaderSignature carries the hex-encoded HMAC-SHA256 signature for the request.
	HeaderSignature = "X-Signature"
	// MaxBodyForSignature is the maximum body size we will hash when authenticating.
	MaxBodyForSignature int = 1 << 20 // 1 MiB
)

// Principal represents an authenticated API client.
type Principal struct {
	APIKey string
}

// Authenticator verifies API key + HMAC signatures on incoming requests.
type Authenticator struct {
	secrets              map[string]string
	allowedTimestampSkew time.Duration
	nonceTTL             time.Duration
	nowFn                func() time.Time

	nonceMu sync.Mutex
	nonces  map[string]*nonceStore

	lastSeenMu sync.Mutex
	lastSeen   map[string]int64
}

// NewAuthenticator builds an Authenticator keyed by the provided secrets. The map
// should contain API key identifiers mapped to their shared secret.
func NewAuthenticator(secrets map[string]string, skew time.Duration, nonceTTL time.Duration, nowFn func() time.Time) *Authenticator {
	cloned := make(map[string]string, len(secrets))
	for k, v := range secrets {
		cloned[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if nowFn == nil {
		nowFn = time.Now
	}
	if skew <= 0 {
		skew = 2 * time.Minute
	}
	if nonceTTL <= 0 {
		// Default to twice the skew window, falling back to two minutes if skew is unset.
		if skew > 0 {
			nonceTTL = 2 * skew
		} else {
			nonceTTL = 2 * time.Minute
		}
	}
	return &Authenticator{
		secrets:              cloned,
		allowedTimestampSkew: skew,
		nonceTTL:             nonceTTL,
		nowFn:                nowFn,
		nonces:               make(map[string]*nonceStore),
		lastSeen:             make(map[string]int64),
	}
}

// Authenticate validates headers and signature, returning the caller principal.
func (a *Authenticator) Authenticate(r *http.Request, body []byte) (*Principal, error) {
	if len(body) > MaxBodyForSignature {
		return nil, fmt.Errorf("request body exceeds %d bytes", MaxBodyForSignature)
	}
	apiKey := strings.TrimSpace(r.Header.Get(HeaderAPIKey))
	if apiKey == "" {
		return nil, errors.New("missing X-Api-Key header")
	}
	secret, ok := a.secrets[apiKey]
	if !ok || secret == "" {
		return nil, errors.New("unknown API key")
	}
	timestampHeader := strings.TrimSpace(r.Header.Get(HeaderTimestamp))
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
	if a.allowedTimestampSkew > 0 && skew > a.allowedTimestampSkew {
		return nil, fmt.Errorf("timestamp outside allowed skew of %s", a.allowedTimestampSkew)
	}
	nonce := strings.TrimSpace(r.Header.Get(HeaderNonce))
	if nonce == "" {
		return nil, errors.New("missing X-Nonce header")
	}
	providedSig := strings.TrimSpace(r.Header.Get(HeaderSignature))
	if providedSig == "" {
		return nil, errors.New("missing X-Signature header")
	}
	expected := ComputeSignature(secret, timestampHeader, nonce, r.Method, CanonicalRequestPath(r), body)
	providedBytes, err := hex.DecodeString(providedSig)
	if err != nil {
		return nil, fmt.Errorf("invalid signature encoding: %w", err)
	}
	if !hmac.Equal(providedBytes, expected) {
		return nil, errors.New("invalid signature")
	}
	if a.isReplay(apiKey, timestampHeader, nonce, now) {
		return nil, errors.New("nonce already used")
	}
	if a.isTimestampReplay(apiKey, ts, now) {
		return nil, errors.New("timestamp not increasing")
	}
	return &Principal{APIKey: apiKey}, nil
}

func (a *Authenticator) isReplay(apiKey, timestamp, nonce string, now time.Time) bool {
	cache := a.nonceStore(apiKey)
	composite := timestamp + "|" + nonce
	return cache.Seen(composite, now)
}

func (a *Authenticator) isTimestampReplay(apiKey string, ts time.Time, now time.Time) bool {
	if a.allowedTimestampSkew <= 0 {
		return false
	}
	cutoff := now.Add(-a.allowedTimestampSkew)
	current := ts.Unix()

	a.lastSeenMu.Lock()
	defer a.lastSeenMu.Unlock()

	last, ok := a.lastSeen[apiKey]
	if ok {
		lastTime := time.Unix(last, 0).UTC()
		if lastTime.After(cutoff) {
			if current <= last {
				return true
			}
		} else {
			delete(a.lastSeen, apiKey)
			ok = false
		}
	}
	if !ok || current > last {
		a.lastSeen[apiKey] = current
	}
	return false
}

func (a *Authenticator) nonceStore(apiKey string) *nonceStore {
	a.nonceMu.Lock()
	defer a.nonceMu.Unlock()
	cache, ok := a.nonces[apiKey]
	if ok {
		return cache
	}
	cache = newNonceStore(a.nonceTTL)
	a.nonces[apiKey] = cache
	return cache
}

// CanonicalRequestPath normalises URL paths and query ordering for signing.
func CanonicalRequestPath(r *http.Request) string {
	path := r.URL.Path
	if path == "" {
		path = "/"
	}
	if r.URL.RawQuery != "" {
		path += "?" + CanonicalQuery(r.URL.RawQuery)
	}
	return path
}

// CanonicalQuery normalises raw query strings for stable HMAC signing.
func CanonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	sort.Strings(parts)
	return strings.Join(parts, "&")
}

// ComputeSignature builds the HMAC-SHA256 signature bytes for the request metadata.
func ComputeSignature(secret, timestamp, nonce, method, path string, body []byte) []byte {
	payload := strings.Join([]string{timestamp, nonce, strings.ToUpper(method), path, string(body)}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(payload))
	return mac.Sum(nil)
}

func parseUnixTimestamp(v string) (time.Time, error) {
	secs, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(secs, 0).UTC(), nil
}

type nonceStore struct {
	ttl time.Duration

	mu      sync.Mutex
	entries map[string]time.Time
}

func newNonceStore(ttl time.Duration) *nonceStore {
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	return &nonceStore{
		ttl:     ttl,
		entries: make(map[string]time.Time),
	}
}

// Seen returns true if the provided nonce has already been observed within the TTL window.
func (n *nonceStore) Seen(key string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	cutoff := now.Add(-n.ttl)
	for k, ts := range n.entries {
		if ts.Before(cutoff) {
			delete(n.entries, k)
		}
	}
	if _, exists := n.entries[key]; exists {
		return true
	}
	n.entries[key] = now
	return false
}
