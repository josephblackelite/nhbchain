package p2p

import (
	"encoding/hex"
	"strings"
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
