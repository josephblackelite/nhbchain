package p2p

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"nhbchain/crypto"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// A handshake names a node, and only a signature made by that node's key ties
// the packet to it. These tests are about what a failed handshake may cost the
// node that the packet names: it must never cost anything unless the node
// itself signed the evidence, because otherwise anybody can lock a node out of
// its peers with one packet.

const identityTestBan = time.Hour

func identityTestConfig(seed byte) ServerConfig {
	cfg := baseConfig(bytes.Repeat([]byte{seed}, 32))
	cfg.PeerBanDuration = identityTestBan
	return cfg
}

// forgedHandshake builds a handshake that names claimedID and is signed by
// signer, which is not the node with that ID.
func forgedHandshake(t *testing.T, signer *crypto.PrivateKey, claimedID string, chainID uint64, genesis []byte) *handshakePacket {
	t.Helper()
	nonce := make([]byte, handshakeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		t.Fatalf("nonce: %v", err)
	}
	packet := &handshakePacket{handshakeMessage: handshakeMessage{
		ProtocolVersion: protocolVersion,
		ChainID:         chainID,
		GenesisHash:     encodeHex(genesis),
		NodeID:          claimedID,
		Nonce:           encodeHex(nonce),
		ClientVersion:   "forger/1.0",
	}}
	digest, err := handshakeDigest(chainID, genesis, nonce, claimedID)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	sig, err := ethcrypto.Sign(digest, signer.PrivateKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	packet.Signature = encodeHex(sig)
	return packet
}

// dialPacket plays a remote for the server's handleInbound that answers the
// server's hello with the given packet.
func dialPacket(t *testing.T, server *Server, packet *handshakePacket) net.Conn {
	t.Helper()
	left, right := net.Pipe()
	go server.handleInbound(left)
	reader := bufio.NewReader(right)
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatalf("read local handshake: %v", err)
	}
	data, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("marshal handshake: %v", err)
	}
	if _, err := right.Write(append(data, '\n')); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	t.Cleanup(func() { right.Close() })
	return right
}

// waitForHandshakeEnd waits until the server has no handshake in flight.
func waitForHandshakeEnd(t *testing.T, server *Server) {
	t.Helper()
	waitUntil(t, 2*time.Second, "the handshake to end", func() bool {
		server.inboundMu.Lock()
		defer server.inboundMu.Unlock()
		return server.pendingHandshakes == 0
	})
}

// requireNothingHeldAgainst fails when the server holds anything against the
// node: a ban in memory, a ban or a violation in the peerstore, or a record.
func requireNothingHeldAgainst(t *testing.T, server *Server, store *Peerstore, nodeID string) {
	t.Helper()
	now := time.Now()
	if server.isBanned(nodeID) {
		t.Fatalf("node %s was banned by a handshake it did not sign", nodeID)
	}
	if banned, until := server.reputation.BanInfo(nodeID, now); banned {
		t.Fatalf("node %s is banned until %s", nodeID, until)
	}
	if store != nil {
		if store.IsBanned(nodeID, now) {
			t.Fatalf("node %s is banned in the peerstore", nodeID)
		}
		if entry, ok := store.ByNodeID(nodeID); ok {
			t.Fatalf("the peerstore recorded %+v for a node that never signed anything", entry)
		}
	}
}

