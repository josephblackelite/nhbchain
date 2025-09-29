package p2p

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

type invalidPayloadHandler struct{}

func (invalidPayloadHandler) HandleMessage(msg *Message) error {
	return fmt.Errorf("%w: invalid", ErrInvalidPayload)
}

func TestInvalidPayloadDisconnectsPeer(t *testing.T) {
	handler := invalidPayloadHandler{}
	genesis := bytes.Repeat([]byte{0xAB}, 32)
	cfg := baseConfig(genesis)
	cfg.PeerBanDuration = 100 * time.Millisecond
	cfg.RateMsgsPerSec = 10
	cfg.RateBurst = 10

	server := NewServer(handler, mustKey(t), cfg)
	remote := NewServer(noopHandler{}, mustKey(t), cfg)

	left, right := net.Pipe()
	defer right.Close()

	go server.handleInbound(left)

	reader := bufio.NewReader(right)
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatalf("read local handshake: %v", err)
	}
	payload, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal handshake: %v", err)
	}
	if _, err := right.Write(append(data, '\n')); err != nil {
		t.Fatalf("write handshake: %v", err)
	}

	wait := func(cond func() bool) bool {
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			if cond() {
				return true
			}
			time.Sleep(10 * time.Millisecond)
		}
		return cond()
	}

	if !wait(func() bool {
		server.mu.RLock()
		_, ok := server.peers[remote.nodeID]
		server.mu.RUnlock()
		return ok
	}) {
		t.Fatal("peer never registered after handshake")
	}

	msgData, err := json.Marshal(&Message{Type: MsgTypeTx, Payload: []byte("{}")})
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	if _, err := right.Write(append(msgData, '\n')); err != nil {
		if !strings.Contains(err.Error(), "closed") {
			t.Fatalf("write invalid message: %v", err)
		}
	}

	if !wait(func() bool {
		server.mu.RLock()
		_, ok := server.peers[remote.nodeID]
		server.mu.RUnlock()
		return !ok
	}) {
		t.Fatal("peer should have been disconnected after invalid payload")
	}

	statuses := server.reputation.Snapshot(server.now())
	status, ok := statuses[remote.nodeID]
	if !ok {
		t.Fatal("expected reputation entry for remote peer")
	}
	if status.Score >= 0 {
		t.Fatalf("expected negative score after invalid payload, got %d", status.Score)
	}
	if status.Misbehavior == 0 {
		t.Fatalf("expected misbehavior count to increase")
	}
}
