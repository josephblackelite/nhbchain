package p2p

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

func FuzzCanonicalizeNonce(f *testing.F) {
	const canonical = "0x0abc1234def5678900fedcba"

	f.Add("")
	f.Add(" \t\n")
	f.Add(canonical)
	f.Add(strings.ToUpper(canonical))
	f.Add(canonical[2:])
	f.Add("0x" + canonical[3:])
	f.Add("0x0\u200b" + canonical[3:])
	f.Add(fullWidthHex(canonical))

	nodeID := "0x1234567890abcdef1234567890abcdef12345678"

	f.Fuzz(func(t *testing.T, input string) {
		guard := newNonceGuard(time.Minute)
		t.Cleanup(guard.Close)

		canonicalNonce, ok := canonicalizeNonce(input)

		accepted := guard.Remember(nodeID, input, time.Time{})

		if !ok {
			if accepted {
				t.Fatalf("expected invalid nonce %q to be rejected", input)
			}
			return
		}

		if canonicalNonce == "" {
			t.Fatalf("canonical nonce must be non-empty")
		}
		if canonicalNonce != strings.ToLower(canonicalNonce) {
			t.Fatalf("canonical nonce %q must be lowercase", canonicalNonce)
		}
		if len(canonicalNonce)%2 != 0 {
			t.Fatalf("canonical nonce %q must have even length", canonicalNonce)
		}
		if strings.TrimSpace(canonicalNonce) != canonicalNonce {
			t.Fatalf("canonical nonce %q must not contain leading or trailing whitespace", canonicalNonce)
		}
		if strings.HasPrefix(canonicalNonce, "0x") {
			t.Fatalf("canonical nonce %q must not retain hex prefix", canonicalNonce)
		}
		if _, err := hex.DecodeString(canonicalNonce); err != nil {
			t.Fatalf("canonical nonce %q must be valid hex: %v", canonicalNonce, err)
		}

		roundTrip, roundTripOK := canonicalizeNonce(canonicalNonce)
		if !roundTripOK {
			t.Fatalf("canonical nonce %q must re-canonicalize", canonicalNonce)
		}
		if roundTrip != canonicalNonce {
			t.Fatalf("canonical nonce %q must be idempotent, got %q", canonicalNonce, roundTrip)
		}

		if !accepted {
			t.Fatalf("expected nonce %q to be accepted on first observation", input)
		}

		if guard.Remember(nodeID, canonicalNonce, time.Now()) {
			t.Fatalf("expected canonical nonce %q to be treated as a replay", canonicalNonce)
		}

		if guard.Remember(nodeID, input, time.Now()) {
			t.Fatalf("expected original nonce %q to be treated as a replay", input)
		}
	})
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
