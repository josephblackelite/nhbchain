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

// reserveConfig is a server configuration with limits of 16 peers in all, 12 of
// them inbound and 8 outbound, and the given configured peers.
func reserveConfig(seed byte, configured ...string) ServerConfig {
	cfg := baseConfig(bytes.Repeat([]byte{seed}, 32))
	cfg.MaxPeers = 16
	cfg.MaxInbound = 12
	cfg.MaxOutbound = 8
	cfg.PersistentPeers = configured
	return cfg
}

// registerAt registers a peer that connects from, or was dialed at, the given
// address.
func registerAt(t *testing.T, server *Server, id string, remote net.Addr, inbound, persistent bool) (*Peer, error) {
	t.Helper()
	left, right := net.Pipe()
	t.Cleanup(func() {
		left.Close()
		right.Close()
	})
	conn := addrConn{Conn: left, remote: remote}
	peer := newPeer(id, "test/1.0", conn, bufio.NewReader(conn), server, inbound, persistent, "")
	return peer, server.registerPeer(peer)
}

// fillWithStrangers registers peers from distinct public addresses until the
// server turns one away. It returns how many it took and why it stopped.
func fillWithStrangers(t *testing.T, server *Server, inbound bool) (int, error) {
	t.Helper()
	for i := 0; i < 200; i++ {
		id := fmt.Sprintf("stranger-%v-%d", inbound, i)
		if _, err := registerAt(t, server, id, tcpAddr(fmt.Sprintf("203.0.113.%d", i+1), 30000+i), inbound, false); err != nil {
			return i, err
		}
	}
	t.Fatalf("the server never turned a stranger away")
	return 0, nil
}

func requireWithinLimits(t *testing.T, server *Server, cfg ServerConfig) {
	t.Helper()
	server.mu.RLock()
	defer server.mu.RUnlock()
	if len(server.peers) > cfg.MaxPeers || server.inboundCount > cfg.MaxInbound || server.outboundCount > cfg.MaxOutbound {
		t.Fatalf("limits of %d/%d/%d exceeded: %d peers, %d inbound, %d outbound",
			cfg.MaxPeers, cfg.MaxInbound, cfg.MaxOutbound, len(server.peers), server.inboundCount, server.outboundCount)
	}
}

// handshakeFrom plays a remote node that connects to the server from the given
// address and sends its handshake. The connection is left open.
func handshakeFrom(t *testing.T, server, remote *Server, from net.Addr) net.Conn {
	t.Helper()
	left, right := net.Pipe()
	go server.handleInbound(addrConn{Conn: left, remote: from})
	t.Cleanup(func() { right.Close() })
	reader := bufio.NewReader(right)
	right.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := reader.ReadBytes('\n'); err != nil {
		t.Fatalf("read local handshake: %v", err)
	}
	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	data, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("marshal handshake: %v", err)
	}
	if _, err := right.Write(append(data, '\n')); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	return right
}

// waitForCloseOrPeer waits until the server has either dropped the connection
// or registered the node.
func waitForCloseOrPeer(t *testing.T, server, remote *Server, conn net.Conn) {
	t.Helper()
	reader := bufio.NewReader(conn)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if server.hasPeer(remote.nodeID) {
			return
		}
		conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		if _, err := reader.ReadBytes('\n'); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
	}
}

func TestStrangersCannotTakeTheSlotsKeptForConfiguredPeers(t *testing.T) {
	cfg := reserveConfig(0xE1, "198.51.100.7:6001", "198.51.100.8:6001")
	server := NewServer(noopHandler{}, mustKey(t), cfg)

	// Of the 12 inbound slots two are kept back for the configured peers.
	taken, err := fillWithStrangers(t, server, true)
	if taken != 10 {
		t.Fatalf("expected strangers to be turned away after 10 inbound peers, got %d (%v)", taken, err)
	}

	// The configured peers still get in: one recognised by its node ID and one
	// that connects from the address of a configured peer, which is how a peer is
	// known before this node has connected to it.
	if _, err := registerAt(t, server, "known-by-id", tcpAddr("192.0.2.50", 31000), true, true); err != nil {
		t.Fatalf("a configured peer must get a slot when strangers hold the rest: %v", err)
	}
	if _, err := registerAt(t, server, "known-by-address", tcpAddr("198.51.100.8", 31001), true, false); err != nil {
		t.Fatalf("a peer connecting from a configured address must get a slot when strangers hold the rest: %v", err)
	}
	// Only as many as were set aside: the reserve is not an open door.
	if _, err := registerAt(t, server, "one-too-many", tcpAddr("198.51.100.7", 31002), true, true); err == nil {
		t.Fatalf("expected the reserve to be limited to the 2 slots set aside")
	}
	requireWithinLimits(t, server, cfg)
}

