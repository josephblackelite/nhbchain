package p2p

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
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
	if err := local.verifyHandshake(packet); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("expected signature error, got %v", err)
	}
}

func TestHandshakeRejectsTimestampSkew(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)
	local := NewServer(handler, mustKey(t), baseConfig(genesis))
	remote := NewServer(handler, mustKey(t), baseConfig(genesis))

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	packet.Timestamp = packet.Timestamp - int64((handshakeSkewAllowance*2)/time.Second)
	payload, err := json.Marshal(packet.handshakeMessage)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	digest := handshakeDigest(payload, packet.Timestamp)
	sig, err := ethcrypto.Sign(digest, remote.privKey.PrivateKey)
	if err != nil {
		t.Fatalf("resign handshake: %v", err)
	}
	packet.Signature = encodeHex(sig)
	if err := local.verifyHandshake(packet); err == nil || !strings.Contains(err.Error(), "timestamp") {
		t.Fatalf("expected timestamp error, got %v", err)
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
