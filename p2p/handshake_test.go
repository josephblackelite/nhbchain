package p2p

import (
	"bufio"
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHandshakeVerifySuccess(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)

	local := NewServer(handler, mustKey(t), baseConfig(genesis))
	remote := NewServer(handler, mustKey(t), baseConfig(genesis))

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if err := local.verifyHandshake(packet); err != nil {
		t.Fatalf("verify handshake: %v", err)
	}
	if packet.nodeID == "" {
		t.Fatalf("expected nodeID to be populated")
	}
	if packet.nodeID != remote.nodeID {
		t.Fatalf("expected nodeID %s got %s", remote.nodeID, packet.nodeID)
	}
}

func TestHandshakeRejectsMismatchedGenesis(t *testing.T) {
	handler := noopHandler{}
	local := NewServer(handler, mustKey(t), baseConfig(bytes.Repeat([]byte{0xAA}, 32)))
	remote := NewServer(handler, mustKey(t), baseConfig(bytes.Repeat([]byte{0xBB}, 32)))

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if err := local.verifyHandshake(packet); err == nil || !strings.Contains(err.Error(), "genesis hash mismatch") {
		t.Fatalf("expected genesis hash mismatch, got %v", err)
	}
}

func TestHandshakeRejectsMismatchedChain(t *testing.T) {
	handler := noopHandler{}
	cfgLocal := baseConfig(bytes.Repeat([]byte{0xAA}, 32))
	cfgRemote := baseConfig(bytes.Repeat([]byte{0xAA}, 32))
	cfgLocal.ChainID = 1
	cfgRemote.ChainID = 2

	local := NewServer(handler, mustKey(t), cfgLocal)
	remote := NewServer(handler, mustKey(t), cfgRemote)

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if err := local.verifyHandshake(packet); err == nil || !strings.Contains(err.Error(), "chain ID mismatch") {
		t.Fatalf("expected chain ID mismatch, got %v", err)
	}
}

func TestHandshakeRejectsTamperedSignature(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)
	local := NewServer(handler, mustKey(t), baseConfig(genesis))
	remote := NewServer(handler, mustKey(t), baseConfig(genesis))

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	sig, err := decodeHex(packet.Signature)
	if err != nil {
		t.Fatalf("decode signature: %v", err)
	}
	sig[0] ^= 0xFF
	packet.Signature = encodeHex(sig)
	if err := local.verifyHandshake(packet); err == nil || (!strings.Contains(err.Error(), "recover signature") && !strings.Contains(strings.ToLower(err.Error()), "node id")) {
		t.Fatalf("expected signature error, got %v", err)
	}
}

func TestHandshakeRejectsNodeIDTamper(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)
	local := NewServer(handler, mustKey(t), baseConfig(genesis))
	remote := NewServer(handler, mustKey(t), baseConfig(genesis))

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	packet.NodeID = "0x010203"
	if err := local.verifyHandshake(packet); err == nil || (!strings.Contains(err.Error(), "recover signature") && !strings.Contains(strings.ToLower(err.Error()), "node id")) {
		t.Fatalf("expected node ID failure, got %v", err)
	}
}

func TestHandshakeNonceReplay(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)
	local := NewServer(handler, mustKey(t), baseConfig(genesis))
	remote := NewServer(handler, mustKey(t), baseConfig(genesis))

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if err := local.verifyHandshake(packet); err != nil {
		t.Fatalf("first verify: %v", err)
	}
	if err := local.verifyHandshake(packet); err == nil || !strings.Contains(err.Error(), "nonce replay") {
		t.Fatalf("expected nonce replay error, got %v", err)
	}
}

func TestHandshakeTimeout(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)
	cfg := baseConfig(genesis)
	cfg.HandshakeTimeout = 50 * time.Millisecond

	local := NewServer(handler, mustKey(t), cfg)

	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	errCh := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(left)
		ctx, cancel := context.WithTimeout(context.Background(), cfg.HandshakeTimeout)
		defer cancel()
		_, err := local.performHandshake(ctx, left, reader)
		errCh <- err
	}()

	reader := bufio.NewReader(right)
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatalf("read local handshake: %v", err)
	}

	err := <-errCh
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}