func TestAConfiguredPeerCanBeDialedWhenStrangersHoldEverythingElse(t *testing.T) {
	cfg := reserveConfig(0xE2, "198.51.100.7:6001", "198.51.100.8:6001")
	server := NewServer(noopHandler{}, mustKey(t), cfg)

	inbound, _ := fillWithStrangers(t, server, true)
	outbound, err := fillWithStrangers(t, server, false)
	if inbound+outbound != 14 || err == nil || !strings.Contains(err.Error(), "maximum peers") {
		t.Fatalf("expected strangers to hold 14 of the 16 slots, got %d inbound and %d outbound (%v)", inbound, outbound, err)
	}

	for _, id := range []string{"validator-a", "validator-b"} {
		if _, err := registerAt(t, server, id, tcpAddr("198.51.100.7", 6001), false, true); err != nil {
			t.Fatalf("a configured peer must get a slot when strangers hold the rest: %v", err)
		}
	}
	if _, err := registerAt(t, server, "validator-c", tcpAddr("198.51.100.9", 6001), false, true); err == nil {
		t.Fatalf("expected the reserve to be limited to the 2 slots set aside")
	}
	requireWithinLimits(t, server, cfg)
}

func TestOutboundStrangersStopShortOfTheLimitByTheReserve(t *testing.T) {
	cfg := reserveConfig(0xE3, "198.51.100.7:6001", "198.51.100.8:6001")
	server := NewServer(noopHandler{}, mustKey(t), cfg)

	taken, err := fillWithStrangers(t, server, false)
	if taken != 6 || err == nil || !strings.Contains(err.Error(), "outbound") {
		t.Fatalf("expected strangers to stop at 6 of the 8 outbound slots, got %d (%v)", taken, err)
	}
	for _, id := range []string{"validator-a", "validator-b"} {
		if _, err := registerAt(t, server, id, tcpAddr("198.51.100.7", 6001), false, true); err != nil {
			t.Fatalf("a configured peer must get one of the outbound slots set aside: %v", err)
		}
	}
	requireWithinLimits(t, server, cfg)
}

func TestNoSlotsAreKeptBackWithoutConfiguredPeers(t *testing.T) {
	cfg := reserveConfig(0xE4)
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	if taken, _ := fillWithStrangers(t, server, true); taken != 12 {
		t.Fatalf("expected all 12 inbound slots to be open to strangers, got %d", taken)
	}

	server = NewServer(noopHandler{}, mustKey(t), cfg)
	if taken, _ := fillWithStrangers(t, server, false); taken != 8 {
		t.Fatalf("expected all 8 outbound slots to be open to strangers, got %d", taken)
	}
}

func TestTheReserveIsAtMostAQuarterOfTheSmallestLimit(t *testing.T) {
	// Ten configured peers, limits of 8: two slots are kept back, not ten.
	cfg := baseConfig(bytes.Repeat([]byte{0xE5}, 32))
	for i := 0; i < 10; i++ {
		cfg.PersistentPeers = append(cfg.PersistentPeers, fmt.Sprintf("198.51.100.%d:6001", 10+i))
	}
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	if taken, _ := fillWithStrangers(t, server, true); taken != 6 {
		t.Fatalf("expected strangers to stop at 6 of 8 inbound slots, got %d", taken)
	}

	// A small inbound limit makes the reserve smaller with it: a quarter of 4 is 1.
	cfg = reserveConfig(0xE5, "198.51.100.7:6001", "198.51.100.8:6001", "198.51.100.9:6001")
	cfg.MaxInbound = 4
	server = NewServer(noopHandler{}, mustKey(t), cfg)
	if taken, _ := fillWithStrangers(t, server, true); taken != 3 {
		t.Fatalf("expected strangers to stop at 3 of 4 inbound slots, got %d", taken)
	}

	// With limits too small to spare a slot nothing is kept back.
	cfg = reserveConfig(0xE5, "198.51.100.7:6001")
	cfg.MaxInbound = 3
	server = NewServer(noopHandler{}, mustKey(t), cfg)
	if taken, _ := fillWithStrangers(t, server, true); taken != 3 {
		t.Fatalf("expected all 3 inbound slots to be open to strangers, got %d", taken)
	}

	// An address in both lists is one configured peer.
	cfg = reserveConfig(0xE5, "198.51.100.7:6001")
	cfg.Bootnodes = []string{"198.51.100.7:6001"}
	server = NewServer(noopHandler{}, mustKey(t), cfg)
	if taken, _ := fillWithStrangers(t, server, true); taken != 11 {
		t.Fatalf("expected one inbound slot to be kept back for one configured peer, got %d strangers", taken)
	}
}

