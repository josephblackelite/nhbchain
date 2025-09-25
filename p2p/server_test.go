package p2p

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

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

func baseConfig(genesis []byte) ServerConfig {
	return ServerConfig{
		ListenAddress:    "127.0.0.1:0",
		ChainID:          1,
		GenesisHash:      genesis,
		ClientVersion:    "test/1.0",
		MaxPeers:         8,
		MaxInbound:       8,
		MaxOutbound:      8,
		PeerBanDuration:  time.Second,
		ReadTimeout:      250 * time.Millisecond,
		WriteTimeout:     250 * time.Millisecond,
		MaxMessageBytes:  1 << 20,
		RateMsgsPerSec:   2,
		RateBurst:        4,
		BanScore:         20,
		GreyScore:        10,
		HandshakeTimeout: time.Second,
	}
}

func TestPeerRateLimitDisconnect(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)

	cfg := baseConfig(genesis)
	cfg.RateMsgsPerSec = 1
	cfg.PeerBanDuration = 100 * time.Millisecond

	server := NewServer(handler, mustKey(t), cfg)
	remote := NewServer(handler, mustKey(t), cfg)

	left, right := net.Pipe()
	defer right.Close()

	go server.handleInbound(left)

	reader := bufio.NewReader(right)
	// read local handshake
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatalf("read local handshake: %v", err)
	}
	// respond with remote handshake
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

	// flood messages to trigger the rate limit
	msgData, err := json.Marshal(&Message{Type: 1, Payload: []byte("spam")})
	if err != nil {
		t.Fatalf("marshal message: %v", err)
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

	for i := 0; i < 5; i++ {
		if _, err := right.Write(append(msgData, '\n')); err != nil {
			if strings.Contains(err.Error(), "closed") {
				break
			}
			t.Fatalf("write message: %v", err)
		}
	}

	if !wait(func() bool {
		server.mu.RLock()
		_, ok := server.peers[remote.nodeID]
		server.mu.RUnlock()
		return !ok
	}) {
		t.Fatal("peer should have been dropped after rate limiting")
	}
}

func TestServerBootnodeDialing(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAA}, 32)

	cfg := baseConfig(genesis)
	cfg.Bootnodes = []string{"1.2.3.4:6001", "5.6.7.8:7001"}
	cfg.PersistentPeers = []string{"9.9.9.9:9001"}

	server := NewServer(handler, mustKey(t), cfg)

	var mu sync.Mutex
	seen := make(map[string]int)
	server.dialFn = func(ctx context.Context, addr string) (net.Conn, error) {
		mu.Lock()
		seen[addr]++
		mu.Unlock()
		return nil, fmt.Errorf("dial blocked")
	}

	server.startDialers()
	time.Sleep(50 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("expected dial attempts for bootnodes and persistent peers")
	}
	var addrs []string
	for addr := range seen {
		addrs = append(addrs, addr)
	}
	sort.Strings(addrs)
	expected := []string{"1.2.3.4:6001", "5.6.7.8:7001", "9.9.9.9:9001"}
	for _, addr := range expected {
		if _, ok := seen[addr]; !ok {
			t.Fatalf("expected dial attempt to %s, got %v", addr, addrs)
		}
	}
}

func TestSeedDialerRecordsFailures(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xEE}, 32)

	cfg := baseConfig(genesis)
	cfg.Seeds = []string{"0xdeadbeef@127.0.0.1:4000"}

	server := NewServer(handler, mustKey(t), cfg)

	dir := t.TempDir()
	store, err := NewPeerstore(filepath.Join(dir, "peers.db"), 10*time.Millisecond, time.Second)
	if err != nil {
		t.Fatalf("create peerstore: %v", err)
	}
	defer store.Close()
	server.SetPeerstore(store)

	server.dialFn = func(ctx context.Context, addr string) (net.Conn, error) {
		return nil, fmt.Errorf("dial blocked")
	}

	server.startConnManager()
	defer func() {
		if server.connMgr != nil {
			server.connMgr.stop()
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec, ok := store.ByNodeID("0xdeadbeef")
		if ok && rec.Fails > 0 {
			if rec.LastSeen.IsZero() {
				t.Fatalf("lastSeen not updated: %+v", rec)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, ok := store.ByNodeID("0xdeadbeef")
	if !ok {
		t.Fatalf("seed record missing")
	}
	t.Fatalf("expected failure recorded, got %+v", rec)
}

func TestSeedDialerSuccessResetsFails(t *testing.T) {
	handler := noopHandler{}
	genesis := bytes.Repeat([]byte{0xAB}, 32)

	cfg := baseConfig(genesis)
	remote := NewServer(handler, mustKey(t), cfg)

	cfg.Seeds = []string{fmt.Sprintf("%s@127.0.0.1:4555", remote.nodeID)}

	server := NewServer(handler, mustKey(t), cfg)

	dir := t.TempDir()
	store, err := NewPeerstore(filepath.Join(dir, "peers.db"), 10*time.Millisecond, time.Second)
	if err != nil {
		t.Fatalf("create peerstore: %v", err)
	}
	defer store.Close()
	server.SetPeerstore(store)

	var mu sync.Mutex
	attempts := 0
	server.dialFn = func(ctx context.Context, addr string) (net.Conn, error) {
		mu.Lock()
		attempts++
		current := attempts
		mu.Unlock()
		if current == 1 {
			return nil, fmt.Errorf("temporary failure")
		}
		local, remoteConn := net.Pipe()
		go func() {
			defer remoteConn.Close()
			reader := bufio.NewReader(remoteConn)
			if _, err := reader.ReadBytes('\n'); err != nil {
				return
			}
			payload, err := remote.buildHandshake()
			if err != nil {
				return
			}
			data, err := json.Marshal(payload)
			if err != nil {
				return
			}
			_, _ = remoteConn.Write(append(data, '\n'))
			time.Sleep(20 * time.Millisecond)
		}()
		return local, nil
	}

	server.startConnManager()
	defer func() {
		if server.connMgr != nil {
			server.connMgr.stop()
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec, ok := store.ByNodeID(remote.nodeID)
		if ok && rec.Fails == 0 && !rec.LastSeen.IsZero() {
			mu.Lock()
			done := attempts >= 2
			mu.Unlock()
			if done {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	rec, ok := store.ByNodeID(remote.nodeID)
	if !ok {
		t.Fatalf("seed record missing")
	}
	t.Fatalf("expected success to reset fails, got %+v after %d attempts", rec, attempts)
}
