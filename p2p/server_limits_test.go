package p2p

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// addrConn reports a chosen remote address, so that tests can play a peer that
// connects from a public address without leaving the machine.
type addrConn struct {
	net.Conn
	remote net.Addr
}

func (c addrConn) RemoteAddr() net.Addr { return c.remote }

func tcpAddr(host string, port int) net.Addr {
	return &net.TCPAddr{IP: net.ParseIP(host), Port: port}
}

func waitUntil(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("timed out waiting for %s", what)
	}
}

// registerTestPeer registers a peer whose write loop is not running, so that
// its outbound queue only fills up.
func registerTestPeer(t *testing.T, server *Server, id string, persistent bool) *Peer {
	t.Helper()
	left, right := net.Pipe()
	t.Cleanup(func() {
		left.Close()
		right.Close()
	})
	peer := newPeer(id, "test/1.0", left, bufio.NewReader(left), server, false, persistent, "")
	if err := server.registerPeer(peer); err != nil {
		t.Fatalf("register %s: %v", id, err)
	}
	return peer
}

func TestBroadcastDropsMessagesForASlowPeerButKeepsItsConnection(t *testing.T) {
	for _, persistent := range []bool{false, true} {
		t.Run(fmt.Sprintf("persistent=%v", persistent), func(t *testing.T) {
			server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xB1}, 32)))
			slow := registerTestPeer(t, server, "slow", persistent)
			fast := registerTestPeer(t, server, "fast", false)

			var received int
			done := make(chan struct{})
			go func() {
				defer close(done)
				for range fast.outbound {
					received++
					if received == 3*outboundQueueSize {
						return
					}
				}
			}()

			msg := &Message{Type: MsgTypeTx, Payload: []byte("{}")}
			for i := 0; i < 3*outboundQueueSize; i++ {
				if err := server.Broadcast(msg); err != nil {
					t.Fatalf("broadcast %d: a message dropped for a slow peer is not an error: %v", i, err)
				}
				for len(fast.outbound) > outboundQueueSize/2 {
					time.Sleep(time.Millisecond)
				}
			}
			<-done

			select {
			case <-slow.closed:
				t.Fatalf("a full send queue must not end the peer's connection")
			default:
			}
			if !server.hasPeer("slow") {
				t.Fatalf("the slow peer was removed")
			}
			if got := len(slow.outbound); got != outboundQueueSize {
				t.Fatalf("expected the slow peer's queue to be full (%d), got %d", outboundQueueSize, got)
			}
			if received != 3*outboundQueueSize {
				t.Fatalf("the healthy peer missed messages: got %d of %d", received, 3*outboundQueueSize)
			}
		})
	}
}

func TestOutboundQueueIsCappedInBytesAsWellAsInMessages(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xB2}, 32))
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	peer := registerTestPeer(t, server, "reader", false)

	big := &Message{Type: MsgTypeBlocks, Payload: make([]byte, cfg.MaxMessageBytes/2)}
	accepted := 0
	var lastErr error
	for i := 0; i < outboundQueueSize; i++ {
		if lastErr = peer.Enqueue(big); lastErr != nil {
			break
		}
		accepted++
	}
	if !errors.Is(lastErr, errQueueFull) {
		t.Fatalf("expected the byte cap to stop the queue, got %v after %d messages", lastErr, accepted)
	}
	if queued := int64(accepted) * int64(len(big.Payload)); queued > 4*int64(cfg.MaxMessageBytes) {
		t.Fatalf("%d bytes queued for one peer, expected at most %d", queued, 4*cfg.MaxMessageBytes)
	}
	// Room is made again as the queue is written out.
	<-peer.outbound
	peer.queuedBytes.Add(-int64(len(big.Payload)))
	if err := peer.Enqueue(big); err != nil {
		t.Fatalf("expected a message to fit again after one was taken out: %v", err)
	}
}

func TestOutboundQueueBytesAreReleasedAsMessagesAreWritten(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xB3}, 32))
	cfg.WriteTimeout = 2 * time.Second
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	left, right := net.Pipe()
	defer right.Close()
	peer := newPeer("writer", "test/1.0", left, bufio.NewReader(left), server, false, false, "")
	go peer.writeLoop()
	defer peer.cancel()

	go func() {
		reader := bufio.NewReader(right)
		for {
			if _, err := reader.ReadBytes('\n'); err != nil {
				return
			}
		}
	}()

	msg := &Message{Type: MsgTypeBlocks, Payload: make([]byte, cfg.MaxMessageBytes/2)}
	for i := 0; i < 40; i++ {
		waitUntil(t, 5*time.Second, "room in the queue", func() bool { return peer.Enqueue(msg) == nil })
	}
	waitUntil(t, 5*time.Second, "the queue to drain", func() bool { return peer.queuedBytes.Load() == 0 })
}