func TestAReservedSlotCanBeTakenAgainOnceItsPeerHasLeft(t *testing.T) {
	cfg := reserveConfig(0xE6, "198.51.100.7:6001")
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	fillWithStrangers(t, server, true)

	first, err := registerAt(t, server, "validator-a", tcpAddr("198.51.100.7", 31000), true, true)
	if err != nil {
		t.Fatalf("a configured peer must get the slot kept back for it: %v", err)
	}
	if _, err := registerAt(t, server, "validator-b", tcpAddr("198.51.100.7", 31001), true, true); err == nil {
		t.Fatalf("expected the single reserved slot to be taken")
	}
	server.removePeer(first, false, nil)
	if _, err := registerAt(t, server, "validator-b", tcpAddr("198.51.100.7", 31002), true, true); err != nil {
		t.Fatalf("expected the reserved slot to be free again: %v", err)
	}
	requireWithinLimits(t, server, cfg)
}

// A peer reports the addresses it listens on in its handshake. Nothing binds
// them to the connection, so they must not be taken as the address of a
// configured peer.
func TestReservedSlotsAreNotGrantedForAReportedListenAddress(t *testing.T) {
	cfg := reserveConfig(0xE7, "198.51.100.7:6001")
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	fillWithStrangers(t, server, true)

	claimant := NewServer(noopHandler{}, mustKey(t), cfg)
	claimant.addListenAddress("198.51.100.7:6001")
	conn := handshakeFrom(t, server, claimant, tcpAddr("203.0.113.200", 32000))
	waitForCloseOrPeer(t, server, claimant, conn)
	if server.hasPeer(claimant.nodeID) {
		t.Fatalf("a peer must not get a reserved slot by naming a configured address in its handshake")
	}

	// The same node is taken in as soon as an ordinary slot is free, so it was
	// the slots that turned it away.
	server.mu.RLock()
	var stranger *Peer
	for _, peer := range server.peers {
		stranger = peer
		break
	}
	server.mu.RUnlock()
	server.removePeer(stranger, false, nil)
	conn = handshakeFrom(t, server, claimant, tcpAddr("203.0.113.200", 32001))
	waitUntil(t, 2*time.Second, "the node to be admitted to an ordinary slot", func() bool { return server.hasPeer(claimant.nodeID) })
	conn.Close()
}

// After a restart the node IDs of the configured peers are not known until this
// node has connected to them, so an inbound connection is recognised by the
// address it really comes from.
func TestAConfiguredPeerIsRecognisedByItsAddressOnTheFirstInboundConnection(t *testing.T) {
	cfg := reserveConfig(0xE8, "198.51.100.7:6001")
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	fillWithStrangers(t, server, true)

	validator := NewServer(noopHandler{}, mustKey(t), cfg)
	validator.addListenAddress("198.51.100.7:6001")
	handshakeFrom(t, server, validator, tcpAddr("198.51.100.7", 33000))
	waitUntil(t, 2*time.Second, "the configured peer to be registered", func() bool { return server.hasPeer(validator.nodeID) })
	requireWithinLimits(t, server, cfg)
}

func TestDialingAConfiguredPeerSucceedsWhenStrangersHoldEverythingElse(t *testing.T) {
	remote, _, remoteAddr := startLoopbackServer(t)
	stranger, _, strangerAddr := startLoopbackServer(t)

	cfg := baseConfig(bytes.Repeat([]byte{0xD1}, 32))
	cfg.MaxPeers = 16
	cfg.MaxInbound = 12
	cfg.MaxOutbound = 8
	cfg.ReadTimeout = 30 * time.Second
	cfg.PersistentPeers = []string{remoteAddr}
	local := NewServer(noopHandler{}, mustKey(t), cfg)
	t.Cleanup(func() { local.Stop() })

	fillWithStrangers(t, local, true)
	fillWithStrangers(t, local, false)

	if err := local.Connect(strangerAddr); err == nil || !strings.Contains(err.Error(), "maximum peers") {
		t.Fatalf("expected a peer that is not configured to be turned away, got %v", err)
	}
	if local.hasPeer(stranger.nodeID) {
		t.Fatalf("a peer that is not configured must not get the reserved slot")
	}
	if err := local.Connect(remoteAddr); err != nil {
		t.Fatalf("a configured peer must be reachable when strangers hold the rest: %v", err)
	}
	if !local.hasPeer(remote.nodeID) {
		t.Fatalf("the configured peer is not connected")
	}
	local.mu.RLock()
	peer := local.peers[remote.nodeID]
	local.mu.RUnlock()
	if peer == nil || !peer.persistent {
		t.Fatalf("expected the configured peer to be connected and persistent")
	}
	requireWithinLimits(t, local, cfg)
}

