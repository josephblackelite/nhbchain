package integration

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/crypto"
	"nhbchain/p2p"
)

type meshHandler struct{}

func (meshHandler) HandleMessage(msg *p2p.Message) error { return nil }

type testNode struct {
	server *p2p.Server
	store  *p2p.Peerstore
}

func TestMiniMeshIntegration(t *testing.T) {
	genesis := bytes.Repeat([]byte{0xAB}, 32)

	base := p2p.ServerConfig{
		ChainID:          777,
		GenesisHash:      genesis,
		ClientVersion:    "mesh/test",
		MaxPeers:         8,
		MaxInbound:       8,
		MaxOutbound:      8,
		MinPeers:         3,
		OutboundPeers:    3,
		MaxMessageBytes:  1 << 16,
		RateMsgsPerSec:   64,
		RateBurst:        128,
		HandshakeTimeout: time.Second,
		DialBackoff:      50 * time.Millisecond,
		MaxDialBackoff:   500 * time.Millisecond,
		EnablePEX:        true,
	}

	key1 := mustKey(t)
	key2 := mustKey(t)
	key3 := mustKey(t)

	id1 := nodeIDFromKey(key1)
	id2 := nodeIDFromKey(key2)
	id3 := nodeIDFromKey(key3)

	n1Cfg := base
	n1Cfg.ListenAddress = "127.0.0.1:37651"
	n1Cfg.Seeds = []string{
		fmt.Sprintf("%s@%s", id2, "127.0.0.1:37652"),
		fmt.Sprintf("%s@%s", id3, "127.0.0.1:37653"),
	}
	n1 := newServerWithKey(t, "n1", n1Cfg, key1)

	n2Cfg := base
	n2Cfg.ListenAddress = "127.0.0.1:37652"
	n2Cfg.ClientVersion = "mesh/test-n2"
	n2Cfg.Seeds = []string{fmt.Sprintf("%s@%s", id1, n1Cfg.ListenAddress)}
	n2 := newServerWithKey(t, "n2", n2Cfg, key2)

	n3Cfg := base
	n3Cfg.ListenAddress = "127.0.0.1:37653"
	n3Cfg.ClientVersion = "mesh/test-n3"
	n3Cfg.Seeds = []string{fmt.Sprintf("%s@%s", id1, n1Cfg.ListenAddress)}
	n3 := newServerWithKey(t, "n3", n3Cfg, key3)

	startServer(t, "n1", n1)
	waitForListen(t, n1.server, n1Cfg.ListenAddress)
	startServer(t, "n2", n2)
	waitForListen(t, n2.server, n2Cfg.ListenAddress)
	startServer(t, "n3", n3)
	waitForListen(t, n3.server, n3Cfg.ListenAddress)

	waitForPeer(t, n1.server, n2.server.NodeID())
	waitForPeer(t, n1.server, n3.server.NodeID())
	waitForPeer(t, n2.server, n1.server.NodeID())
	waitForPeer(t, n3.server, n1.server.NodeID())

	discoverPeerViaPEX(t, n2, n3.server.NodeID())
	discoverPeerViaPEX(t, n3, n2.server.NodeID())

	waitForNetPeer(t, n2.server, n3.server.NodeID())
	waitForNetPeer(t, n3.server, n2.server.NodeID())

	wrongCfg := base
	wrongCfg.ListenAddress = "127.0.0.1:37654"
	wrongCfg.ClientVersion = "mesh/test-wrong"
	wrongCfg.ChainID = base.ChainID + 1
	wrongKey := mustKey(t)
	wrong := newServerWithKey(t, "wrong", wrongCfg, wrongKey)
	t.Cleanup(func() {
		if wrong.store != nil {
			_ = wrong.store.Close()
		}
	})

	if err := wrong.server.Connect(n1Cfg.ListenAddress); err == nil || !containsChainMismatch(err) {
		t.Fatalf("expected chain mismatch during handshake, got %v", err)
	}

	ensureNoPeer(t, n1.server, wrong.server.NodeID())
}

func newServer(t *testing.T, name string, cfg p2p.ServerConfig) *testNode {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return newServerWithKey(t, name, cfg, key)
}