func TestPongIsDroppedWhenThePeerQueueIsFull(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xB4}, 32)))
	peer := registerTestPeer(t, server, "busy", true)
	for i := 0; i < outboundQueueSize; i++ {
		if err := peer.Enqueue(&Message{Type: MsgTypeTx, Payload: []byte("x")}); err != nil {
			t.Fatalf("fill queue: %v", err)
		}
	}
	ping, err := NewPingMessage(7, time.Now())
	if err != nil {
		t.Fatalf("build ping: %v", err)
	}
	handled, err := peer.handleControlMessage(ping)
	if err != nil || !handled {
		t.Fatalf("a ping the peer's full queue has no room to answer must not fail the connection: handled=%v err=%v", handled, err)
	}
}

func TestKeepaliveKeepsPingingWhileTheQueueIsFull(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xB5}, 32))
	cfg.PingInterval = 20 * time.Millisecond
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	peer := registerTestPeer(t, server, "busy", true)
	for i := 0; i < outboundQueueSize; i++ {
		if err := peer.Enqueue(&Message{Type: MsgTypeTx, Payload: []byte("x")}); err != nil {
			t.Fatalf("fill queue: %v", err)
		}
	}
	go peer.keepaliveLoop()
	defer peer.cancel()

	// Several ticks pass with nowhere to put the ping.
	time.Sleep(150 * time.Millisecond)
	for len(peer.outbound) > 0 {
		<-peer.outbound
	}
	waitUntil(t, 2*time.Second, "a ping after room was made", func() bool {
		select {
		case msg := <-peer.outbound:
			return msg.Type == MsgTypePing
		default:
			return false
		}
	})
}

// admitProbe plays an inbound connection from the given address up to the point
// where the server has either refused it or started the handshake, which it
// starts by sending its own hello. It returns whether the connection was
// admitted and closes the remote end.
func admitProbe(t *testing.T, server *Server, remote net.Addr, keepOpen *[]net.Conn) bool {
	t.Helper()
	left, right := net.Pipe()
	go server.handleInbound(addrConn{Conn: left, remote: remote})
	right.SetReadDeadline(time.Now().Add(time.Second))
	_, err := bufio.NewReader(right).ReadBytes('\n')
	admitted := err == nil
	if admitted && keepOpen != nil {
		*keepOpen = append(*keepOpen, right)
		return true
	}
	right.Close()
	if admitted {
		// The handshake ends with the connection; wait until its slot is free.
		waitUntil(t, 2*time.Second, "the handshake slot to be released", func() bool {
			server.inboundMu.Lock()
			defer server.inboundMu.Unlock()
			return server.pendingHandshakes == 0
		})
	}
	return admitted
}

func TestInboundConnectionAttemptsFromOneAddressAreLimited(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xB6}, 32)))
	remote := tcpAddr("203.0.113.9", 40001)

	admitted := 0
	for i := 0; i < 4*handshakeAttemptBurst; i++ {
		if admitProbe(t, server, remote, nil) {
			admitted++
		}
	}
	if admitted == 0 || admitted > handshakeAttemptBurst+1 {
		t.Fatalf("expected between 1 and %d of %d attempts from one address to start a handshake, got %d",
			handshakeAttemptBurst+1, 4*handshakeAttemptBurst, admitted)
	}
	// Another address is not affected by the first one's attempts.
	if !admitProbe(t, server, tcpAddr("203.0.113.10", 40001), nil) {
		t.Fatalf("a different address must not be limited by the first one")
	}
}

func TestInboundHandshakesInFlightFromOneAddressAreLimited(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xB7}, 32)))
	var open []net.Conn
	t.Cleanup(func() {
		for _, conn := range open {
			conn.Close()
		}
	})

	admitted := 0
	for i := 0; i < 2*defaultMaxPendingPerIP+2; i++ {
		if admitProbe(t, server, tcpAddr("203.0.113.9", 41000+i), &open) {
			admitted++
		}
	}
	if admitted != defaultMaxPendingPerIP {
		t.Fatalf("expected %d handshakes in flight from one address, got %d", defaultMaxPendingPerIP, admitted)
	}
}

