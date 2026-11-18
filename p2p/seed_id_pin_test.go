package p2p

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// PL-R5T-60 (1): a seed is configured with the node ID it should have, but the connection manager
// dialed it by address and the ID the handshake proved was never compared with it, so whoever
// answered at a seed's address (a reassigned address, a hijacked route, an old DNS record) was taken
// for the seed.

func seedPinPair(t *testing.T) (dialer, seed *Server, seedAddr string) {
	t.Helper()
	dialer, _ = startChallengeServer(t, "auto")
	seed, seedAddr = startChallengeServer(t, "auto")
	return dialer, seed, seedAddr
}

func TestSeedPin_AConnectionToAnotherNodeIsRefused(t *testing.T) {
	dialer, seed, seedAddr := seedPinPair(t)
	wrong := "0x" + strings.Repeat("ab", 64)

	err := dialer.ConnectExpecting(seedAddr, wrong)
	if err == nil {
		t.Fatalf("a node whose handshake proved another ID was taken for the seed")
	}
	if !errors.Is(err, ErrPeerIDMismatch) {
		t.Fatalf("want ErrPeerIDMismatch, got %v", err)
	}
	if dialer.hasPeer(seed.nodeID) {
		t.Fatalf("the dialer kept a connection to a node that is not the seed")
	}
	// Neither ID is held against: the node that answered did nothing wrong, and the ID in the seed
	// list is only a string somebody configured.
	requireNothingHeldAgainst(t, dialer, nil, seed.nodeID)
	requireNothingHeldAgainst(t, dialer, nil, normalizeHex(wrong))
}

func TestSeedPin_TheRightIDConnects(t *testing.T) {
	for name, spell := range map[string]func(string) string{
		"as the node prints it": func(id string) string { return id },
		"without the prefix":    func(id string) string { return strings.TrimPrefix(id, "0x") },
		"in upper case":         func(id string) string { return "0x" + strings.ToUpper(strings.TrimPrefix(id, "0x")) },
	} {
		t.Run(name, func(t *testing.T) {
			dialer, seed, seedAddr := seedPinPair(t)
			if err := dialer.ConnectExpecting(seedAddr, spell(seed.nodeID)); err != nil {
				t.Fatalf("the seed itself was refused: %v", err)
			}
			waitUntil(t, 3*time.Second, "the connection", func() bool { return dialer.hasPeer(seed.nodeID) })
		})
	}
}

func TestSeedPin_WithoutAnIDTheDialIsAsBefore(t *testing.T) {
	dialer, seed, seedAddr := seedPinPair(t)
	if err := dialer.Connect(seedAddr); err != nil {
		t.Fatalf("a dial that names no ID must connect to whoever answers: %v", err)
	}
	waitUntil(t, 3*time.Second, "the connection", func() bool { return dialer.hasPeer(seed.nodeID) })
	if err := dialer.ConnectExpecting(seedAddr, ""); err != nil {
		t.Fatalf("an empty expected ID must be no pin: %v", err)
	}
}

// Through the connection manager: a seed entry with the wrong ID never gets connected, one with the
// right ID does.
func TestSeedPin_TheSeedLoopPinsTheConfiguredID(t *testing.T) {
	seed, seedAddr := startChallengeServer(t, "auto")

	run := func(t *testing.T, seedID string) *Server {
		cfg := baseConfig(challengeGenesis)
		cfg.HandshakeTimeout = 2 * time.Second
		cfg.ReadTimeout = 30 * time.Second
		cfg.WriteTimeout = 5 * time.Second
		cfg.Seeds = []string{seedID + "@" + seedAddr}
		cfg.DialBackoff = 50 * time.Millisecond
		cfg.MaxDialBackoff = 100 * time.Millisecond
		server := NewServer(noopHandler{}, mustKey(t), cfg)
		go server.Start()
		t.Cleanup(func() { server.Stop() })
		return server
	}

	good := run(t, seed.nodeID)
	waitUntil(t, 5*time.Second, "the seed loop to reach the seed by its ID", func() bool { return good.hasPeer(seed.nodeID) })

	bad := run(t, "0x"+strings.Repeat("cd", 64))
	time.Sleep(1500 * time.Millisecond)
	if bad.hasPeer(seed.nodeID) {
		t.Fatalf("the seed loop connected to a node that does not have the configured seed ID")
	}
}
