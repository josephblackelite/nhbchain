package p2p

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

// frameSize must be exactly the length of the JSON the write loop produces, or
// the limit it enforces would be off by the difference.
func TestFrameSizeMatchesTheEncodedMessage(t *testing.T) {
	payloads := [][]byte{nil, {}, {1}, {1, 2}, {1, 2, 3}, {1, 2, 3, 4}, bytes.Repeat([]byte{0xFF}, 1000), bytes.Repeat([]byte{0x7}, 4096+1)}
	for _, msgType := range []byte{0, 1, 9, 10, 99, 100, 255} {
		for _, payload := range payloads {
			msg := &Message{Type: msgType, Payload: payload}
			encoded, err := json.Marshal(msg)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if got := frameSize(msg); got != len(encoded) {
				t.Fatalf("type %d, %d payload bytes: frameSize %d, encoded length %d", msgType, len(payload), got, len(encoded))
			}
		}
	}
}

// The limit is set to the frame of a 3000 byte payload, so that one message is
// exactly at the limit and a payload one byte longer is the smallest that is
// over it (base64 grows in steps of four).
const limitTestPayload = 3000

func limitTestConfig(genesisByte byte) ServerConfig {
	cfg := baseConfig(bytes.Repeat([]byte{genesisByte}, 32))
	cfg.MaxMessageBytes = frameSize(&Message{Type: MsgTypeBlocks, Payload: make([]byte, limitTestPayload)})
	return cfg
}

func TestEnqueueRefusesAFrameLargerThanTheLimitAndKeepsThePeer(t *testing.T) {
	cfg := limitTestConfig(0xC1)
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	peer := registerTestPeer(t, server, "reader", false)

	atLimit := &Message{Type: MsgTypeBlocks, Payload: make([]byte, limitTestPayload)}
	if err := peer.Enqueue(atLimit); err != nil {
		t.Fatalf("a frame of exactly the limit is accepted by the receiver and must be queued: %v", err)
	}
	over := &Message{Type: MsgTypeBlocks, Payload: make([]byte, limitTestPayload+1)}
	err := peer.Enqueue(over)
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("expected ErrMessageTooLarge for a frame over the limit, got %v", err)
	}
	if got := len(peer.outbound); got != 1 {
		t.Fatalf("only the frame within the limit may be queued, %d queued", got)
	}
	if got := peer.queuedBytes.Load(); got != int64(len(atLimit.Payload)) {
		t.Fatalf("the refused message must not be counted as queued: %d bytes counted", got)
	}
	select {
	case <-peer.closed:
		t.Fatalf("refusing to send a message must not end the connection")
	default:
	}
	if !server.hasPeer("reader") {
		t.Fatalf("the peer was removed")
	}
}

func TestBroadcastDoesNotSendOrDisconnectOverAFrameTheReceiverWouldRefuse(t *testing.T) {
	cfg := limitTestConfig(0xC2)
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	first := registerTestPeer(t, server, "first", false)
	second := registerTestPeer(t, server, "second", true)

	over := &Message{Type: MsgTypeBlock, Payload: make([]byte, limitTestPayload+1)}
	err := server.Broadcast(over)
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("expected the broadcast to report ErrMessageTooLarge, got %v", err)
	}
	for _, peer := range []*Peer{first, second} {
		if got := len(peer.outbound); got != 0 {
			t.Fatalf("%s was queued %d messages of a frame no receiver accepts", peer.id, got)
		}
		select {
		case <-peer.closed:
			t.Fatalf("%s was disconnected for a message this node refused to send", peer.id)
		default:
		}
		if !server.hasPeer(peer.id) {
			t.Fatalf("%s was removed", peer.id)
		}
	}
	// The refusal costs the message and nothing else: the next one is delivered.
	small := &Message{Type: MsgTypeTx, Payload: []byte("{}")}
	if err := server.Broadcast(small); err != nil {
		t.Fatalf("broadcast after the refusal: %v", err)
	}
	for _, peer := range []*Peer{first, second} {
		if got := len(peer.outbound); got != 1 {
			t.Fatalf("%s should hold the one message that fits, holds %d", peer.id, got)
		}
	}
}