func TestForgedHandshakeCannotBanTheNodeItNames(t *testing.T) {
	genesis := bytes.Repeat([]byte{0xD1}, 32)
	wrongGenesis := bytes.Repeat([]byte{0xD2}, 32)

	cases := []struct {
		name  string
		build func(t *testing.T, attacker *crypto.PrivateKey, victimID string) *handshakePacket
	}{
		{"signed with another key", func(t *testing.T, attacker *crypto.PrivateKey, victimID string) *handshakePacket {
			return forgedHandshake(t, attacker, victimID, 1, genesis)
		}},
		{"garbage signature", func(t *testing.T, attacker *crypto.PrivateKey, victimID string) *handshakePacket {
			p := forgedHandshake(t, attacker, victimID, 1, genesis)
			p.Signature = encodeHex(bytes.Repeat([]byte{0x11}, 65))
			return p
		}},
		{"truncated signature", func(t *testing.T, attacker *crypto.PrivateKey, victimID string) *handshakePacket {
			p := forgedHandshake(t, attacker, victimID, 1, genesis)
			p.Signature = "0x1234"
			return p
		}},
		{"signature that is not hex", func(t *testing.T, attacker *crypto.PrivateKey, victimID string) *handshakePacket {
			p := forgedHandshake(t, attacker, victimID, 1, genesis)
			p.Signature = "not-a-signature"
			return p
		}},
		{"wrong chain id, signed with another key", func(t *testing.T, attacker *crypto.PrivateKey, victimID string) *handshakePacket {
			return forgedHandshake(t, attacker, victimID, 99, genesis)
		}},
		{"wrong genesis, signed with another key", func(t *testing.T, attacker *crypto.PrivateKey, victimID string) *handshakePacket {
			return forgedHandshake(t, attacker, victimID, 1, wrongGenesis)
		}},
		{"wrong chain id, garbage signature", func(t *testing.T, attacker *crypto.PrivateKey, victimID string) *handshakePacket {
			p := forgedHandshake(t, attacker, victimID, 99, genesis)
			p.Signature = encodeHex(bytes.Repeat([]byte{0x22}, 65))
			return p
		}},
	}

	for _, persistent := range []bool{false, true} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/persistent=%v", tc.name, persistent), func(t *testing.T) {
				local := NewServer(noopHandler{}, mustKey(t), identityTestConfig(0xD1))
				store := newTestPeerstore(t)
				local.SetPeerstore(store)
				victim := NewServer(noopHandler{}, mustKey(t), identityTestConfig(0xD1))
				if persistent {
					local.rememberPersistentPeerID(victim.nodeID)
				}

				packet := tc.build(t, mustKey(t), victim.nodeID)
				if err := local.verifyHandshake(packet); err == nil {
					t.Fatalf("a forged handshake must be rejected")
				}
				requireNothingHeldAgainst(t, local, store, victim.nodeID)

				// The node that really holds the ID can still connect.
				dialHello(t, local, victim)
				waitUntil(t, 2*time.Second, "the genuine node to be registered", func() bool { return local.hasPeer(victim.nodeID) })
			})
		}
	}
}

// The whole path a forged packet takes, as an anonymous remote would send it,
// followed by the node it named connecting for real.
func TestForgedHandshakeOverTheWireCannotLockOutAGenuinePeer(t *testing.T) {
	genesis := bytes.Repeat([]byte{0xD3}, 32)
	for _, wrongChain := range []bool{false, true} {
		t.Run(fmt.Sprintf("wrongChain=%v", wrongChain), func(t *testing.T) {
			local := NewServer(noopHandler{}, mustKey(t), identityTestConfig(0xD3))
			store := newTestPeerstore(t)
			local.SetPeerstore(store)
			victim := NewServer(noopHandler{}, mustKey(t), identityTestConfig(0xD3))
			local.rememberPersistentPeerID(victim.nodeID)

			chain := uint64(1)
			if wrongChain {
				chain = 42
			}
			dialPacket(t, local, forgedHandshake(t, mustKey(t), victim.nodeID, chain, genesis))
			waitForHandshakeEnd(t, local)
			requireNothingHeldAgainst(t, local, store, victim.nodeID)

			dialHello(t, local, victim)
			waitUntil(t, 2*time.Second, "the genuine node to be registered", func() bool { return local.hasPeer(victim.nodeID) })
		})
	}
}

// The same holds when this node is the one that dials: whoever answers at the
// address it dialed can name any node ID.
func TestForgedHandshakeFromADialedAddressCannotBanTheNodeItNames(t *testing.T) {
	genesis := bytes.Repeat([]byte{0xD8}, 32)
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistent=%v", persistent), func(t *testing.T) {
			local := NewServer(noopHandler{}, mustKey(t), identityTestConfig(0xD8))
			store := newTestPeerstore(t)
			local.SetPeerstore(store)
			victim := NewServer(noopHandler{}, mustKey(t), identityTestConfig(0xD8))
			if persistent {
				local.rememberPersistentPeerID(victim.nodeID)
			}

			forged := forgedHandshake(t, mustKey(t), victim.nodeID, 1, genesis)
			local.dialFn = func(ctx context.Context, addr string) (net.Conn, error) {
				left, right := net.Pipe()
				go func() {
					defer right.Close()
					if _, err := bufio.NewReader(right).ReadBytes('\n'); err != nil {
						return
					}
					data, _ := json.Marshal(forged)
					right.Write(append(data, '\n'))
				}()
				return left, nil
			}
			if err := local.Connect("203.0.113.9:6001"); err == nil {
				t.Fatalf("a forged handshake must not complete a connection")
			}
			requireNothingHeldAgainst(t, local, store, victim.nodeID)

			dialHello(t, local, victim)
			waitUntil(t, 2*time.Second, "the genuine node to be registered", func() bool { return local.hasPeer(victim.nodeID) })
		})
	}
}

