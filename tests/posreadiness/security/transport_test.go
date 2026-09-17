//go:build posreadiness

package security

import "testing"

func TestPlaintextRejected(t *testing.T) {
	testPlaintextRejected(t)
}

func TestTLSAndMTLSRequired(t *testing.T) {
	testTLSAndMTLSRequired(t)
}

func TestHMACReplayBlocked(t *testing.T) {
	testHMACReplayBlocked(t)
}