func TestInboundHandshakesInFlightAreLimitedInTotal(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xB8}, 32)))
	server.maxPendingHandshakes = 6
	var open []net.Conn
	t.Cleanup(func() {
		for _, conn := range open {
			conn.Close()
		}
	})

	admitted := 0
	for i := 0; i < 20; i++ {
		host := fmt.Sprintf("203.0.113.%d", 20+i)
		if admitProbe(t, server, tcpAddr(host, 42000+i), &open) {
			admitted++
		}
	}
	if admitted != 6 {
		t.Fatalf("expected 6 handshakes in flight in total, got %d", admitted)
	}
}

func TestConfiguredPeersAreNotCrowdedOutOfTheHandshakeSlots(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xB8}, 32))
	cfg.PersistentPeers = []string{"198.51.100.7:6001"}
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	server.maxPendingHandshakes = 2
	var open []net.Conn
	t.Cleanup(func() {
		for _, conn := range open {
			conn.Close()
		}
	})

	for i := 0; i < 2; i++ {
		if !admitProbe(t, server, tcpAddr(fmt.Sprintf("203.0.113.%d", 60+i), 47000), &open) {
			t.Fatalf("handshake %d should have been admitted", i)
		}
	}
	if admitProbe(t, server, tcpAddr("203.0.113.62", 47000), &open) {
		t.Fatalf("expected the shared handshake slots to be used up for an unknown address")
	}
	if !admitProbe(t, server, tcpAddr("198.51.100.7", 47000), &open) {
		t.Fatalf("a configured peer must get a handshake slot even when strangers have used them all")
	}
}

func TestInboundAddressLimitsSpareLoopbackAndConfiguredPeers(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xB9}, 32))
	cfg.PersistentPeers = []string{"198.51.100.7:6001"}
	cfg.Bootnodes = []string{"198.51.100.8:6001"}
	server := NewServer(noopHandler{}, mustKey(t), cfg)

	for _, host := range []string{"127.0.0.1", "198.51.100.7", "198.51.100.8"} {
		for i := 0; i < 4*handshakeAttemptBurst; i++ {
			if !admitProbe(t, server, tcpAddr(host, 43000+i), nil) {
				t.Fatalf("%s must not be limited per address (attempt %d)", host, i)
			}
		}
	}
	if !server.exemptFromAddressLimits("pipe") {
		t.Fatalf("an address that is not an IP has no per-address limit")
	}
	if server.exemptFromAddressLimits("203.0.113.9") {
		t.Fatalf("an unknown public address must be limited")
	}
}

func TestEstablishedInboundPeersFromOneAddressAreLimited(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xBA}, 32))
	cfg.MaxPeers = 64
	cfg.MaxInbound = 64
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	register := func(id string, host string, port int) error {
		left, right := net.Pipe()
		t.Cleanup(func() {
			left.Close()
			right.Close()
		})
		conn := addrConn{Conn: left, remote: tcpAddr(host, port)}
		return server.registerPeer(newPeer(id, "test/1.0", conn, bufio.NewReader(conn), server, true, false, ""))
	}

	for i := 0; i < defaultMaxInboundPerIP; i++ {
		if err := register(fmt.Sprintf("peer-%d", i), "203.0.113.9", 50000+i); err != nil {
			t.Fatalf("peer %d: %v", i, err)
		}
	}
	if err := register("one-too-many", "203.0.113.9", 51000); err == nil {
		t.Fatalf("expected the %dth inbound peer from one address to be refused", defaultMaxInboundPerIP+1)
	}
	if err := register("elsewhere", "203.0.113.77", 51001); err != nil {
		t.Fatalf("a peer from another address must be accepted: %v", err)
	}
	for i := 0; i < 2*defaultMaxInboundPerIP; i++ {
		if err := register(fmt.Sprintf("local-%d", i), "127.0.0.1", 52000+i); err != nil {
			t.Fatalf("loopback peers are not limited per address: %v", err)
		}
	}
}

// dialHello plays a remote peer for the server's handleInbound: it reads the
// server's hello and answers with the remote's own.
func dialHello(t *testing.T, server, remote *Server) net.Conn {
	t.Helper()
	left, right := net.Pipe()
	go server.handleInbound(left)
	reader := bufio.NewReader(right)
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
	t.Cleanup(func() { right.Close() })
	return right
}

