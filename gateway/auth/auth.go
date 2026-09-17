package auth

import (
	"container/list"
	"context"
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

	maxAllowedTimestampSkew  = 2 * time.Minute
	defaultTimestampSkew     = maxAllowedTimestampSkew
	maxNonceWindow           = 10 * time.Minute
	defaultNonceWindow       = maxNonceWindow
	defaultNonceCapacity     = 4096
	maxNonceCapacity         = 65536
	persistencePruneInterval = time.Minute
)

// Principal represents an authenticated API client.
type Principal struct {
	APIKey string
}

// NonceRecord captures persisted nonce usage metadata.
type NonceRecord struct {
	APIKey     string
	Timestamp  string
	Nonce      string
	ObservedAt time.Time
}

// NoncePersistence provides durable storage for API key nonce usage.
type NoncePersistence interface {
	EnsureNonce(ctx context.Context, record NonceRecord) (bool, error)
	RecentNonces(ctx context.Context, cutoff time.Time) ([]NonceRecord, error)
	PruneNonces(ctx context.Context, cutoff time.Time) error
}

// Authenticator verifies API key + HMAC signatures on incoming requests.
type Authenticator struct {
	secrets              map[string]string
	allowedTimestampSkew time.Duration
	nonceTTL             time.Duration
	nonceCapacity        int
	nowFn                func() time.Time

	nonceMu sync.Mutex
	nonces  map[string]*nonceStore

	lastSeenMu sync.Mutex
	lastSeen   map[string]int64

	persistence NoncePersistence
	lastPruned  time.Time
}

