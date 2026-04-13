package netsec

import (
	"strings"
	"testing"
	"time"

	"nhbchain/p2p"
)

func TestHandshakeNonceCanonicalReplay(t *testing.T) {
	guard := p2p.NewNonceReplayGuard(time.Minute)
	nodeID := "0x1234567890abcdef1234567890abcdef12345678"
	canonical := "0x0abc1234def5678900fedcba"

	if !guard.Remember(nodeID, canonical, time.Now()) {
		t.Fatalf("expected canonical nonce %q to be accepted", canonical)
	}

	variants := map[string]string{
		"upper":      strings.ToUpper(canonical),
		"no-prefix":  canonical[2:],
		"odd-length": "0x" + canonical[3:],
		"zero-width": "0x0\u200b" + canonical[3:],
		"unicode":    fullWidthHex(canonical),
	}

	now := time.Now().Add(time.Millisecond)
	for name, value := range variants {
		if guard.Remember(nodeID, value, now) {
			t.Fatalf("expected %s variant %q to be treated as a replay", name, value)
		}
		now = now.Add(time.Millisecond)
	}
}

func fullWidthHex(input string) string {
	var builder strings.Builder
	builder.Grow(len(input))
	for _, r := range strings.ToLower(input) {
		switch {
		case r >= '0' && r <= '9':
			builder.WriteRune('０' + (r - '0'))
		case r >= 'a' && r <= 'f':
			builder.WriteRune('ａ' + (r - 'a'))
		case r == 'x':
			builder.WriteRune('ｘ')
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}
