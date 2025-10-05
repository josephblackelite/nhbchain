package p2p

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NonceReplayGuard exposes the replay tracking contract used by the handshake layer.
type NonceReplayGuard interface {
	Remember(nodeID, nonce string, observedAt time.Time) bool
}

// NewNonceReplayGuard constructs a nonce replay guard with the supplied retention window.
func NewNonceReplayGuard(window time.Duration) NonceReplayGuard {
	return newNonceGuard(window)
}

type nonceGuard struct {
	window     time.Duration
	mu         sync.Mutex
	entries    map[string]*list.Element
	order      *list.List
	seen       map[string]struct{}
	maxEntries int
}

type nonceRecord struct {
	key  string
	seen time.Time
}

func newNonceGuard(window time.Duration) *nonceGuard {
	if window <= 0 {
		window = 10 * time.Minute
	}
	return &nonceGuard{
		window:     window,
		entries:    make(map[string]*list.Element),
		order:      list.New(),
		seen:       make(map[string]struct{}),
		maxEntries: defaultNonceGuardMaxEntries,
	}
}

// Remember returns false if the nonce was already observed within the guard's
// retention policies. The guard keeps a time-ordered window for eviction
// heuristics while storing cryptographic fingerprints for recent (nodeID,
// nonce) pairs. Entries are expired after the window elapses or when the
// capacity limit is exceeded.
func (g *nonceGuard) Remember(nodeID, nonce string, observedAt time.Time) bool {
	if nonce == "" {
		return false
	}
	if observedAt.IsZero() {
		observedAt = time.Now()
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	fingerprint := g.fingerprint(nodeID, nonce)
	if fingerprint == "" {
		return false
	}
	g.pruneLocked(observedAt)
	if _, exists := g.seen[fingerprint]; exists {
		if elem, ok := g.entries[fingerprint]; ok {
			if elem != nil {
				if record, ok := elem.Value.(*nonceRecord); ok && record != nil {
					record.seen = observedAt
				}
				g.order.MoveToFront(elem)
			}
		} else {
			record := &nonceRecord{key: fingerprint, seen: observedAt}
			elem := g.order.PushFront(record)
			g.entries[fingerprint] = elem
		}
		g.enforceLimitLocked()
		return false
	}
	g.seen[fingerprint] = struct{}{}
	record := &nonceRecord{key: fingerprint, seen: observedAt}
	elem := g.order.PushFront(record)
	g.entries[fingerprint] = elem
	g.enforceLimitLocked()
	return true
}

func (g *nonceGuard) pruneLocked(now time.Time) {
	if g.order == nil {
		return
	}
	threshold := now.Add(-g.window)
	for elem := g.order.Back(); elem != nil; {
		record, _ := elem.Value.(*nonceRecord)
		if record == nil {
			prev := elem.Prev()
			g.order.Remove(elem)
			elem = prev
			continue
		}
		if !record.seen.Before(threshold) {
			break
		}
		prev := elem.Prev()
		g.order.Remove(elem)
		delete(g.entries, record.key)
		delete(g.seen, record.key)
		elem = prev
	}
	g.enforceLimitLocked()
}

func (g *nonceGuard) enforceLimitLocked() {
	if g.order == nil {
		return
	}
	if g.maxEntries <= 0 {
		return
	}
	for len(g.seen) > g.maxEntries {
		elem := g.order.Back()
		if elem == nil {
			break
		}
		record, _ := elem.Value.(*nonceRecord)
		g.order.Remove(elem)
		if record != nil {
			delete(g.entries, record.key)
			delete(g.seen, record.key)
		}
	}
}

const defaultNonceGuardMaxEntries = 64 * 1024

func (g *nonceGuard) fingerprint(nodeID, nonce string) string {
	canonicalNonce, ok := canonicalizeNonce(nonce)
	if !ok {
		return ""
	}
	normalized := normalizeHex(nodeID)
	if normalized == "" {
		normalized = strings.ToLower(strings.TrimSpace(nodeID))
	}
	if normalized == "" {
		return ""
	}
	payload := normalized + ":" + canonicalNonce
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func canonicalizeNonce(nonce string) (string, bool) {
	if nonce == "" {
		return "", false
	}
	normalized := norm.NFKC.String(nonce)
	if normalized == "" {
		return "", false
	}
	cleaned := strings.Builder{}
	cleaned.Grow(len(normalized))
	for _, r := range normalized {
		if unicode.Is(unicode.Cf, r) {
			continue
		}
		cleaned.WriteRune(r)
	}
	trimmed := strings.TrimSpace(cleaned.String())
	if trimmed == "" {
		return "", false
	}
	lowered := strings.ToLower(trimmed)
	for strings.HasPrefix(lowered, "0x") {
		lowered = strings.TrimSpace(lowered[2:])
	}
	if lowered == "" {
		return "", false
	}
	if len(lowered)%2 == 1 {
		lowered = "0" + lowered
	}
	decoded, err := hex.DecodeString(lowered)
	if err != nil {
		return "", false
	}
	if len(decoded) == 0 {
		return "", false
	}
	return hex.EncodeToString(decoded), true
}