// Forged handshakes name node IDs of the sender's choosing, so they must not
// leave a trace per ID in any table either.
func TestForgedHandshakesLeaveNoTracePerNodeID(t *testing.T) {
	genesis := bytes.Repeat([]byte{0xD4}, 32)
	local := NewServer(noopHandler{}, mustKey(t), identityTestConfig(0xD4))
	store := newTestPeerstore(t)
	local.SetPeerstore(store)
	attacker := mustKey(t)

	const forged = 300
	for i := 0; i < forged; i++ {
		id := fmt.Sprintf("0x%064x", i+1)
		for _, chain := range []uint64{1, 7} {
			if err := local.verifyHandshake(forgedHandshake(t, attacker, id, chain, genesis)); err == nil {
				t.Fatalf("a forged handshake must be rejected")
			}
		}
	}

	local.reputation.mu.Lock()
	tracked := len(local.reputation.records)
	local.reputation.mu.Unlock()
	if tracked != 0 {
		t.Fatalf("forged handshakes made the reputation table track %d node IDs", tracked)
	}
	if got := len(store.Snapshot()); got != 0 {
		t.Fatalf("forged handshakes made the peerstore hold %d entries", got)
	}
}

// A handshake is not a challenge, so whoever has seen a node's handshake can
// send it again. The second copy is refused, and it says nothing about the node
// that signed it.
func TestReplayedHandshakeCannotBanTheNodeThatSignedIt(t *testing.T) {
	local := NewServer(noopHandler{}, mustKey(t), identityTestConfig(0xD5))
	store := newTestPeerstore(t)
	local.SetPeerstore(store)
	victim := NewServer(noopHandler{}, mustKey(t), identityTestConfig(0xD5))
	local.rememberPersistentPeerID(victim.nodeID)

	// Anybody who connects to the victim is handed its signed handshake.
	captured, err := victim.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if err := local.verifyHandshake(captured); err != nil {
		t.Fatalf("first use: %v", err)
	}
	replay := *captured
	if err := local.verifyHandshake(&replay); err == nil {
		t.Fatalf("a replayed handshake must be rejected")
	}
	if local.isBanned(victim.nodeID) {
		t.Fatalf("a replay of the victim's handshake banned the victim")
	}
	if store.IsBanned(victim.nodeID, time.Now()) {
		t.Fatalf("a replay of the victim's handshake banned the victim in the peerstore")
	}
	if entry, ok := store.ByNodeID(victim.nodeID); ok && entry.Violations != 0 {
		t.Fatalf("a replay of the victim's handshake counted as the victim's violation: %+v", entry)
	}

	dialHello(t, local, victim)
	waitUntil(t, 2*time.Second, "the genuine node to be registered", func() bool { return local.hasPeer(victim.nodeID) })
}

func TestReplayedHandshakeOverTheWireCannotLockOutTheNodeThatSignedIt(t *testing.T) {
	local := NewServer(noopHandler{}, mustKey(t), identityTestConfig(0xD6))
	victim := NewServer(noopHandler{}, mustKey(t), identityTestConfig(0xD6))

	captured, err := victim.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	for i := 0; i < 2; i++ {
		dialPacket(t, local, captured)
		waitForHandshakeEnd(t, local)
	}
	// The first copy was taken for the victim and holds its slot.
	if local.isBanned(victim.nodeID) {
		t.Fatalf("replaying the victim's handshake banned the victim")
	}
}

// A ban is for a node that signed the wrong chain or genesis itself, and never
// for a configured persistent peer, whose recovery must not wait for a ban to
// run out.
func TestSignedHandshakeViolationBansOnlyNodesThatAreNotConfiguredPersistent(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistent=%v", persistent), func(t *testing.T) {
			localCfg := identityTestConfig(0xD7)
			remoteCfg := identityTestConfig(0xD7)
			remoteCfg.ChainID = 2
			local := NewServer(noopHandler{}, mustKey(t), localCfg)
			store := newTestPeerstore(t)
			local.SetPeerstore(store)
			remote := NewServer(noopHandler{}, mustKey(t), remoteCfg)
			if persistent {
				local.rememberPersistentPeerID(remote.nodeID)
			}

			packet, err := remote.buildHandshake()
			if err != nil {
				t.Fatalf("build handshake: %v", err)
			}
			if err := local.verifyHandshake(packet); err == nil {
				t.Fatalf("a handshake for another chain must be rejected")
			}

			banned := local.isBanned(remote.nodeID)
			storeBanned := store.IsBanned(remote.nodeID, time.Now())
			if persistent && (banned || storeBanned) {
				t.Fatalf("a configured persistent peer must not be banned on handshake evidence (memory=%v peerstore=%v)", banned, storeBanned)
			}
			if !persistent && (!banned || !storeBanned) {
				t.Fatalf("a node that signed a handshake for another chain is banned (memory=%v peerstore=%v)", banned, storeBanned)
			}
		})
	}
}
