package p2p

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHandshakeVerifySuccess(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)

	local := NewServer(handler, mustKey(t), baseConfig(genesis))
	remote := NewServer(handler, mustKey(t), baseConfig(genesis))

	remote.addListenAddress("127.0.0.1:34567")

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if err := local.verifyHandshake(packet); err != nil {
		t.Fatalf("verify handshake: %v", err)
	}
	nonceBytes, err := decodeHex(packet.Nonce)
	if err != nil {
		t.Fatalf("decode nonce: %v", err)
	}
	if len(nonceBytes) != handshakeNonceSize {
		t.Fatalf("expected nonce length %d got %d", handshakeNonceSize, len(nonceBytes))
	}
	if packet.nodeID == "" {
		t.Fatalf("expected nodeID to be populated")
	}
	if packet.nodeID != remote.nodeID {
		t.Fatalf("expected nodeID %s got %s", remote.nodeID, packet.nodeID)
	}
	if len(packet.ListenAddrs) == 0 || packet.ListenAddrs[0] != "127.0.0.1:34567" {
		t.Fatalf("expected listen address to be sanitized, got %v", packet.ListenAddrs)
	}
	if len(packet.addrs) != len(packet.ListenAddrs) {
		t.Fatalf("expected cached listen addrs to mirror payload")
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
	if !local.isBanned(normalizeHex(packet.NodeID)) {
		t.Fatalf("expected peer to be banned after genesis mismatch")
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
	if !local.isBanned(normalizeHex(packet.NodeID)) {
		t.Fatalf("expected peer to be banned after chain mismatch")
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
	if !local.isBanned(normalizeHex(packet.NodeID)) {
		t.Fatalf("expected peer to be banned after signature mismatch")
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
	if !local.isBanned(normalizeHex(packet.NodeID)) {
		t.Fatalf("expected peer to be banned after node ID tamper")
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
	if !local.isBanned(normalizeHex(packet.NodeID)) {
		t.Fatalf("expected peer to be banned after nonce replay")
	}
}

func TestHandshakeNonceReplayAfterWindow(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)
	local := NewServer(handler, mustKey(t), baseConfig(genesis))
	remote := NewServer(handler, mustKey(t), baseConfig(genesis))

	local.nonceGuard = newNonceGuard(5 * time.Millisecond)
	fakeNow := time.Unix(1000, 0)
	local.now = func() time.Time { return fakeNow }

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if err := local.verifyHandshake(packet); err != nil {
		t.Fatalf("first verify: %v", err)
	}

	fakeNow = fakeNow.Add(time.Second)

	if err := local.verifyHandshake(packet); err != nil {
		t.Fatalf("expected nonce to be accepted after window expiry, got %v", err)
	}
	if local.isBanned(normalizeHex(packet.NodeID)) {
		t.Fatalf("expected peer not to be banned after nonce reuse beyond window")
	}
}

func TestHandshakeViolationPersistsToPeerstore(t *testing.T) {
	handler := noopHandler{}
	localGenesis := bytes.Repeat([]byte{0xAA}, 32)
	remoteGenesis := bytes.Repeat([]byte{0xBB}, 32)

	local := NewServer(handler, mustKey(t), baseConfig(localGenesis))
	store := newTestPeerstore(t)
	local.SetPeerstore(store)
	fakeNow := time.Unix(100, 0)
	local.now = func() time.Time { return fakeNow }

	remote := NewServer(handler, mustKey(t), baseConfig(remoteGenesis))

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if err := local.verifyHandshake(packet); err == nil {
		t.Fatalf("expected verification to fail")
	}
	normalized := normalizeHex(packet.NodeID)
	entry, ok := store.ByNodeID(normalized)
	if !ok {
		t.Fatalf("expected peerstore entry to be created")
	}
	if entry.Violations != 1 {
		t.Fatalf("expected violation count 1 got %d", entry.Violations)
	}
	if entry.Score != clampScore(-violationScorePenalty) {
		t.Fatalf("expected score penalty applied got %v", entry.Score)
	}
	if !entry.LastViolation.Equal(fakeNow) {
		t.Fatalf("expected last violation timestamp recorded")
	}
	if !store.IsBanned(normalized, fakeNow.Add(500*time.Millisecond)) {
		t.Fatalf("expected peerstore ban to be recorded")
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
	if err == nil {
		t.Fatalf("expected timeout error, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "context deadline exceeded") && !strings.Contains(msg, "i/o timeout") {
		t.Fatalf("expected timeout error, got %v", err)
	}
}

func TestHandshakeRejectsOversizedFrame(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)
	cfg := baseConfig(genesis)
	cfg.MaxMessageBytes = 64

	local := NewServer(handler, mustKey(t), cfg)

	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()

	counting := &countingConn{Conn: left}

	errCh := make(chan error, 1)
	go func() {
		reader := bufio.NewReaderSize(counting, 1)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, err := local.performHandshake(ctx, counting, reader)
		errCh <- err
	}()

	remoteReader := bufio.NewReader(right)
	if _, err := remoteReader.ReadBytes('\n'); err != nil {
		t.Fatalf("read local handshake: %v", err)
	}

	oversized := bytes.Repeat([]byte("a"), cfg.MaxMessageBytes+1)
	if _, err := right.Write(oversized); err != nil {
		t.Fatalf("write oversized handshake: %v", err)
	}

	err := <-errCh
	if err == nil || !errors.Is(err, errHandshakeFrameTooLarge) {
		t.Fatalf("expected oversized frame error, got %v", err)
	}

	if got := counting.read.Load(); got > int64(cfg.MaxMessageBytes+1) {
		t.Fatalf("expected to read at most %d bytes, got %d", cfg.MaxMessageBytes+1, got)
	}
}

type countingConn struct {
	net.Conn
	read atomic.Int64
}

func (c *countingConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.read.Add(int64(n))
	return n, err
}
