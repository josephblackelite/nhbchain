package p2p

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// PL-R5T-20 and PL-DC-25: the v1 handshake is a replayable bearer token. The extension adds a
// second round in which each side signs both sides' fresh challenges; it is offered in a field a
// v1 peer ignores, so it is negotiated, never imposed.

var challengeGenesis = bytes.Repeat([]byte{0xC7}, 32)

func challengeServer(t *testing.T, mode string, addrs ...string) *Server {
	t.Helper()
	cfg := baseConfig(challengeGenesis)
	cfg.HandshakeChallenge = mode
	cfg.HandshakeTimeout = 700 * time.Millisecond
	s := NewServer(noopHandler{}, mustKey(t), cfg)
	for _, a := range addrs {
		s.addListenAddress(a)
	}
	return s
}

// tcpPair returns the two ends of a loopback TCP connection (bytes written by one side are
// buffered, so both sides can write their hello before reading, as real peers do).
func tcpPair(t *testing.T) (dialer, listener net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			accepted <- nil
			return
		}
		accepted <- c
	}()
	d, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	l := <-accepted
	if l == nil {
		t.Fatalf("accept failed")
	}
	t.Cleanup(func() { d.Close(); l.Close() })
	return d, l
}

type handshakeResult struct {
	packet *handshakePacket
	err    error
}

// run performs both sides of a handshake over a loopback pair, a in the dialer role and b in the
// listener role.
func runHandshakePair(t *testing.T, a, b *Server) (fromA, fromB handshakeResult) {
	t.Helper()
	d, l := tcpPair(t)
	resA := make(chan handshakeResult, 1)
	resB := make(chan handshakeResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), a.handshakeTimeout)
		defer cancel()
		p, err := a.handshakeAs(ctx, d, bufio.NewReader(d), roleDialer)
		resA <- handshakeResult{p, err}
	}()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), b.handshakeTimeout)
		defer cancel()
		p, err := b.handshakeAs(ctx, l, bufio.NewReader(l), roleListener)
		resB <- handshakeResult{p, err}
	}()
	return <-resA, <-resB
}

// ---- negotiation ------------------------------------------------------------------------

func TestHandshakeChallenge_TwoUpgradedNodesProveTheirKeys(t *testing.T) {
	a, b := challengeServer(t, "auto"), challengeServer(t, "")
	ra, rb := runHandshakePair(t, a, b)
	if ra.err != nil || rb.err != nil {
		t.Fatalf("handshake failed: %v / %v", ra.err, rb.err)
	}
	if !ra.packet.challenged || !rb.packet.challenged {
		t.Fatalf("both nodes offered the extension; both proofs must have been checked (%v, %v)", ra.packet.challenged, rb.packet.challenged)
	}
	if ra.packet.nodeID != b.nodeID || rb.packet.nodeID != a.nodeID {
		t.Fatalf("the proofs were checked against the wrong identities")
	}
}

func TestHandshakeChallenge_ANodeThatDoesNotOfferItIsAV1Peer(t *testing.T) {
	for _, mode := range []string{"off"} {
		a, b := challengeServer(t, "auto"), challengeServer(t, mode)
		ra, rb := runHandshakePair(t, a, b)
		if ra.err != nil || rb.err != nil {
			t.Fatalf("auto against %q: %v / %v", mode, ra.err, rb.err)
		}
		if ra.packet.challenged || rb.packet.challenged {
			t.Fatalf("auto against %q: no proof can have been exchanged", mode)
		}
		// The reverse roles too.
		rb2, ra2 := runHandshakePair(t, challengeServer(t, mode), challengeServer(t, "auto"))
		if ra2.err != nil || rb2.err != nil || ra2.packet.challenged || rb2.packet.challenged {
			t.Fatalf("%q against auto in the other roles: %v / %v", mode, ra2.err, rb2.err)
		}
	}
}

