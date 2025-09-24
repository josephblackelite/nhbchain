package p2p

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"strings"
	"testing"

	"nhbchain/crypto"
)

type noopHandler struct{}

func (noopHandler) HandleMessage(msg *Message) error { return nil }

func mustKey(t *testing.T) *crypto.PrivateKey {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func TestHandshakeRejectsMismatchedGenesis(t *testing.T) {
	handler := noopHandler{}
	local := NewServer("127.0.0.1:0", handler, mustKey(t), 1, bytes.Repeat([]byte{0xAA}, 32), "alpha")
	remote := NewServer("127.0.0.1:0", handler, mustKey(t), 1, bytes.Repeat([]byte{0xBB}, 32), "alpha")

	msg, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if _, err := local.verifyHandshake(msg); err == nil || !strings.Contains(err.Error(), "genesis hash mismatch") {
		t.Fatalf("expected genesis hash mismatch error, got %v", err)
	}
}

func TestHandshakeRejectsMismatchedNetwork(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)
	local := NewServer("127.0.0.1:0", handler, mustKey(t), 1, genesis, "alpha")
	remote := NewServer("127.0.0.1:0", handler, mustKey(t), 1, genesis, "beta")

	msg, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if _, err := local.verifyHandshake(msg); err == nil || !strings.Contains(err.Error(), "network name mismatch") {
		t.Fatalf("expected network name mismatch error, got %v", err)
	}
}

func TestPeerBanOnInvalidMessageRate(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)
	server := NewServer("127.0.0.1:0", handler, mustKey(t), 1, genesis, "alpha")

	peerID := "peer-ban-test"
	for i := 0; i < invalidRateSampleSize; i++ {
		left, right := net.Pipe()
		reader := bufio.NewReader(left)
		peer := newPeer(peerID, left, reader, server)
		if err := server.registerPeer(peer); err != nil {
			t.Fatalf("register peer: %v", err)
		}
		server.handleProtocolViolation(peer, fmt.Errorf("invalid message"))
		if i < invalidRateSampleSize-1 {
			server.adjustReputation(peerID, malformedPenalty)
		}
		right.Close()
		if i < invalidRateSampleSize-1 && server.isBanned(peerID) {
			t.Fatalf("peer banned too early at iteration %d", i)
		}
	}

	if !server.isBanned(peerID) {
		t.Fatalf("expected peer to be banned after %d invalid messages", invalidRateSampleSize)
	}
}
