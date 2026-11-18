package p2p

import (
	"bytes"
	"errors"
	"testing"
)

func TestSendToReachesTheNamedPeerOnly(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xC1}, 32)))
	target := registerTestPeer(t, server, "target", false)
	bystander := registerTestPeer(t, server, "bystander", false)

	msg := &Message{Type: MsgTypeStatus, Payload: []byte(`{"height":7}`)}
	if err := server.SendTo("target", msg); err != nil {
		t.Fatalf("send to a connected peer: %v", err)
	}
	if got := len(target.outbound); got != 1 {
		t.Fatalf("expected one queued message for the named peer, got %d", got)
	}
	if queued := <-target.outbound; queued != msg {
		t.Fatalf("the named peer was queued a different message than the one sent")
	}
	if got := len(bystander.outbound); got != 0 {
		t.Fatalf("a message for one peer was queued for another: %d messages", got)
	}
}

func TestSendToAPeerThatIsNotConnectedDropsTheMessage(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xC2}, 32)))
	bystander := registerTestPeer(t, server, "bystander", false)

	err := server.SendTo("gone", &Message{Type: MsgTypeBlocks, Payload: []byte(`{}`)})
	if !errors.Is(err, ErrPeerUnknown) {
		t.Fatalf("expected ErrPeerUnknown for a peer that is not connected, got %v", err)
	}
	if got := len(bystander.outbound); got != 0 {
		t.Fatalf("a message for a peer that is gone must not go to the others: %d messages", got)
	}
}

func TestSendToAPeerWithAFullQueueDropsTheMessageAndKeepsTheConnection(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xC3}, 32)))
	slow := registerTestPeer(t, server, "slow", false)
	other := registerTestPeer(t, server, "other", false)

	msg := &Message{Type: MsgTypeStatus, Payload: []byte("{}")}
	for i := 0; i < 3*outboundQueueSize; i++ {
		if err := server.SendTo("slow", msg); err != nil {
			t.Fatalf("send %d: a message dropped for a full queue is not an error: %v", i, err)
		}
	}
	if got := len(slow.outbound); got != outboundQueueSize {
		t.Fatalf("expected the queue to be full (%d), got %d", outboundQueueSize, got)
	}
	select {
	case <-slow.closed:
		t.Fatalf("a full send queue must not end the peer's connection")
	default:
	}
	if !server.hasPeer("slow") {
		t.Fatalf("the peer with the full queue was removed")
	}
	if got := len(other.outbound); got != 0 {
		t.Fatalf("messages for one peer were queued for another: %d messages", got)
	}
}
