package p2p

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// The client version in a handshake is chosen by whoever sends it and is not
// covered by the handshake signature. These tests are about how much of it a
// node keeps and logs: a node holds one record per peer it has seen and writes a
// log line for every connection, so the version must stay small whatever a
// remote sends.

// clientVersionLogSink is a goroutine-safe sink for the server log.
type clientVersionLogSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *clientVersionLogSink) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *clientVersionLogSink) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Len()
}

func (l *clientVersionLogSink) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// initPeerWithVersion plays a remote that holds a key of its own and answers
// the server's hello with a validly signed handshake that carries the given
// client version. It returns the remote's end of the connection and what
// initPeer made of the handshake.
func initPeerWithVersion(t *testing.T, server *Server, cfg ServerConfig, version string) (net.Conn, error) {
	t.Helper()
	remote := NewServer(noopHandler{}, mustKey(t), cfg)
	left, right := net.Pipe()
	t.Cleanup(func() { right.Close() })

	errc := make(chan error, 1)
	go func() { errc <- server.initPeer(left, true, false, "") }()

	if _, err := bufio.NewReader(right).ReadBytes('\n'); err != nil {
		t.Fatalf("read local handshake: %v", err)
	}
	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	// The client version is outside the signature, so the signature stays valid.
	packet.ClientVersion = version
	data, err := json.Marshal(packet)
	if err != nil {
		t.Fatalf("marshal handshake: %v", err)
	}
	right.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := right.Write(append(data, '\n')); err != nil {
		t.Fatalf("write handshake: %v", err)
	}
	select {
	case err := <-errc:
		return right, err
	case <-time.After(5 * time.Second):
		t.Fatalf("initPeer did not return")
		return right, nil
	}
}

func TestVerifyHandshakeClientVersionLimits(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xE2}, 32))
	cases := []struct {
		name    string
		version string
		ok      bool
	}{
		{"empty", "", false},
		{"one byte", "v", true},
		{"typical version string", "nhbchain/v1.2.3 (linux/amd64)", true},
		{"printable non-ASCII text", "nhbchain/node-é", true},
		{"exactly the limit", strings.Repeat("v", maxClientVersionLen), true},
		{"one byte over the limit", strings.Repeat("v", maxClientVersionLen+1), false},
		{"far over the limit", strings.Repeat("v", 900<<10), false},
		{"newline", "nhbchain/node\nsecond line", false},
		{"carriage return", "nhbchain/node\r", false},
		{"tab", "nhbchain\t/node", false},
		{"NUL byte", "nhbchain/node\x00", false},
		{"DEL", "nhbchain/node\x7f", false},
		{"escape sequence", "nhbchain/node\x1b[2J", false},
		{"bytes that are not UTF-8", "nhbchain/node\xff\xfe", false},
		{"replacement character", "nhbchain/node�", false},
		{"right-to-left override", "nhbchain/node‮", false},
		{"line separator", "nhbchain/node ", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			local := NewServer(noopHandler{}, mustKey(t), cfg)
			store := newTestPeerstore(t)
			local.SetPeerstore(store)
			remote := NewServer(noopHandler{}, mustKey(t), cfg)

			packet, err := remote.buildHandshake()
			if err != nil {
				t.Fatalf("build handshake: %v", err)
			}
			packet.ClientVersion = tc.version
			err = local.verifyHandshake(packet)
			if tc.ok {
				if err != nil {
					t.Fatalf("a client version of %d bytes was refused: %v", len(tc.version), err)
				}
				return
			}
			if err == nil {
				t.Fatalf("a client version of %d bytes was accepted", len(tc.version))
			}
			// The error is logged, so it must not carry the value.
			if len(err.Error()) > 512 {
				t.Fatalf("the error is %d bytes long and must not carry the client version", len(err.Error()))
			}
			// The version is not signed: nothing may be held against the node ID the
			// handshake names.
			requireNothingHeldAgainst(t, local, store, remote.nodeID)

			// The refusal happens before the nonce is used, so the same node can
			// connect with a valid version straight away.
			packet.ClientVersion = "test/1.0"
			if err := local.verifyHandshake(packet); err != nil {
				t.Fatalf("the same handshake with a valid client version was refused: %v", err)
			}
		})
	}
}

// A validly signed handshake with an oversized client version must not make the
// node keep the version in a peer record, nor write it to the log.
func TestHandshakeWithOversizedClientVersionLeavesNothingBehind(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xE1}, 32))
	cfg.MaxPeers = 64
	cfg.MaxInbound = 64
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	logs := &clientVersionLogSink{}
	server.logger = slog.New(slog.NewTextHandler(logs, nil))

	const peers = 8
	huge := strings.Repeat("A", 900<<10)
	for i := 0; i < peers; i++ {
		conn, err := initPeerWithVersion(t, server, cfg, huge)
		conn.Close()
		if err == nil {
			t.Errorf("peer %d: a client version of %d bytes was accepted", i, len(huge))
		}
	}

	server.mu.RLock()
	connected := len(server.peers)
	records := len(server.records)
	retained := 0
	for _, rec := range server.records {
		retained += len(rec.Version)
	}
	server.mu.RUnlock()

	if connected != 0 || records != 0 {
		t.Errorf("refused handshakes left %d connected peers and %d records", connected, records)
	}
	if retained != 0 {
		t.Errorf("%d bytes of client version are retained in peer records", retained)
	}
	if logs.Len() > 4096 {
		t.Errorf("%d bytes were written to the log for %d refused handshakes", logs.Len(), peers)
	}
}