func TestPeerIsRecordedAndPersistedOnlyOnceItIsRegistered(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xBB}, 32))
	cfg.MaxPeers = 1
	cfg.MaxInbound = 1
	cfg.EnablePEX = true
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	store := newTestPeerstore(t)
	server.SetPeerstore(store)

	admitted := NewServer(noopHandler{}, mustKey(t), cfg)
	admitted.addListenAddress("127.0.0.1:34001")
	turnedAway := NewServer(noopHandler{}, mustKey(t), cfg)
	turnedAway.addListenAddress("127.0.0.1:34002")

	dialHello(t, server, admitted)
	waitUntil(t, 2*time.Second, "the first peer to register", func() bool { return server.hasPeer(admitted.nodeID) })

	dialHello(t, server, turnedAway)
	// The handshake of the second peer completes but there is no slot for it.
	waitUntil(t, 2*time.Second, "the second handshake to be turned down", func() bool {
		server.inboundMu.Lock()
		defer server.inboundMu.Unlock()
		return server.pendingHandshakes == 0
	})
	time.Sleep(50 * time.Millisecond)

	if server.hasPeer(turnedAway.nodeID) {
		t.Fatalf("the second peer must not have been registered")
	}
	if _, ok := store.ByNodeID(turnedAway.nodeID); ok {
		t.Fatalf("a peer that never got a slot must not be written to the peerstore")
	}
	server.pex.mu.Lock()
	_, inBook := server.pex.book[turnedAway.nodeID]
	server.pex.mu.Unlock()
	if inBook {
		t.Fatalf("a peer that never got a slot must not enter the address book")
	}
	server.mu.RLock()
	_, recorded := server.records[turnedAway.nodeID]
	server.mu.RUnlock()
	if recorded {
		t.Fatalf("a peer that never got a slot must not get a peer record")
	}

	entry, ok := store.ByNodeID(admitted.nodeID)
	if !ok || entry.Addr != "127.0.0.1:34001" || entry.Score < 1 {
		t.Fatalf("expected the registered peer to be persisted with a success, got %+v ok=%v", entry, ok)
	}
}

func TestAddressesInAHandshakeAreLimited(t *testing.T) {
	addrs := make([]string, 5000)
	for i := range addrs {
		addrs[i] = fmt.Sprintf("10.30.%d.%d:26656", (i>>8)&0xff, i&0xff)
	}
	cleaned := sanitizeListenAddrs(addrs)
	if len(cleaned) == 0 || len(cleaned) > maxHandshakeListenAddrs {
		t.Fatalf("expected between 1 and %d addresses, got %d", maxHandshakeListenAddrs, len(cleaned))
	}
}

func TestReputationTableIsBounded(t *testing.T) {
	m := NewReputationManager(ReputationConfig{BanScore: 100, GreyScore: 50, MaxRecords: 50})
	now := time.Now()
	m.SetBan("banned-early", now.Add(time.Hour), now)
	for i := 0; i < 500; i++ {
		m.MarkUseful(fmt.Sprintf("peer-%d", i), now.Add(time.Duration(i)*time.Second))
	}
	m.mu.Lock()
	tracked := len(m.records)
	_, kept := m.records["banned-early"]
	m.mu.Unlock()
	if tracked > 50 {
		t.Fatalf("reputation table holds %d peers, cap is 50", tracked)
	}
	if !kept {
		t.Fatalf("a peer under an active ban must be the last to be forgotten")
	}
	if !m.IsBanned("banned-early", now.Add(time.Minute)) {
		t.Fatalf("the ban was lost")
	}

	defaultCap := NewReputationManager(ReputationConfig{})
	for i := 0; i < defaultReputationMaxRecords+500; i++ {
		defaultCap.MarkUseful(fmt.Sprintf("peer-%d", i), now)
	}
	defaultCap.mu.Lock()
	tracked = len(defaultCap.records)
	defaultCap.mu.Unlock()
	if tracked > defaultReputationMaxRecords {
		t.Fatalf("reputation table holds %d peers, cap is %d", tracked, defaultReputationMaxRecords)
	}
}

func TestPeerRecordsAreBounded(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xBC}, 32)))
	for i := 0; i < maxPeerRecords+500; i++ {
		server.recordPeerHandshake(&handshakePacket{nodeID: fmt.Sprintf("0x%064x", i+1)})
	}
	server.mu.RLock()
	got := len(server.records)
	server.mu.RUnlock()
	if got > maxPeerRecords {
		t.Fatalf("peer records hold %d peers, cap is %d", got, maxPeerRecords)
	}
}