func TestInboundPeersAreLimitedPerNetworkNotPerExactAddress(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xE9}, 32))
	cfg.MaxPeers = 64
	cfg.MaxInbound = 64
	server := NewServer(noopHandler{}, mustKey(t), cfg)

	for i := 0; i < defaultMaxInboundPerIP; i++ {
		if _, err := registerAt(t, server, fmt.Sprintf("v6-%d", i), tcpAddr(fmt.Sprintf("2001:db8:1:2::%x", i+1), 34000+i), true, false); err != nil {
			t.Fatalf("peer %d: %v", i, err)
		}
	}
	// Another address in the same /64 is the same host as far as a limit goes.
	if _, err := registerAt(t, server, "v6-too-many", tcpAddr("2001:db8:1:2:aaaa:bbbb:cccc:1", 34100), true, false); err == nil {
		t.Fatalf("expected the %dth inbound peer from one IPv6 /64 to be refused", defaultMaxInboundPerIP+1)
	}
	if _, err := registerAt(t, server, "v6-elsewhere", tcpAddr("2001:db8:1:3::1", 34101), true, false); err != nil {
		t.Fatalf("a peer from another /64 must be accepted: %v", err)
	}
}

func TestInboundConnectionAttemptsAreLimitedPerNetwork(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xEA}, 32)))

	admitted := 0
	for i := 0; i < 4*handshakeAttemptBurst; i++ {
		if admitProbe(t, server, tcpAddr(fmt.Sprintf("2001:db8:1:2::%x", i+1), 35000+i), nil) {
			admitted++
		}
	}
	if admitted == 0 || admitted > handshakeAttemptBurst+1 {
		t.Fatalf("expected between 1 and %d of %d attempts from one IPv6 /64 to start a handshake, got %d",
			handshakeAttemptBurst+1, 4*handshakeAttemptBurst, admitted)
	}
	if !admitProbe(t, server, tcpAddr("2001:db8:1:3::1", 35999), nil) {
		t.Fatalf("a different /64 must not be limited by the first one")
	}
}

func TestInboundHandshakesInFlightAreLimitedPerNetwork(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xEB}, 32)))
	var open []net.Conn
	t.Cleanup(func() {
		for _, conn := range open {
			conn.Close()
		}
	})

	admitted := 0
	for i := 0; i < 2*defaultMaxPendingPerIP+2; i++ {
		if admitProbe(t, server, tcpAddr(fmt.Sprintf("2001:db8:1:2::%x", i+1), 36000+i), &open) {
			admitted++
		}
	}
	if admitted != defaultMaxPendingPerIP {
		t.Fatalf("expected %d handshakes in flight from one IPv6 /64, got %d", defaultMaxPendingPerIP, admitted)
	}
}

func TestChainDataRequestsShareABudgetWithinANetwork(t *testing.T) {
	handler := &requestRecorder{}
	server := NewServer(handler, mustKey(t), requestFloodConfig(0xEC))
	burst := int(getBlocksBurstPerIP)
	for i := 0; i < 3; i++ {
		floodPeer(t, server, fmt.Sprintf("asker-%d", i), tcpAddr(fmt.Sprintf("2001:db8:1:2::%x", i+1), 37000+i), false, MsgTypeGetBlocks, 10*burst)
	}

	waitUntil(t, 3*time.Second, "the budget to be spent", func() bool { return handler.count(MsgTypeGetBlocks) >= burst })
	time.Sleep(300 * time.Millisecond)
	if got := handler.count(MsgTypeGetBlocks); got > burst+4 {
		t.Fatalf("expected the budget of one IPv6 /64 to bound the requests handled to about %d, got %d", burst, got)
	}
}

func TestMessageRateBudgetIsSharedWithinANetwork(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xED}, 32)))
	now := time.Now()
	burst := int(server.cfg.RateBurst)

	allowed := 0
	for i := 0; i < 5*burst; i++ {
		if server.allowIP(fmt.Sprintf("[2001:db8:1:2::%x]:38000", i+1), now) {
			allowed++
		}
	}
	if allowed == 0 || allowed > burst+1 {
		t.Fatalf("expected between 1 and %d messages from one IPv6 /64 to pass at once, got %d", burst+1, allowed)
	}
	if !server.allowIP("[2001:db8:1:3::1]:38000", now) {
		t.Fatalf("a different /64 must have a budget of its own")
	}

	// An IPv4-mapped IPv6 address is the IPv4 address it stands for.
	allowed = 0
	for i := 0; i < 5*burst; i++ {
		remote := "203.0.113.9:38001"
		if i%2 == 1 {
			remote = "[::ffff:203.0.113.9]:38001"
		}
		if server.allowIP(remote, now) {
			allowed++
		}
	}
	if allowed == 0 || allowed > burst+1 {
		t.Fatalf("expected one budget for an IPv4 address and its mapped form, got %d messages through of at most %d", allowed, burst+1)
	}
	// IPv4 addresses next to each other stay separate.
	if !server.allowIP("203.0.113.10:38002", now) {
		t.Fatalf("a neighbouring IPv4 address must have a budget of its own")
	}
}