func newServerWithKey(t *testing.T, name string, cfg p2p.ServerConfig, key *crypto.PrivateKey) *testNode {
	t.Helper()
	server := p2p.NewServer(meshHandler{}, key, cfg)
	dir := t.TempDir()
	store, err := p2p.NewPeerstore(filepath.Join(dir, name+"-peers.db"), 20*time.Millisecond, 200*time.Millisecond)
	if err != nil {
		t.Fatalf("peerstore: %v", err)
	}
	server.SetPeerstore(store)
	return &testNode{server: server, store: store}
}

func startServer(t *testing.T, name string, node *testNode) *testNode {
	t.Helper()
	if node == nil {
		t.Fatalf("startServer: node %s is nil", name)
	}
	go func() {
		if err := node.server.Start(); err != nil && !strings.Contains(err.Error(), "use of closed network connection") {
			t.Logf("server %s stopped: %v", name, err)
		}
	}()
	return node
}

func waitForListen(t *testing.T, server *p2p.Server, expect string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	target := strings.ToLower(expect)
	for time.Now().Before(deadline) {
		addrs := server.ListenAddresses()
		for _, addr := range addrs {
			if strings.ToLower(addr) == target {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("server failed to bind %s", expect)
}

func waitForPeer(t *testing.T, server *p2p.Server, nodeID string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	target := strings.ToLower(strings.TrimSpace(nodeID))
	for time.Now().Before(deadline) {
		if hasPeer(server, target) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for peer %s", nodeID)
}

func waitForNetPeer(t *testing.T, server *p2p.Server, nodeID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	target := strings.ToLower(strings.TrimSpace(nodeID))
	for time.Now().Before(deadline) {
		infos := server.NetPeers()
		for _, info := range infos {
			if strings.ToLower(strings.TrimSpace(info.NodeID)) == target && strings.EqualFold(info.State, "connected") {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("net_info missing connected peer %s", nodeID)
}

func ensureNoPeer(t *testing.T, server *p2p.Server, nodeID string) {
	t.Helper()
	peers := server.SnapshotPeers()
	target := strings.ToLower(strings.TrimSpace(nodeID))
	for _, peer := range peers {
		if strings.ToLower(strings.TrimSpace(peer.NodeID)) == target {
			t.Fatalf("unexpected peer %s registered", nodeID)
		}
	}
}

func hasPeer(server *p2p.Server, target string) bool {
	for _, peer := range server.SnapshotPeers() {
		if strings.ToLower(strings.TrimSpace(peer.NodeID)) == target {
			return true
		}
	}
	return false
}

func containsChainMismatch(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "chain ID mismatch")
}

func discoverPeerViaPEX(t *testing.T, node *testNode, targetID string) {
	t.Helper()
	requestPEXSample(t, node, targetID)
	if err := node.server.DialPeer(targetID); err != nil {
		t.Fatalf("dialing discovered peer %s failed: %v", targetID, err)
	}
	waitForPeer(t, node.server, targetID)
}

func requestPEXSample(t *testing.T, node *testNode, targetID string) {
	t.Helper()
	if node == nil || node.server == nil {
		t.Fatalf("requestPEXSample: node missing server")
	}
	tokenBase := fmt.Sprintf("mesh-%s", node.server.NodeID())
	deadline := time.Now().Add(10 * time.Second)
	normalized := strings.ToLower(strings.TrimSpace(targetID))
	for time.Now().Before(deadline) {
		if hasPeerstoreEntry(node.store, normalized) {
			return
		}
		msg, err := p2p.NewPexRequestMessage(32, fmt.Sprintf("%s-%d", tokenBase, time.Now().UnixNano()))
		if err == nil {
			_ = node.server.Broadcast(msg)
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !hasPeerstoreEntry(node.store, normalized) {
		t.Fatalf("peerstore missing PEX entry for %s", targetID)
	}
}

func mustKey(t *testing.T) *crypto.PrivateKey {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func nodeIDFromKey(key *crypto.PrivateKey) string {
	if key == nil {
		return ""
	}
	pub := &key.PrivateKey.PublicKey
	pubBytes := ethcrypto.FromECDSAPub(pub)
	if len(pubBytes) == 0 {
		return ""
	}
	hash := ethcrypto.Keccak256(pubBytes[1:])
	return "0x" + hex.EncodeToString(hash)
}

func hasPeerstoreEntry(store *p2p.Peerstore, target string) bool {
	if store == nil {
		return false
	}
	entries := store.Snapshot()
	for _, entry := range entries {
		if strings.ToLower(strings.TrimSpace(entry.NodeID)) == target {
			return true
		}
	}
	return false
}