func TestMessageStatisticsOfIdlePeersAreSwept(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xBD}, 32)))
	now := time.Now()
	server.now = func() time.Time { return now }
	// A new peer every 1/100 of the statistics window: at most about a hundred
	// windows are live at any time, the rest have run out and may be dropped.
	for i := 0; i < 3*metricsSweepSize; i++ {
		server.updatePeerMetrics(fmt.Sprintf("peer-%d", i), true)
		now = now.Add(invalidRateWindow / 100)
	}
	server.mu.Lock()
	got := len(server.metrics)
	server.mu.Unlock()
	if got > metricsSweepSize {
		t.Fatalf("message statistics table holds %d peers, expected at most %d", got, metricsSweepSize)
	}
}

// requestRecorder counts the messages the peer-to-peer server hands to it.
type requestRecorder struct {
	mu    sync.Mutex
	types map[byte]int
	from  []string
}

func (r *requestRecorder) HandleMessage(msg *Message) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.types == nil {
		r.types = make(map[byte]int)
	}
	r.types[msg.Type]++
	return nil
}

func (r *requestRecorder) count(t byte) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.types[t]
}

// peerRecorder is a requestRecorder that also wants to know the sender.
type peerRecorder struct {
	requestRecorder
}

func (r *peerRecorder) HandlePeerMessage(from PeerSender, msg *Message) error {
	r.mu.Lock()
	r.from = append(r.from, from.ID())
	r.mu.Unlock()
	return r.HandleMessage(msg)
}

// peerOnPipe starts a peer's read loop on a pipe. The function it returns writes
// n copies of a message type into the pipe, as fast as the pipe takes them, and
// its channel is closed once they are all written.
func peerOnPipe(t *testing.T, server *Server, id string, remote net.Addr, persistent bool) (*Peer, func(msgType byte, n int) <-chan struct{}) {
	t.Helper()
	left, right := net.Pipe()
	t.Cleanup(func() {
		left.Close()
		right.Close()
	})
	conn := addrConn{Conn: left, remote: remote}
	peer := newPeer(id, "test/1.0", conn, bufio.NewReader(conn), server, true, persistent, "")
	server.mu.Lock()
	server.peers[peer.id] = peer
	server.mu.Unlock()
	go peer.readLoop()

	return peer, func(msgType byte, n int) <-chan struct{} {
		line, err := json.Marshal(&Message{Type: msgType, Payload: []byte("{}")})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		line = append(line, '\n')
		done := make(chan struct{})
		go func() {
			defer close(done)
			for i := 0; i < n; i++ {
				right.SetWriteDeadline(time.Now().Add(2 * time.Second))
				if _, err := right.Write(line); err != nil {
					return
				}
			}
		}()
		return done
	}
}

// floodPeer starts a peer's read loop on a pipe and writes n copies of a
// message type into it, as fast as the pipe takes them.
func floodPeer(t *testing.T, server *Server, id string, remote net.Addr, persistent bool, msgType byte, n int) *Peer {
	t.Helper()
	peer, send := peerOnPipe(t, server, id, remote, persistent)
	send(msgType, n)
	return peer
}

func requestFloodConfig(seed byte) ServerConfig {
	cfg := baseConfig(bytes.Repeat([]byte{seed}, 32))
	cfg.RateMsgsPerSec = 100000
	cfg.RateBurst = 100000
	cfg.ReadTimeout = 5 * time.Second
	cfg.PingInterval = 0
	return cfg
}

func TestChainDataRequestsAreLimitedPerAddress(t *testing.T) {
	cases := []struct {
		name  string
		msg   byte
		burst int
	}{
		{"GetBlocks", MsgTypeGetBlocks, int(getBlocksBurstPerIP)},
		{"GetStatus", MsgTypeGetStatus, int(getStatusBurstPerIP)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := &requestRecorder{}
			server := NewServer(handler, mustKey(t), requestFloodConfig(0xC1))
			peer := floodPeer(t, server, "asker", tcpAddr("203.0.113.30", 44000), false, tc.msg, 10*tc.burst)

			waitUntil(t, 3*time.Second, "the budget to be spent", func() bool { return handler.count(tc.msg) >= tc.burst })
			time.Sleep(300 * time.Millisecond)
			if got := handler.count(tc.msg); got > tc.burst+1 {
				t.Fatalf("expected at most %d %s requests to reach the handler, got %d", tc.burst+1, tc.name, got)
			}
			// Going over the budget costs the requests, not the connection: an
			// honest node can be led into sending many of them.
			select {
			case <-peer.closed:
				t.Fatalf("a peer over its request budget must not be disconnected")
			default:
			}
			if status := server.reputation.Snapshot(time.Now())["asker"]; status.Misbehavior != 0 || status.Score < 0 || status.Banned {
				t.Fatalf("a peer over its request budget must not be blamed for it, got %+v", status)
			}
		})
	}
}

