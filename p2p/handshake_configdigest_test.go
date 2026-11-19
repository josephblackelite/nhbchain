package p2p

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

// PL-R5T-16 / PL-R5T-30: validators that run different consensus settings accept
// different blocks and nothing told them so. The handshake carries a hash of the
// settings (never a value) and each side says so, loudly, when the other's differs.
// The field is advisory: it sits outside the signature, is ignored by builds that
// do not know it, and never refuses or bans a peer.

const (
	digestA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	digestB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestHandshakeCarriesTheConsensusConfigDigest(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xC1}, 32))
	cfg.ConsensusConfigDigest = digestA
	packet, err := NewServer(noopHandler{}, mustKey(t), cfg).buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if packet.ConfigDigest != digestA {
		t.Fatalf("handshake carries %q, want the configured digest", packet.ConfigDigest)
	}
	encoded, _ := json.Marshal(packet)
	if !strings.Contains(string(encoded), `"configDigest":"`+digestA+`"`) {
		t.Fatalf("the digest is not on the wire: %s", encoded)
	}
}

// A node with no digest configured sends the packet it always sent, in its field
// set, so a build that does not know the field is not disturbed.
func TestHandshakeWithoutADigestHasNoDigestField(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xC2}, 32))
	packet, err := NewServer(noopHandler{}, mustKey(t), cfg).buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	encoded, _ := json.Marshal(packet)
	if strings.Contains(string(encoded), "configDigest") {
		t.Fatalf("a node without a digest still sends the field: %s", encoded)
	}
}

func TestDigestDoesNotDisturbTheSignatureCheck(t *testing.T) {
	cfg := baseConfig(bytes.Repeat([]byte{0xC3}, 32))
	cfg.ConsensusConfigDigest = digestA
	local := NewServer(noopHandler{}, mustKey(t), baseConfig(bytes.Repeat([]byte{0xC3}, 32)))
	remote := NewServer(noopHandler{}, mustKey(t), cfg)
	packet, err := remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	if err := local.verifyHandshake(packet); err != nil {
		t.Fatalf("a handshake with a digest was refused: %v", err)
	}
	// Changing it after signing does not break the signature either: it is advisory.
	// (A fresh packet: the first one's nonce is spent.)
	packet, err = remote.buildHandshake()
	if err != nil {
		t.Fatalf("build handshake: %v", err)
	}
	packet.ConfigDigest = digestB
	if err := local.verifyHandshake(packet); err != nil {
		t.Fatalf("a changed digest made the handshake invalid: %v", err)
	}
}

func digestCheckServer(t *testing.T, own string) (*Server, *clientVersionLogSink) {
	t.Helper()
	cfg := baseConfig(bytes.Repeat([]byte{0xC4}, 32))
	cfg.ConsensusConfigDigest = own
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	logs := &clientVersionLogSink{}
	server.logger = slog.New(slog.NewTextHandler(logs, nil))
	return server, logs
}

func TestPeerWithADifferentDigestIsReported(t *testing.T) {
	server, logs := digestCheckServer(t, digestA)
	if !server.checkPeerConfigDigest("peer-one", digestB) {
		t.Fatalf("a different digest was not reported as a difference")
	}
	out := logs.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, "different consensus settings") {
		t.Fatalf("no error line for a peer that runs different consensus settings:\n%s", out)
	}
	if strings.Contains(out, digestB) || strings.Contains(out, digestA) {
		t.Fatalf("the log carries a digest value:\n%s", out)
	}
}

func TestPeerWithTheSameDigestIsNotReported(t *testing.T) {
	server, logs := digestCheckServer(t, digestA)
	if server.checkPeerConfigDigest("peer-one", digestA) {
		t.Fatalf("an identical digest was reported")
	}
	if strings.Contains(logs.String(), "different consensus settings") {
		t.Fatalf("an error line for an identical digest:\n%s", logs.String())
	}
}

// A build that does not send the field (every build until this one) is not a
// mismatch, and neither is a node that has no digest of its own to compare.
func TestNoDigestOnEitherSideIsNotAMismatch(t *testing.T) {
	server, _ := digestCheckServer(t, digestA)
	if server.checkPeerConfigDigest("old-peer", "") {
		t.Fatalf("a peer that sends no digest was reported")
	}
	bare, _ := digestCheckServer(t, "")
	if bare.checkPeerConfigDigest("peer", digestB) {
		t.Fatalf("a node without its own digest reported a difference")
	}
}

// The field is chosen by the sender. Anything that is not 64 hex characters is
// treated as absent and never written to the log.
func TestMalformedPeerDigestIsIgnoredAndNeverLogged(t *testing.T) {
	server, logs := digestCheckServer(t, digestA)
	for name, bad := range map[string]string{
		"too short":   "abcd",
		"too long":    strings.Repeat("a", 65),
		"huge":        strings.Repeat("a", 900<<10),
		"not hex":     strings.Repeat("z", 64),
		"upper case":  strings.Repeat("A", 64),
		"newline":     digestB[:63] + "\n",
		"escape code": "\x1b[2J" + digestB[:59],
	} {
		if server.checkPeerConfigDigest("peer", bad) {
			t.Errorf("%s: reported as a difference", name)
		}
	}
	if logs.Len() != 0 {
		t.Fatalf("malformed digests were written to the log:\n%.200s", logs.String())
	}
}

// Through the real connection path: a peer whose settings differ is reported in the
// log and the connection is still made. The digest is evidence for an operator, not
// a ground for refusing a validator.
func TestConnectionToAPeerWithADifferentDigestIsMadeAndReported(t *testing.T) {
	ownCfg := baseConfig(bytes.Repeat([]byte{0xC5}, 32))
	ownCfg.ConsensusConfigDigest = digestA
	server := NewServer(noopHandler{}, mustKey(t), ownCfg)
	logs := &clientVersionLogSink{}
	server.logger = slog.New(slog.NewTextHandler(logs, nil))

	remoteCfg := ownCfg
	remoteCfg.ConsensusConfigDigest = digestB
	conn, err := initPeerWithVersion(t, server, remoteCfg, "test/1.0")
	if conn != nil {
		defer conn.Close()
	}
	if err != nil {
		t.Fatalf("a peer with other settings was refused: %v", err)
	}
	if !strings.Contains(logs.String(), "different consensus settings") {
		t.Fatalf("the difference was not reported on a real connection:\n%s", logs.String())
	}
}

func TestConnectionToAPeerWithTheSameDigestIsQuiet(t *testing.T) {
	ownCfg := baseConfig(bytes.Repeat([]byte{0xC6}, 32))
	ownCfg.ConsensusConfigDigest = digestA
	server := NewServer(noopHandler{}, mustKey(t), ownCfg)
	logs := &clientVersionLogSink{}
	server.logger = slog.New(slog.NewTextHandler(logs, nil))
	conn, err := initPeerWithVersion(t, server, ownCfg, "test/1.0")
	if conn != nil {
		defer conn.Close()
	}
	if err != nil {
		t.Fatalf("a peer with the same settings was refused: %v", err)
	}
	if strings.Contains(logs.String(), "different consensus settings") {
		t.Fatalf("reported a difference where there is none:\n%s", logs.String())
	}
}