func TestHandshakeWithClientVersionAtTheLimitIsAcceptedAndRecorded(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xE3}, 32))
	server := NewServer(noopHandler{}, mustKey(t), cfg)

	version := strings.Repeat("v", maxClientVersionLen)
	conn, err := initPeerWithVersion(t, server, cfg, version)
	if err != nil {
		t.Fatalf("a client version of %d bytes was refused: %v", len(version), err)
	}
	defer conn.Close()

	server.mu.RLock()
	defer server.mu.RUnlock()
	if len(server.peers) != 1 || len(server.records) != 1 {
		t.Fatalf("expected one connected peer and one record, got %d and %d", len(server.peers), len(server.records))
	}
	for _, rec := range server.records {
		if rec.Version != version {
			t.Fatalf("the record holds a version of %d bytes, expected %d", len(rec.Version), len(version))
		}
	}
}

// Refusing a handshake is logged with its error; the error must not make the
// log line as long as the version was.
func TestRefusedClientVersionIsNotWrittenToTheLog(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xE4}, 32))
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	logs := &clientVersionLogSink{}
	server.logger = slog.New(slog.NewTextHandler(logs, nil))
	remote := NewServer(noopHandler{}, mustKey(t), cfg)

	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	packet.ClientVersion = strings.Repeat("B", 64<<10)
	dialPacket(t, server, packet)

	waitUntil(t, 2*time.Second, "the refusal to be logged", func() bool { return logs.Len() > 0 })
	if got := logs.String(); strings.Contains(got, "BBBBBBBB") || len(got) > 4096 {
		t.Fatalf("the refusal logged %d bytes that include the client version", len(got))
	}
	if server.hasPeer(remote.nodeID) {
		t.Fatalf("the peer was registered")
	}
}

// A node whose own client version peers would refuse must not send it: it would
// be refused by every peer it connects to. The default is used instead.
func TestNewServerReplacesAClientVersionPeersWouldRefuse(t *testing.T) {
	const fallback = "nhbchain/node"
	cases := []struct {
		name    string
		version string
		want    string
	}{
		{"empty", "", fallback},
		{"over the limit", strings.Repeat("v", maxClientVersionLen+1), fallback},
		{"newline", "nhbchain/node\n", fallback},
		{"NUL byte", "nhbchain/node\x00", fallback},
		{"bytes that are not UTF-8", "nhbchain/node\xff", fallback},
		{"custom value", "custom/1.2", "custom/1.2"},
		{"exactly the limit", strings.Repeat("v", maxClientVersionLen), strings.Repeat("v", maxClientVersionLen)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baseConfig(bytes.Repeat([]byte{0xE5}, 32))
			cfg.ClientVersion = tc.version
			server := NewServer(noopHandler{}, mustKey(t), cfg)
			if server.cfg.ClientVersion != tc.want {
				t.Fatalf("the server uses a client version of %d bytes, expected %d", len(server.cfg.ClientVersion), len(tc.want))
			}

			peerCfg := baseConfig(bytes.Repeat([]byte{0xE5}, 32))
			peer := NewServer(noopHandler{}, mustKey(t), peerCfg)
			packet, err := server.buildHandshake()
			if err != nil {
				t.Fatalf("build handshake: %v", err)
			}
			if err := peer.verifyHandshake(packet); err != nil {
				t.Fatalf("a peer refused the handshake this node sends: %v", err)
			}
		})
	}
}

func TestNewServerSaysWhenItReplacesTheConfiguredClientVersion(t *testing.T) {
	logs := &clientVersionLogSink{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	cfg := baseConfig(bytes.Repeat([]byte{0xE6}, 32))
	cfg.ClientVersion = ""
	NewServer(noopHandler{}, mustKey(t), cfg)
	if strings.Contains(logs.String(), "Configured client version") {
		t.Fatalf("an unset client version was reported: %s", logs.String())
	}

	cfg.ClientVersion = strings.Repeat("C", 64<<10)
	NewServer(noopHandler{}, mustKey(t), cfg)
	got := logs.String()
	if !strings.Contains(got, "Configured client version") {
		t.Fatalf("a client version that was replaced was not reported: %s", got)
	}
	if strings.Contains(got, "CCCCCCCC") || len(got) > 4096 {
		t.Fatalf("the report is %d bytes long and includes the client version", len(got))
	}
}

// With every record holding the longest version a handshake may carry, the
// table of peers seen keeps no more than about a mebibyte of versions.
func TestPeerRecordsHoldABoundedNumberOfVersionBytes(t *testing.T) {
	server := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xE7}, 32)))
	version := strings.Repeat("v", maxClientVersionLen)
	for i := 0; i < maxPeerRecords+500; i++ {
		server.recordPeerHandshake(&handshakePacket{
			handshakeMessage: handshakeMessage{ClientVersion: version},
			nodeID:           fmt.Sprintf("0x%064x", i+1),
		})
	}
	server.mu.RLock()
	records := len(server.records)
	retained := 0
	for _, rec := range server.records {
		retained += len(rec.Version)
	}
	server.mu.RUnlock()

	if records > maxPeerRecords {
		t.Fatalf("peer records hold %d peers, cap is %d", records, maxPeerRecords)
	}
	if retained > 1<<20 {
		t.Fatalf("peer records retain %d bytes of client versions, expected at most %d", retained, 1<<20)
	}
}