// A real v1 peer: its hello is built without the extension and it never sends or waits for a
// second frame. The upgraded node must complete the handshake and send it nothing more.
func TestHandshakeChallenge_AV1PeerGetsNoSecondFrame(t *testing.T) {
	up := challengeServer(t, "auto")
	old := challengeServer(t, "off")
	d, l := tcpPair(t)

	done := make(chan handshakeResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), up.handshakeTimeout)
		defer cancel()
		p, err := up.handshakeAs(ctx, l, bufio.NewReader(l), roleListener)
		done <- handshakeResult{p, err}
	}()

	oldHello, err := old.buildHandshake()
	if err != nil {
		t.Fatalf("build v1 hello: %v", err)
	}
	if oldHello.Ext != nil {
		t.Fatalf("a v1 hello must not carry the extension")
	}
	data, _ := json.Marshal(oldHello)
	if _, err := d.Write(append(data, '\n')); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	reader := bufio.NewReader(d)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatalf("read the upgraded node's hello: %v", err)
	}
	var theirs handshakePacket
	if err := json.Unmarshal(line, &theirs); err != nil {
		t.Fatalf("decode hello: %v", err)
	}
	if theirs.Ext == nil || theirs.Ext.Version != handshakeExtVersion {
		t.Fatalf("an upgraded node must offer the extension in its hello")
	}
	// The v1 verifier accepts this hello: the v1 signature is over the same fields as before.
	if err := old.verifyHandshake(&theirs); err != nil {
		t.Fatalf("a v1 node refused the upgraded node's hello: %v", err)
	}
	res := <-done
	if res.err != nil {
		t.Fatalf("the upgraded node refused a v1 peer in auto mode: %v", res.err)
	}
	if res.packet.challenged {
		t.Fatalf("no proof can have been exchanged with a v1 peer")
	}
	_ = d.SetReadDeadline(time.Now().Add(150 * time.Millisecond))
	if extra, err := reader.ReadBytes('\n'); err == nil {
		t.Fatalf("the upgraded node sent a v1 peer a second frame: %q", extra)
	}
}

func TestHandshakeChallenge_RequireRefusesAV1PeerAndHoldsNothingAgainstIt(t *testing.T) {
	strict := challengeServer(t, "require")
	old := challengeServer(t, "off")
	rs, ro := runHandshakePair(t, strict, old)
	if !errors.Is(rs.err, errHandshakeChallengeNeeded) {
		t.Fatalf("a node in require mode must refuse a v1 peer, got %v", rs.err)
	}
	_ = ro
	requireNothingHeldAgainst(t, strict, nil, old.nodeID)
	// Two upgraded nodes still work in require mode.
	r1, r2 := runHandshakePair(t, challengeServer(t, "require"), challengeServer(t, "require"))
	if r1.err != nil || r2.err != nil {
		t.Fatalf("require against require: %v / %v", r1.err, r2.err)
	}
}

func TestHandshakeChallenge_UnknownModesAreAuto(t *testing.T) {
	for value, want := range map[string]challengeMode{"": challengeAuto, "auto": challengeAuto, "AUTO": challengeAuto, " off ": challengeOff, "require": challengeRequire, "Require": challengeRequire} {
		got, ok := parseChallengeMode(value)
		if !ok || got != want {
			t.Fatalf("parseChallengeMode(%q) = %v, %v", value, got, ok)
		}
	}
	if got, ok := parseChallengeMode("strict"); ok || got != challengeAuto {
		t.Fatalf("an unknown mode must be reported and treated as auto")
	}
	if s := challengeServer(t, "nonsense"); s.challengeMode != challengeAuto {
		t.Fatalf("an unknown configured mode must give auto")
	}
	if s := challengeServer(t, "require"); s.challengeMode != challengeRequire {
		t.Fatalf("require must be carried to the server")
	}
}

// ---- the attacks the extension closes -------------------------------------------------

// recordHello plays a victim's side of one connection with a recorder and returns the hello it
// sent and the proof it would send for a given counterpart: what a link observer sees.
func recordHello(t *testing.T, victim *Server) *handshakePacket {
	t.Helper()
	hello, err := victim.buildHandshakeExt(true)
	if err != nil {
		t.Fatalf("build hello: %v", err)
	}
	return hello
}

// serveAsListener runs target's handshake as the listener on one end of a loopback pair and gives
// the test the other end.
func serveAsListener(t *testing.T, target *Server) (peer net.Conn, result chan handshakeResult) {
	t.Helper()
	d, l := tcpPair(t)
	_ = d.SetDeadline(time.Now().Add(3 * time.Second))
	result = make(chan handshakeResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), target.handshakeTimeout)
		defer cancel()
		p, err := target.handshakeAs(ctx, l, bufio.NewReader(l), roleListener)
		result <- handshakeResult{p, err}
	}()
	return d, result
}