// NewAuthenticator builds an Authenticator keyed by the provided secrets. The map
// should contain API key identifiers mapped to their shared secret.
func NewAuthenticator(secrets map[string]string, skew time.Duration, nonceTTL time.Duration, nonceCapacity int, nowFn func() time.Time, persistence NoncePersistence) *Authenticator {
	cloned := make(map[string]string, len(secrets))
	for k, v := range secrets {
		cloned[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	if nowFn == nil {
		nowFn = time.Now
	}
	if skew <= 0 {
		skew = defaultTimestampSkew
	}
	if skew > maxAllowedTimestampSkew {
		skew = maxAllowedTimestampSkew
	}
	if nonceTTL <= 0 {
		nonceTTL = defaultNonceWindow
	}
	if nonceTTL > maxNonceWindow {
		nonceTTL = maxNonceWindow
	}
	if nonceCapacity <= 0 {
		nonceCapacity = defaultNonceCapacity
	}
	if nonceCapacity > maxNonceCapacity {
		nonceCapacity = maxNonceCapacity
	}
	return &Authenticator{
		secrets:              cloned,
		allowedTimestampSkew: skew,
		nonceTTL:             nonceTTL,
		nonceCapacity:        nonceCapacity,
		nowFn:                nowFn,
		nonces:               make(map[string]*nonceStore),
		lastSeen:             make(map[string]int64),
		persistence:          persistence,
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
	duplicate, err := a.registerNonce(r.Context(), apiKey, timestampHeader, nonce, now)
	if err != nil {
		return nil, err
	}
	if duplicate {
		return nil, errors.New("nonce already used")
	}
	if a.isTimestampReplay(apiKey, ts, now) {
		return nil, errors.New("timestamp not increasing")
	}
	return &Principal{APIKey: apiKey}, nil
}

// HydrateNonces warms the in-memory cache with persisted nonce usage records.
func (a *Authenticator) HydrateNonces(ctx context.Context, cutoff time.Time) error {
	if a == nil || a.persistence == nil {
		return nil
	}
	records, err := a.persistence.RecentNonces(ctx, cutoff)
	if err != nil {
		return fmt.Errorf("load persistent nonces: %w", err)
	}
	for _, rec := range records {
		if strings.TrimSpace(rec.APIKey) == "" || strings.TrimSpace(rec.Timestamp) == "" || strings.TrimSpace(rec.Nonce) == "" {
			continue
		}
		observed := rec.ObservedAt
		if observed.IsZero() {
			observed = cutoff
		}
		store := a.nonceStore(rec.APIKey)
		store.Add(rec.Timestamp+"|"+rec.Nonce, observed)
	}
	return nil
}

func (a *Authenticator) registerNonce(ctx context.Context, apiKey, timestamp, nonce string, now time.Time) (bool, error) {
	cache := a.nonceStore(apiKey)
	composite := timestamp + "|" + nonce
	if cache.Contains(composite, now) {
		return true, nil
	}
	if a.persistence != nil {
		if err := a.prunePersistent(ctx, now); err != nil {
			return false, err
		}
		record := NonceRecord{
			APIKey:     apiKey,
			Timestamp:  timestamp,
			Nonce:      nonce,
			ObservedAt: now,
		}
		existed, err := a.persistence.EnsureNonce(ctx, record)
		if err != nil {
			return false, fmt.Errorf("persist nonce: %w", err)
		}
		if existed {
			cache.Add(composite, now)
			return true, nil
		}
	}
	cache.Add(composite, now)
	return false, nil
}

func (a *Authenticator) prunePersistent(ctx context.Context, now time.Time) error {
	if a.persistence == nil || a.nonceTTL <= 0 {
		return nil
	}
	cutoff := now.Add(-a.nonceTTL)
	if a.lastPruned.IsZero() || now.Sub(a.lastPruned) >= persistencePruneInterval {
		if err := a.persistence.PruneNonces(ctx, cutoff); err != nil {
			return fmt.Errorf("prune persistent nonces: %w", err)
		}
		a.lastPruned = now
	}
	return nil
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
	cache = newNonceStore(a.nonceTTL, a.nonceCapacity)
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
	ttl      time.Duration
	capacity int

	mu      sync.Mutex
	entries map[string]*list.Element
	order   *list.List
}

type nonceEntry struct {
	key string
	ts  time.Time
}

func newNonceStore(ttl time.Duration, capacity int) *nonceStore {
	if ttl <= 0 {
		ttl = defaultNonceWindow
	}
	if ttl > maxNonceWindow {
		ttl = maxNonceWindow
	}
	if capacity <= 0 {
		capacity = defaultNonceCapacity
	}
	if capacity > maxNonceCapacity {
		capacity = maxNonceCapacity
	}
	return &nonceStore{
		ttl:      ttl,
		capacity: capacity,
		entries:  make(map[string]*list.Element),
		order:    list.New(),
	}
}

// Seen returns true if the provided nonce has already been observed within the TTL window.
func (n *nonceStore) Seen(key string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.evictExpired(now.Add(-n.ttl))
	if _, exists := n.entries[key]; exists {
		return true
	}
	n.insertLocked(key, now)
	return false
}

// Contains reports whether the nonce has been observed without mutating the cache when new.
func (n *nonceStore) Contains(key string, now time.Time) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.evictExpired(now.Add(-n.ttl))
	_, exists := n.entries[key]
	return exists
}

// Add registers a nonce in the cache, applying eviction as required.
func (n *nonceStore) Add(key string, now time.Time) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.evictExpired(now.Add(-n.ttl))
	n.insertLocked(key, now)
}

func (n *nonceStore) insertLocked(key string, now time.Time) {
	if elem, exists := n.entries[key]; exists {
		elem.Value = nonceEntry{key: key, ts: now}
		n.order.MoveToBack(elem)
		return
	}
	if n.capacity > 0 {
		for n.order.Len() >= n.capacity {
			n.evictFront()
		}
	}
	elem := n.order.PushBack(nonceEntry{key: key, ts: now})
	n.entries[key] = elem
}

func (n *nonceStore) evictExpired(cutoff time.Time) {
	for {
		front := n.order.Front()
		if front == nil {
			return
		}
		entry := front.Value.(nonceEntry)
		if !entry.ts.Before(cutoff) {
			return
		}
		n.order.Remove(front)
		delete(n.entries, entry.key)
	}
}

func (n *nonceStore) evictFront() {
	front := n.order.Front()
	if front == nil {
		return
	}
	entry := front.Value.(nonceEntry)
	n.order.Remove(front)
	delete(n.entries, entry.key)
}