// A peer that overshoots its budget, as an honest node catching up on a long
// chain does, is answered again as soon as the budget has refilled.
func TestARequesterOverItsBudgetIsServedAgainOnceTheBudgetRefills(t *testing.T) {
	handler := &requestRecorder{}
	server := NewServer(handler, mustKey(t), requestFloodConfig(0xC5))
	peer, send := peerOnPipe(t, server, "syncer", tcpAddr("203.0.113.32", 44002), false)

	<-send(MsgTypeGetBlocks, 4*int(getBlocksBurstPerIP))
	time.Sleep(200 * time.Millisecond)
	served := handler.count(MsgTypeGetBlocks)
	if served == 0 || served > int(getBlocksBurstPerIP)+1 {
		t.Fatalf("expected between 1 and %d requests to be served from the burst, got %d", int(getBlocksBurstPerIP)+1, served)
	}

	// Two requests' worth of budget comes back.
	time.Sleep(time.Duration(2 / getBlocksRatePerIP * float64(time.Second)))
	<-send(MsgTypeGetBlocks, 1)
	waitUntil(t, 2*time.Second, "a request after the budget refilled to be served", func() bool {
		return handler.count(MsgTypeGetBlocks) > served
	})
	select {
	case <-peer.closed:
		t.Fatalf("the peer was disconnected for asking too fast")
	default:
	}
}

func TestChainDataRequestsFromAConfiguredPersistentPeerAreNotLimited(t *testing.T) {
	handler := &requestRecorder{}
	server := NewServer(handler, mustKey(t), requestFloodConfig(0xC2))
	total := 5 * int(getBlocksBurstPerIP)
	peer := floodPeer(t, server, "validator", tcpAddr("203.0.113.31", 44001), true, MsgTypeGetBlocks, total)

	waitUntil(t, 3*time.Second, "every request to be handled", func() bool { return handler.count(MsgTypeGetBlocks) == total })
	select {
	case <-peer.closed:
		t.Fatalf("a persistent peer must not be disconnected for asking often")
	default:
	}
}

func TestChainDataRequestsShareABudgetAcrossAddresses(t *testing.T) {
	handler := &requestRecorder{}
	server := NewServer(handler, mustKey(t), requestFloodConfig(0xC3))
	server.getBlocksShared = newTokenBucket(0.001, 10)

	var peers []*Peer
	for i := 0; i < 5; i++ {
		peers = append(peers, floodPeer(t, server, fmt.Sprintf("asker-%d", i), tcpAddr(fmt.Sprintf("203.0.113.%d", 40+i), 45000), false, MsgTypeGetBlocks, 4))
	}
	waitUntil(t, 3*time.Second, "the shared budget to be spent", func() bool { return handler.count(MsgTypeGetBlocks) >= 10 })
	time.Sleep(200 * time.Millisecond)
	if got := handler.count(MsgTypeGetBlocks); got != 10 {
		t.Fatalf("expected the shared budget of 10 to bound the requests handled, got %d", got)
	}
	for _, peer := range peers {
		select {
		case <-peer.closed:
			t.Fatalf("an exhausted shared budget must not be held against %s", peer.id)
		default:
		}
	}
}

func TestPeerAwareHandlersReceiveThePeerTheMessageCameFrom(t *testing.T) {
	handler := &peerRecorder{}
	server := NewServer(handler, mustKey(t), requestFloodConfig(0xC4))
	floodPeer(t, server, "sender", tcpAddr("203.0.113.50", 46000), false, MsgTypeGetStatus, 3)

	waitUntil(t, 3*time.Second, "the messages to be handled", func() bool { return handler.count(MsgTypeGetStatus) == 3 })
	handler.mu.Lock()
	from := append([]string(nil), handler.from...)
	handler.mu.Unlock()
	if len(from) != 3 || strings.Join(from, ",") != "sender,sender,sender" {
		t.Fatalf("expected every message to arrive with its sender, got %v", from)
	}
}