func sendFrame(t *testing.T, conn net.Conn, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(append(data, '\n')); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// The replay: an attacker presents a hello that the victim signed on another connection. It
// cannot answer the target's fresh challenge, whatever it sends next.
func TestHandshakeChallenge_AReplayedHelloCannotBeAnsweredWithAnyRecordedProof(t *testing.T) {
	victim := challengeServer(t, "auto")
	target := challengeServer(t, "auto")

	// A proof the victim really made, for some other connection (with another listener).
	staleProof := func(recorded *handshakePacket) *handshakeAuth {
		other := challengeServer(t, "auto")
		otherHello, _ := other.buildHandshakeExt(true)
		otherChallenge, _ := decodeChallenge(otherHello.Ext.Challenge)
		staleDigest := authDigest(victim.cfg.ChainID, victim.genesis, roleDialer, &recorded.handshakeMessage, recorded.challenge, &otherHello.handshakeMessage, otherChallenge)
		staleSig, err := ethcrypto.Sign(staleDigest, victim.privKey.PrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		return &handshakeAuth{Sig: encodeHex(staleSig)}
	}
	proofs := map[string]func(*handshakePacket) *handshakeAuth{
		"the proof of another connection": staleProof,
		"garbage":                         func(*handshakePacket) *handshakeAuth { return &handshakeAuth{Sig: "0x" + strings.Repeat("11", 65)} },
		"an empty proof":                  func(*handshakePacket) *handshakeAuth { return &handshakeAuth{Sig: ""} },
	}
	for name, makeProof := range proofs {
		t.Run(name, func(t *testing.T) {
			// A recorded hello can be shown to a node once: the v1 nonce guard refuses a second showing
			// before any proof is asked for, so each case records its own.
			recorded := recordHello(t, victim)
			proof := makeProof(recorded)
			peer, result := serveAsListener(t, target)
			reader := bufio.NewReader(peer)
			if _, err := reader.ReadBytes('\n'); err != nil {
				t.Fatalf("read the target's hello: %v", err)
			}
			sendFrame(t, peer, recorded)
			if _, err := reader.ReadBytes('\n'); err != nil { // the target's own proof frame
				t.Fatalf("read the target's proof: %v", err)
			}
			sendFrame(t, peer, proof)
			res := <-result
			if res.err == nil {
				t.Fatalf("a replayed hello was accepted as %s", victim.nodeID)
			}
			requireNothingHeldAgainst(t, target, nil, victim.nodeID)
		})
	}

	t.Run("no proof at all", func(t *testing.T) {
		recorded := recordHello(t, victim)
		peer, result := serveAsListener(t, target)
		reader := bufio.NewReader(peer)
		_, _ = reader.ReadBytes('\n')
		sendFrame(t, peer, recorded)
		if res := <-result; res.err == nil {
			t.Fatalf("a replayed hello with no proof was accepted")
		}
	})
}

// The downgrade that the extension cannot prevent on its own: strip the offer from a recorded hello
// and a node in auto mode takes it as a v1 hello. A node in require mode does not.
func TestHandshakeChallenge_StrippingTheOfferIsOnlyPossibleAgainstAutoMode(t *testing.T) {
	victim := challengeServer(t, "auto")
	v1 := recordHello(t, victim)
	v1.Ext = nil // the v1 signature does not cover it, so the hello still verifies

	auto := challengeServer(t, "auto")
	peer, result := serveAsListener(t, auto)
	reader := bufio.NewReader(peer)
	_, _ = reader.ReadBytes('\n')
	sendFrame(t, peer, v1)
	if res := <-result; res.err != nil {
		t.Fatalf("auto mode is documented to take a hello without the offer as a v1 hello: %v", res.err)
	}

	strict := challengeServer(t, "require")
	peer2, result2 := serveAsListener(t, strict)
	reader2 := bufio.NewReader(peer2)
	_, _ = reader2.ReadBytes('\n')
	sendFrame(t, peer2, v1)
	if res := <-result2; !errors.Is(res.err, errHandshakeChallengeNeeded) {
		t.Fatalf("require mode must refuse a hello without the offer, got %v", res.err)
	}
}

// A proof is for one role: the proof a node made as the listener is no use to a node that expects a
// listener's proof from a dialer, and the other way round.
func TestHandshakeChallenge_AProofBelongsToItsRole(t *testing.T) {
	a := challengeServer(t, "auto")
	b := challengeServer(t, "auto")
	helloA, _ := a.buildHandshakeExt(true)
	helloB, _ := b.buildHandshakeExt(true)
	chA, chB := helloA.challenge, helloB.challenge

	// What b checks when it is the listener and a is the dialer: a's proof as a dialer.
	asDialer := authDigest(a.cfg.ChainID, a.genesis, roleDialer, &helloA.handshakeMessage, chA, &helloB.handshakeMessage, chB)
	asListener := authDigest(a.cfg.ChainID, a.genesis, roleListener, &helloA.handshakeMessage, chA, &helloB.handshakeMessage, chB)
	if bytes.Equal(asDialer, asListener) {
		t.Fatalf("the role is not covered by the proof")
	}
	// Reflection: a's proof for b, presented to a as b's proof.
	toB := authDigest(a.cfg.ChainID, a.genesis, roleDialer, &helloA.handshakeMessage, chA, &helloB.handshakeMessage, chB)
	toA := authDigest(a.cfg.ChainID, a.genesis, roleDialer, &helloB.handshakeMessage, chB, &helloA.handshakeMessage, chA)
	if bytes.Equal(toB, toA) {
		t.Fatalf("a proof can be reflected back to its signer")
	}
}

func TestHandshakeChallenge_TheProofCoversTheClaimedAddressesAndVersion(t *testing.T) {
	a := challengeServer(t, "auto", "198.51.100.7:30303")
	b := challengeServer(t, "auto")
	helloA, _ := a.buildHandshakeExt(true)
	helloB, _ := b.buildHandshakeExt(true)
	base := authDigest(a.cfg.ChainID, a.genesis, roleDialer, &helloA.handshakeMessage, helloA.challenge, &helloB.handshakeMessage, helloB.challenge)

	tampered := helloA.handshakeMessage
	tampered.ListenAddrs = []string{"203.0.113.9:30303"}
	if bytes.Equal(base, authDigest(a.cfg.ChainID, a.genesis, roleDialer, &tampered, helloA.challenge, &helloB.handshakeMessage, helloB.challenge)) {
		t.Fatalf("the listen addresses are not covered")
	}
	retitled := helloA.handshakeMessage
	retitled.ClientVersion = "other/9.9"
	if bytes.Equal(base, authDigest(a.cfg.ChainID, a.genesis, roleDialer, &retitled, helloA.challenge, &helloB.handshakeMessage, helloB.challenge)) {
		t.Fatalf("the client version is not covered")
	}
	otherChain := authDigest(a.cfg.ChainID+1, a.genesis, roleDialer, &helloA.handshakeMessage, helloA.challenge, &helloB.handshakeMessage, helloB.challenge)
	if bytes.Equal(base, otherChain) {
		t.Fatalf("the chain id is not covered")
	}
	otherGenesis := authDigest(a.cfg.ChainID, bytes.Repeat([]byte{1}, 32), roleDialer, &helloA.handshakeMessage, helloA.challenge, &helloB.handshakeMessage, helloB.challenge)
	if bytes.Equal(base, otherGenesis) {
		t.Fatalf("the genesis is not covered")
	}
	otherPeer := authDigest(a.cfg.ChainID, a.genesis, roleDialer, &helloA.handshakeMessage, helloA.challenge, &helloB.handshakeMessage, bytes.Repeat([]byte{9}, 32))
	if bytes.Equal(base, otherPeer) {
		t.Fatalf("the verifier's challenge is not covered")
	}
	// The signer's own challenge and identity.
	if bytes.Equal(base, authDigest(a.cfg.ChainID, a.genesis, roleDialer, &helloA.handshakeMessage, bytes.Repeat([]byte{7}, 32), &helloB.handshakeMessage, helloB.challenge)) {
		t.Fatalf("the signer's own challenge is not covered")
	}
	renamed := helloA.handshakeMessage
	renamed.NodeID = "0x" + strings.Repeat("ab", 64)
	if bytes.Equal(base, authDigest(a.cfg.ChainID, a.genesis, roleDialer, &renamed, helloA.challenge, &helloB.handshakeMessage, helloB.challenge)) {
		t.Fatalf("the signer's node id is not covered")
	}
	c := challengeServer(t, "auto")
	helloC, _ := c.buildHandshakeExt(true)
	if bytes.Equal(base, authDigest(a.cfg.ChainID, a.genesis, roleDialer, &helloA.handshakeMessage, helloA.challenge, &helloC.handshakeMessage, helloB.challenge)) {
		t.Fatalf("the verifier's identity is not covered")
	}
}

// A hello with a malformed challenge cannot be answered.
func TestHandshakeChallenge_AMalformedChallengeIsRefused(t *testing.T) {
	target := challengeServer(t, "auto")
	for name, mutate := range map[string]func(h *handshakePacket){
		"too short":             func(h *handshakePacket) { h.Ext.Challenge = "0x" + strings.Repeat("ab", 16) },
		"not hex":               func(h *handshakePacket) { h.Ext.Challenge = "0xzz" },
		"empty":                 func(h *handshakePacket) { h.Ext.Challenge = "" },
		"older version offered": nil,
	} {
		if mutate == nil {
			continue
		}
		t.Run(name, func(t *testing.T) {
			hello := recordHello(t, challengeServer(t, "auto"))
			mutate(hello)
			peer, result := serveAsListener(t, target)
			reader := bufio.NewReader(peer)
			_, _ = reader.ReadBytes('\n')
			sendFrame(t, peer, hello)
			if res := <-result; res.err == nil || !errors.Is(res.err, errHandshakeAuthFailure) {
				t.Fatalf("a malformed challenge must be an auth failure, got %v", res.err)
			}
		})
	}
}

// The hello a v1 node verifies is byte for byte what it verified before: the extension is outside
// the signature.
func TestHandshakeChallenge_TheV1DigestIsUnchanged(t *testing.T) {
	s := challengeServer(t, "auto")
	with, err := s.buildHandshakeExt(true)
	if err != nil {
		t.Fatal(err)
	}
	nonce, _ := decodeHex(with.Nonce)
	digest, err := handshakeDigest(with.ChainID, challengeGenesis, nonce, with.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := decodeHex(with.Signature)
	pub, err := ethcrypto.SigToPub(digest, sig)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if normalizeHex(deriveNodeIDFromPub(pub)) != with.NodeID {
		t.Fatalf("the v1 signature of a hello that carries the extension does not verify with the v1 digest")
	}
	var wire map[string]any
	data, _ := json.Marshal(with)
	_ = json.Unmarshal(data, &wire)
	if _, ok := wire["ext"]; !ok {
		t.Fatalf("the offer is missing from the hello")
	}
	v1, _ := s.buildHandshake()
	data, _ = json.Marshal(v1)
	if strings.Contains(string(data), `"ext"`) {
		t.Fatalf("a v1 hello must not mention the extension")
	}
}

// ---- through real servers: roles, negotiation and refusal at the connection level ----------

func startChallengeServer(t *testing.T, mode string) (*Server, string) {
	t.Helper()
	cfg := baseConfig(challengeGenesis)
	cfg.HandshakeChallenge = mode
	cfg.ReadTimeout = 30 * time.Second
	cfg.WriteTimeout = 5 * time.Second
	cfg.HandshakeTimeout = 3 * time.Second
	cfg.RateMsgsPerSec = 1000
	cfg.RateBurst = 1000
	server := NewServer(noopHandler{}, mustKey(t), cfg)
	go server.Start()
	t.Cleanup(func() { server.Stop() })
	var addr string
	waitUntil(t, 3*time.Second, "the server to listen", func() bool {
		for _, candidate := range server.ListenAddresses() {
			if !strings.HasSuffix(candidate, ":0") {
				addr = candidate
				return true
			}
		}
		return false
	})
	return server, addr
}

// peerChallenged reports whether server holds a connection to id and whether that peer proved its key.
func peerChallenged(server *Server, id string) (connected, challenged bool) {
	server.mu.Lock()
	defer server.mu.Unlock()
	peer, ok := server.peers[id]
	if !ok {
		return false, false
	}
	return true, peer.challenged
}

func TestHandshakeChallenge_RealServersProveTheirKeysInTheRolesTheyPlay(t *testing.T) {
	a, _ := startChallengeServer(t, "auto")
	b, bAddr := startChallengeServer(t, "")
	if err := a.Connect(bAddr); err != nil {
		t.Fatalf("connect: %v", err)
	}
	waitUntil(t, 3*time.Second, "both sides to hold the connection", func() bool {
		return a.hasPeer(b.nodeID) && b.hasPeer(a.nodeID)
	})
	if ok, challenged := peerChallenged(a, b.nodeID); !ok || !challenged {
		t.Fatalf("the dialer did not check the listener's proof (connected %v, challenged %v)", ok, challenged)
	}
	if ok, challenged := peerChallenged(b, a.nodeID); !ok || !challenged {
		t.Fatalf("the listener did not check the dialer's proof (connected %v, challenged %v)", ok, challenged)
	}
}

func TestHandshakeChallenge_RealServersFallBackToV1WithAnUnchangedPeer(t *testing.T) {
	a, _ := startChallengeServer(t, "auto")
	b, bAddr := startChallengeServer(t, "off")
	if err := a.Connect(bAddr); err != nil {
		t.Fatalf("connect: %v", err)
	}
	waitUntil(t, 3*time.Second, "both sides to hold the connection", func() bool {
		return a.hasPeer(b.nodeID) && b.hasPeer(a.nodeID)
	})
	if _, challenged := peerChallenged(a, b.nodeID); challenged {
		t.Fatalf("no proof can have been checked of a node that does not offer it")
	}
	if _, challenged := peerChallenged(b, a.nodeID); challenged {
		t.Fatalf("a node that does not offer it checked a proof")
	}
}

func TestHandshakeChallenge_RealServersInRequireModeRefuseAnUnchangedPeerBothWays(t *testing.T) {
	strict, strictAddr := startChallengeServer(t, "require")
	old, oldAddr := startChallengeServer(t, "off")
	if err := strict.Connect(oldAddr); err == nil {
		t.Fatalf("a node in require mode dialed an unchanged peer and kept the connection")
	}
	if err := old.Connect(strictAddr); err == nil {
		// The refusal may arrive after the dial returns; the connection must not stay.
		time.Sleep(200 * time.Millisecond)
	}
	if strict.hasPeer(old.nodeID) || old.hasPeer(strict.nodeID) {
		t.Fatalf("a node in require mode holds a connection with an unchanged peer")
	}
}

// A hello that offers an older version of the extension is no offer.
func TestHandshakeChallenge_AnOlderOfferedVersionIsNotAnOffer(t *testing.T) {
	for mode, wantErr := range map[string]bool{"auto": false, "require": true} {
		target := challengeServer(t, mode)
		hello := recordHello(t, challengeServer(t, "auto"))
		hello.Ext.Version = handshakeExtVersion - 1
		peer, result := serveAsListener(t, target)
		reader := bufio.NewReader(peer)
		_, _ = reader.ReadBytes('\n')
		sendFrame(t, peer, hello)
		res := <-result
		if (res.err != nil) != wantErr {
			t.Fatalf("mode %s with an offer of version %d: error %v, want error=%v", mode, hello.Ext.Version, res.err, wantErr)
		}
		if res.err == nil && res.packet.challenged {
			t.Fatalf("mode %s: an older offer was treated as a proof", mode)
		}
	}
}

// Every hello carries a challenge of its own, drawn at random.
func TestHandshakeChallenge_EveryHelloCarriesAFreshChallenge(t *testing.T) {
	s := challengeServer(t, "auto")
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		hello, err := s.buildHandshakeExt(true)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := decodeChallenge(hello.Ext.Challenge)
		if err != nil {
			t.Fatalf("challenge: %v", err)
		}
		if bytes.Equal(raw, make([]byte, handshakeChallengeSize)) {
			t.Fatalf("a challenge of zeros")
		}
		if seen[hello.Ext.Challenge] {
			t.Fatalf("a challenge was used twice")
		}
		seen[hello.Ext.Challenge] = true
		if !bytes.Equal(raw, hello.challenge) {
			t.Fatalf("the challenge kept for checking the proof is not the one sent")
		}
	}
}

// The v1 entry point (used by the tests of the v1 rules) never offers the extension: its hello is a v1
// hello and it does not wait for a second frame.
func TestHandshakeChallenge_TheV1EntryPointOffersNothing(t *testing.T) {
	s := challengeServer(t, "auto")
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	got := make(chan *handshakePacket, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
		defer cancel()
		_, _ = s.performHandshake(ctx, left, bufio.NewReader(left))
	}()
	line, err := bufio.NewReader(right).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read hello: %v", err)
	}
	var hello handshakePacket
	if err := json.Unmarshal(line, &hello); err != nil {
		t.Fatalf("decode hello: %v", err)
	}
	got <- &hello
	if hello.Ext != nil {
		t.Fatalf("the v1 entry point offered the extension")
	}
}
