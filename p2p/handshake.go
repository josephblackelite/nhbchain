package p2p

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

const (
	protocolVersion       uint32 = 1
	handshakeNonceSize           = 12
	handshakeReplayWindow        = 10 * time.Minute
	handshakeDomain              = "nhb-handshake-v1"

	// A handshake advertises at most maxHandshakeListenAddrs addresses; only the
	// first maxHandshakeListenAddrScan entries of the list are looked at.
	maxHandshakeListenAddrs    = 8
	maxHandshakeListenAddrScan = 64

	// handshakeExtVersion is the version of the challenge-response extension (PL-R5T-20,
	// PL-DC-25). It rides in an optional field of the hello that a v1 peer ignores, so the
	// v1 digest, and with it every v1 peer's verdict on the signature, is unchanged.
	handshakeExtVersion    = 2
	handshakeChallengeSize = 32
	handshakeAuthDomain    = "nhb-handshake-auth-v2"

	// The client version is chosen by whoever sends the handshake and is not
	// covered by its signature. It is kept in the peer record and written to the
	// log, so it is limited in size.
	maxClientVersionLen = 256
)

var (
	errHandshakeChainMismatch    = errors.New("handshake chain mismatch")
	errHandshakeGenesisMismatch  = errors.New("handshake genesis mismatch")
	errHandshakeSignatureFailure = errors.New("handshake signature mismatch")
	errHandshakeFrameTooLarge    = errors.New("handshake frame exceeds maximum size")
	errHandshakeChallengeNeeded  = errors.New("handshake challenge required: the peer did not offer it")
	errHandshakeAuthFailure      = errors.New("handshake challenge failure")
)

type handshakeMessage struct {
	ProtocolVersion uint32   `json:"protoVersion"`
	ChainID         uint64   `json:"chainId"`
	GenesisHash     string   `json:"genesisHash"`
	NodeID          string   `json:"nodeId"`
	Nonce           string   `json:"nonce"`
	ClientVersion   string   `json:"clientVersion"`
	ListenAddrs     []string `json:"listenAddrs,omitempty"`
	// Ext offers the challenge-response extension. A peer that does not know it ignores the
	// field; it is not covered by the v1 signature, which is why the proof it leads to is a
	// second signature over both sides' challenges (see authDigest).
	Ext *handshakeExt `json:"ext,omitempty"`
	// ConfigDigest is the root of a hash of the sender's consensus settings (see
	// config.Config.ConsensusDigest): 64 lower-case hex characters, or absent. It is
	// advisory. It sits outside the signature, builds that do not know it ignore
	// it, and a difference is reported, never acted on (PL-R5T-16, PL-R5T-30).
	ConfigDigest string `json:"configDigest,omitempty"`
}

// handshakeExt is the optional extension block of a hello.
type handshakeExt struct {
	Version   int    `json:"v"`
	Challenge string `json:"n"`
}

// handshakeAuth is the frame each side sends after the hellos when both offered the extension.
type handshakeAuth struct {
	Sig string `json:"hs2auth"`
}

type handshakePacket struct {
	handshakeMessage
	Signature string `json:"sig"`

	nodeID string
	pubKey *ecdsa.PublicKey
	addrs  []string

	// challenge is the 32-byte challenge this node put in its own hello, and challenged is set on
	// a remote packet whose sender proved it holds the key by signing both challenges.
	challenge  []byte
	challenged bool
}

// handshakeRole says which side of the connection a node is: the one that dialed or the one
// that accepted. It is part of what the proof signs, so that one side's proof cannot be
// presented back to it or relayed to a node that expects the other side.
type handshakeRole byte

const (
	roleNone     handshakeRole = 0
	roleDialer   handshakeRole = 1
	roleListener handshakeRole = 2
)

func (r handshakeRole) opposite() handshakeRole {
	switch r {
	case roleDialer:
		return roleListener
	case roleListener:
		return roleDialer
	}
	return roleNone
}

// challengeMode is how a node treats the challenge-response extension.
type challengeMode int

const (
	// challengeAuto offers the extension and proves the key to a peer that offers it too; a
	// peer that does not is handled as a v1 peer. The default.
	challengeAuto challengeMode = iota
	// challengeOff does not offer it: the node behaves exactly like a v1 node.
	challengeOff
	// challengeRequire refuses a peer that does not offer it. For use once every peer has
	// upgraded: it is what closes the downgrade, since a link observer can strip the field.
	challengeRequire
)

// challengeModeOrAuto reads the configured mode. A value that is not one of auto, off or require
// is not guessed at: the node falls back to auto and says so.
func challengeModeOrAuto(value string, logger *slog.Logger) challengeMode {
	mode, ok := parseChallengeMode(value)
	if !ok && logger != nil {
		logger.Warn("Unknown handshake challenge mode; using auto", slog.String("value", value))
	}
	return mode
}

func parseChallengeMode(value string) (challengeMode, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "auto":
		return challengeAuto, true
	case "off":
		return challengeOff, true
	case "require":
		return challengeRequire, true
	}
	return challengeAuto, false
}

// performHandshake is the v1 handshake: no extension is offered or expected.
func (s *Server) performHandshake(ctx context.Context, conn net.Conn, reader *bufio.Reader) (*handshakePacket, error) {
	return s.handshakeAs(ctx, conn, reader, roleNone)
}

// handshakeAs performs the handshake in the given role. With a role (and unless the node is set
// not to), it offers the challenge-response extension and, when the peer offers it too, proves
// its key and checks the peer's (PL-R5T-20, PL-DC-25).
func (s *Server) handshakeAs(ctx context.Context, conn net.Conn, reader *bufio.Reader, role handshakeRole) (*handshakePacket, error) {
	offer := role != roleNone && s.challengeMode != challengeOff
	local, err := s.buildHandshakeExt(offer)
	if err != nil {
		return nil, fmt.Errorf("prepare handshake: %w", err)
	}
	if err := writeFrame(ctx, conn, local); err != nil {
		return nil, fmt.Errorf("send handshake: %w", err)
	}

	payload, err := readFrame(ctx, conn, reader, s.cfg.MaxMessageBytes)
	if err != nil {
		return nil, fmt.Errorf("read handshake: %w", err)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("empty handshake from peer")
	}

	var remote handshakePacket
	if err := json.Unmarshal(payload, &remote); err != nil {
		return nil, fmt.Errorf("decode handshake: %w", err)
	}

	if err := s.verifyHandshake(&remote); err != nil {
		return nil, err
	}
	remote.addrs = sanitizeListenAddrs(remote.ListenAddrs)
	if !offer {
		return &remote, nil
	}
	if remote.Ext == nil || remote.Ext.Version < handshakeExtVersion {
		// A v1 peer, or one whose offer was stripped on the way. Handled as v1 unless this node
		// requires the challenge.
		if s.challengeMode == challengeRequire {
			return nil, errHandshakeChallengeNeeded
		}
		return &remote, nil
	}
	if err := s.exchangeChallenge(ctx, conn, reader, role, local, &remote); err != nil {
		return nil, err
	}
	remote.challenged = true
	return &remote, nil
}

// exchangeChallenge runs the second round: each side signs, with the key behind its node ID, a
// digest over both challenges, both node IDs, its role and what its hello said about its
// addresses and version, sends the signature and checks the peer's. A hello that was recorded
// and is played back cannot be answered: the proof has to cover the challenge this node chose
// for this connection.
func (s *Server) exchangeChallenge(ctx context.Context, conn net.Conn, reader *bufio.Reader, role handshakeRole, local, remote *handshakePacket) error {
	remoteChallenge, err := decodeChallenge(remote.Ext.Challenge)
	if err != nil {
		return fmt.Errorf("%w: %v", errHandshakeAuthFailure, err)
	}
	digest := authDigest(s.cfg.ChainID, s.genesis, role, &local.handshakeMessage, local.challenge, &remote.handshakeMessage, remoteChallenge)
	sig, err := ethcrypto.Sign(digest, s.privKey.PrivateKey)
	if err != nil {
		return fmt.Errorf("sign handshake proof: %w", err)
	}
	if err := writeFrame(ctx, conn, handshakeAuth{Sig: encodeHex(sig)}); err != nil {
		return fmt.Errorf("send handshake proof: %w", err)
	}
	payload, err := readFrame(ctx, conn, reader, s.cfg.MaxMessageBytes)
	if err != nil {
		return fmt.Errorf("read handshake proof: %w", err)
	}
	var auth handshakeAuth
	if err := json.Unmarshal(payload, &auth); err != nil {
		return fmt.Errorf("%w: decode: %v", errHandshakeAuthFailure, err)
	}
	sigBytes, err := decodeHex(auth.Sig)
	if err != nil || len(sigBytes) != 65 {
		return fmt.Errorf("%w: bad signature encoding", errHandshakeAuthFailure)
	}
	want := authDigest(s.cfg.ChainID, s.genesis, role.opposite(), &remote.handshakeMessage, remoteChallenge, &local.handshakeMessage, local.challenge)
	recovered, err := ethcrypto.SigToPub(want, sigBytes)
	if err != nil {
		return fmt.Errorf("%w: recover: %v", errHandshakeAuthFailure, err)
	}
	if derived := normalizeHex(deriveNodeIDFromPub(recovered)); derived == "" || !strings.EqualFold(derived, remote.nodeID) {
		// Nothing is held against the node ID: the proof does not come from it.
		return fmt.Errorf("%w: the proof was not made by %s", errHandshakeAuthFailure, remote.nodeID)
	}
	return nil
}

func decodeChallenge(value string) ([]byte, error) {
	raw, err := decodeHex(value)
	if err != nil {
		return nil, fmt.Errorf("challenge encoding: %w", err)
	}
	if len(raw) != handshakeChallengeSize {
		return nil, fmt.Errorf("challenge length %d", len(raw))
	}
	return raw, nil
}

// authDigest is what a node signs to prove its key in the second round: the signer's role and
// node ID, the peer's node ID, both challenges, the chain and genesis, and hashes of the
// addresses and client version the signer's hello claimed. The listen addresses of a hello are
// not covered by the v1 signature; here they are.
func authDigest(chainID uint64, genesis []byte, role handshakeRole, signer *handshakeMessage, signerChallenge []byte, peer *handshakeMessage, peerChallenge []byte) []byte {
	var chain [8]byte
	binary.BigEndian.PutUint64(chain[:], chainID)
	addrs := sha256.Sum256([]byte(strings.Join(signer.ListenAddrs, "\n")))
	version := sha256.Sum256([]byte(signer.ClientVersion))
	data := []byte(handshakeAuthDomain)
	data = append(data, 0)
	data = append(data, chain[:]...)
	data = appendLengthPrefixed(data, genesis)
	data = append(data, byte(role))
	data = appendLengthPrefixed(data, []byte(normalizeHex(signer.NodeID)))
	data = appendLengthPrefixed(data, []byte(normalizeHex(peer.NodeID)))
	data = appendLengthPrefixed(data, signerChallenge)
	data = appendLengthPrefixed(data, peerChallenge)
	data = append(data, addrs[:]...)
	data = append(data, version[:]...)
	return ethcrypto.Keccak256(data)
}

func appendLengthPrefixed(dst, value []byte) []byte {
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(value)))
	dst = append(dst, n[:]...)
	return append(dst, value...)
}

// buildHandshake builds this node's hello as a v1 node would: no extension is offered.
func (s *Server) buildHandshake() (*handshakePacket, error) {
	return s.buildHandshakeExt(false)
}

// buildHandshakeExt builds this node's hello, with the challenge-response extension offered
// when offerChallenge is set.
func (s *Server) buildHandshakeExt(offerChallenge bool) (*handshakePacket, error) {
	nonce := make([]byte, handshakeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate handshake nonce: %w", err)
	}

	nodeID := normalizeHex(s.nodeID)
	if nodeID == "" {
		return nil, fmt.Errorf("invalid local node ID")
	}

	listen := sanitizeListenAddrs(s.ListenAddresses())
	payload := handshakeMessage{
		ProtocolVersion: protocolVersion,
		ChainID:         s.cfg.ChainID,
		GenesisHash:     encodeHex(s.genesis),
		NodeID:          nodeID,
		Nonce:           encodeHex(nonce),
		ClientVersion:   s.cfg.ClientVersion,
		ListenAddrs:     listen,
		ConfigDigest:    s.cfg.ConsensusConfigDigest,
	}
	var challenge []byte
	if offerChallenge {
		challenge = make([]byte, handshakeChallengeSize)
		if _, err := rand.Read(challenge); err != nil {
			return nil, fmt.Errorf("generate handshake challenge: %w", err)
		}
		payload.Ext = &handshakeExt{Version: handshakeExtVersion, Challenge: encodeHex(challenge)}
	}

	digest, err := handshakeDigest(payload.ChainID, s.genesis, nonce, payload.NodeID)
	if err != nil {
		return nil, err
	}
	sig, err := ethcrypto.Sign(digest, s.privKey.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("sign handshake: %w", err)
	}

	packet := &handshakePacket{
		handshakeMessage: payload,
		Signature:        encodeHex(sig),
		nodeID:           nodeID,
		pubKey:           &s.privKey.PrivateKey.PublicKey,
		addrs:            listen,
		challenge:        challenge,
	}
	nonceKey := hex.EncodeToString(nonce)
	if !s.nonceGuard.Remember(nodeID, nonceKey, s.now()) {
		return nil, fmt.Errorf("nonce collision detected")
	}
	return packet, nil
}

func (s *Server) verifyHandshake(packet *handshakePacket) error {
	if packet == nil {
		return fmt.Errorf("nil handshake packet")
	}
	if packet.ProtocolVersion != protocolVersion {
		return fmt.Errorf("unsupported protocol version %d", packet.ProtocolVersion)
	}
	if packet.ClientVersion == "" {
		return fmt.Errorf("handshake missing client version")
	}
	if !validClientVersion(packet.ClientVersion) {
		// The field is not signed, so nothing is held against the node ID, and the
		// value is left out of the message because the message is logged.
		return fmt.Errorf("handshake client version is longer than %d bytes or has characters that are not printable", maxClientVersionLen)
	}
	if strings.TrimSpace(packet.NodeID) == "" {
		return fmt.Errorf("handshake missing node ID")
	}
	if len(packet.Signature) == 0 {
		return fmt.Errorf("handshake missing signature")
	}
	canonicalNodeID := normalizeHex(packet.NodeID)
	if canonicalNodeID == "" {
		return fmt.Errorf("handshake missing node ID")
	}
	if packet.NodeID != canonicalNodeID {
		return fmt.Errorf("handshake node ID must be canonical hex")
	}
	canonicalNonce, ok := canonicalizeNonce(packet.Nonce)
	if !ok {
		return fmt.Errorf("invalid nonce encoding")
	}
	nonceBytes, err := hex.DecodeString(canonicalNonce)
	if err != nil {
		return fmt.Errorf("invalid nonce encoding: %w", err)
	}
	if len(nonceBytes) != handshakeNonceSize {
		return fmt.Errorf("invalid handshake nonce length: %d", len(nonceBytes))
	}
	if packet.Nonce != encodeHex(nonceBytes) {
		return fmt.Errorf("handshake nonce must use canonical encoding")
	}
	if packet.ChainID != s.cfg.ChainID {
		s.markHandshakeViolation(packet.NodeID, handshakeSignedByClaimedNode(packet))
		return fmt.Errorf("%w: chain ID mismatch: remote %d local %d", errHandshakeChainMismatch, packet.ChainID, s.cfg.ChainID)
	}
	remoteGenesis, err := decodeHex(packet.GenesisHash)
	if err != nil {
		return fmt.Errorf("invalid genesis hash encoding: %w", err)
	}
	if len(remoteGenesis) == 0 {
		return fmt.Errorf("handshake missing genesis hash")
	}
	if !bytesEqual(remoteGenesis, s.genesis) {
		s.markHandshakeViolation(packet.NodeID, handshakeSignedByClaimedNode(packet))
		return fmt.Errorf("%w: genesis hash mismatch: remote %x local %x", errHandshakeGenesisMismatch, remoteGenesis, s.genesis)
	}
	sigBytes, err := decodeHex(packet.Signature)
	if err != nil {
		return signatureMismatch("invalid signature encoding: %v", err)
	}
	if len(sigBytes) != 65 {
		return signatureMismatch("invalid handshake signature length: %d", len(sigBytes))
	}

	digest, err := handshakeDigest(packet.ChainID, remoteGenesis, nonceBytes, packet.NodeID)
	if err != nil {
		return signatureMismatch("%v", err)
	}
	recovered, err := ethcrypto.SigToPub(digest, sigBytes)
	if err != nil {
		return signatureMismatch("recover signature: %v", err)
	}
	derived := normalizeHex(deriveNodeIDFromPub(recovered))
	claimed := normalizeHex(packet.NodeID)
	if derived == "" || claimed == "" {
		return signatureMismatch("unable to derive node identity")
	}
	if !strings.EqualFold(derived, claimed) {
		return signatureMismatch("node ID mismatch: derived %s claimed %s", derived, claimed)
	}

	nonceKey := hex.EncodeToString(nonceBytes)
	if !s.nonceGuard.Remember(derived, nonceKey, s.now()) {
		// A handshake is not a challenge: whoever has seen this node's handshake
		// can send it again, so the copy is refused but nothing is held against
		// the node that signed it.
		fmt.Printf("Handshake nonce replay from %s rejected\n", derived)
		return fmt.Errorf("handshake nonce replay detected")
	}

	packet.nodeID = derived
	packet.pubKey = recovered
	packet.NodeID = derived
	return nil
}

// validClientVersion reports whether a client version may be kept and logged:
// it is not empty, has at most maxClientVersionLen bytes and holds only
// printable characters.
func validClientVersion(v string) bool {
	if v == "" || len(v) > maxClientVersionLen {
		return false
	}
	for _, r := range v {
		// Bytes that are not valid UTF-8 decode to utf8.RuneError.
		if r == utf8.RuneError || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// signatureMismatch builds the error for a handshake whose signature does not
// verify. Nothing is held against the node ID the packet names: the packet does
// not come from that node, and a ban on the ID would lock out whoever really
// holds it.
func signatureMismatch(format string, args ...any) error {
	params := make([]any, 0, len(args)+1)
	params = append(params, errHandshakeSignatureFailure)
	params = append(params, args...)
	return fmt.Errorf("%w: "+format, params...)
}

func sanitizeListenAddrs(addrs []string) []string {
	if len(addrs) == 0 {
		return nil
	}
	// The list comes from the remote peer: only look at the first few entries and
	// keep at most maxHandshakeListenAddrs of them.
	if len(addrs) > maxHandshakeListenAddrScan {
		addrs = addrs[:maxHandshakeListenAddrScan]
	}
	cleaned := make([]string, 0, len(addrs))
	seen := make(map[string]struct{}, len(addrs))
	for _, raw := range addrs {
		if len(cleaned) >= maxHandshakeListenAddrs {
			break
		}
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		host, port, err := net.SplitHostPort(trimmed)
		if err != nil {
			continue
		}
		host = strings.TrimSpace(host)
		port = strings.TrimSpace(port)
		if host == "" || port == "" || port == "0" {
			continue
		}
		if ip := net.ParseIP(host); ip != nil {
			if ip.IsUnspecified() {
				continue
			}
			host = ip.String()
		}
		normalized := net.JoinHostPort(host, port)
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		cleaned = append(cleaned, normalized)
	}
	if len(cleaned) == 0 {
		return nil
	}
	return cleaned
}

// sourceBoundAddrs returns the advertised addresses that are at the host the
// connection came from, in their original order. The addresses in a handshake
// are the sender's own claim and the signature does not cover them, so only an
// address at the connection's own IP is one this node can stand behind. A host
// name never qualifies: it says nothing about where the connection came from.
// The port is not compared, since a listening port is not the port a connection
// is opened from.
func sourceBoundAddrs(addrs []string, remote net.Addr) []string {
	if len(addrs) == 0 || remote == nil {
		return nil
	}
	source := net.ParseIP(remoteHost(remote.String()))
	if source == nil {
		return nil
	}
	bound := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
		if err != nil {
			continue
		}
		if ip := net.ParseIP(host); ip != nil && ip.Equal(source) {
			bound = append(bound, addr)
		}
	}
	return bound
}

// handshakeSignedByClaimedNode reports whether the packet's signature really
// comes from the node ID the packet claims. It has no side effects.
func handshakeSignedByClaimedNode(packet *handshakePacket) bool {
	if packet == nil {
		return false
	}
	canonicalNonce, ok := canonicalizeNonce(packet.Nonce)
	if !ok {
		return false
	}
	nonceBytes, err := hex.DecodeString(canonicalNonce)
	if err != nil {
		return false
	}
	genesis, err := decodeHex(packet.GenesisHash)
	if err != nil {
		return false
	}
	sigBytes, err := decodeHex(packet.Signature)
	if err != nil || len(sigBytes) != 65 {
		return false
	}
	digest, err := handshakeDigest(packet.ChainID, genesis, nonceBytes, packet.NodeID)
	if err != nil {
		return false
	}
	recovered, err := ethcrypto.SigToPub(digest, sigBytes)
	if err != nil {
		return false
	}
	derived := normalizeHex(deriveNodeIDFromPub(recovered))
	claimed := normalizeHex(packet.NodeID)
	return derived != "" && claimed != "" && strings.EqualFold(derived, claimed)
}

// markHandshakeViolation bans a node that failed the handshake, when the node
// itself signed the evidence (authenticated). The node ID in a packet is a
// string the sender chose, so a packet that is not signed by that node is never
// held against it: a ban would lock out whoever really holds the ID, in memory
// as much as in the peerstore. A configured persistent peer is not banned on
// handshake evidence either, so that it can reconnect as soon as its fault is
// fixed.
func (s *Server) markHandshakeViolation(nodeID string, authenticated bool) {
	if s == nil || !authenticated {
		return
	}
	normalized := normalizeHex(nodeID)
	if normalized == "" || s.isConfiguredPersistentPeer(normalized) {
		return
	}
	now := s.now()
	duration := s.cfg.PeerBanDuration
	if duration <= 0 {
		duration = defaultPeerBan
	}
	until := now.Add(duration)

	if s.reputation != nil {
		s.reputation.SetBan(normalized, until, now)
	}
	if s.peerstore != nil {
		if _, err := s.peerstore.RecordViolation(normalized, now); err != nil {
			fmt.Printf("record handshake violation %s: %v\n", normalized, err)
		}
		if err := s.peerstore.SetBan(normalized, until); err != nil {
			fmt.Printf("record handshake ban %s: %v\n", normalized, err)
		}
	}
}

func handshakeDigest(chainID uint64, genesis []byte, nonce []byte, nodeID string) ([]byte, error) {
	canonicalNodeID := normalizeHex(nodeID)
	if canonicalNodeID == "" {
		return nil, fmt.Errorf("empty node ID in handshake")
	}
	nodeBytes, err := decodeHex(canonicalNodeID)
	if err != nil {
		return nil, fmt.Errorf("decode node ID: %w", err)
	}
	if len(nodeBytes) == 0 {
		return nil, fmt.Errorf("empty node ID in handshake")
	}
	if len(nonce) != handshakeNonceSize {
		return nil, fmt.Errorf("invalid handshake nonce length: %d", len(nonce))
	}
	var chainBuf [8]byte
	binary.BigEndian.PutUint64(chainBuf[:], chainID)
	domain := []byte(handshakeDomain)
	data := make([]byte, 0, len(domain)+len(chainBuf)+len(genesis)+len(nonce)+len(canonicalNodeID))
	data = append(data, domain...)
	data = append(data, chainBuf[:]...)
	data = append(data, genesis...)
	data = append(data, nonce...)
	data = append(data, []byte(canonicalNodeID)...)
	return ethcrypto.Keccak256(data), nil
}

func writeFrame(ctx context.Context, conn net.Conn, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetWriteDeadline(deadline); err != nil {
			return err
		}
		defer conn.SetWriteDeadline(time.Time{})
	}
	_, err = conn.Write(append(data, '\n'))
	return err
}

func readFrame(ctx context.Context, conn net.Conn, reader *bufio.Reader, limit int) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		defer conn.SetReadDeadline(time.Time{})
	}
	if limit <= 0 {
		limit = defaultMaxMessageSize
	}
	initialCap := limit
	if initialCap > 1024 {
		initialCap = 1024
	}
	buf := make([]byte, 0, initialCap)
	for {
		b, err := reader.ReadByte()
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return nil, ctxErr
				}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}
			return nil, err
		}
		if b == '\n' {
			return bytesTrimSpace(buf), nil
		}
		if len(buf)+1 > limit {
			return nil, errHandshakeFrameTooLarge
		}
		buf = append(buf, b)
	}
}

func encodeHex(data []byte) string {
	if len(data) == 0 {
		return "0x"
	}
	return "0x" + hex.EncodeToString(data)
}

func decodeHex(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X") {
		value = value[2:]
	}
	if value == "" {
		return []byte{}, nil
	}
	if len(value)%2 == 1 {
		value = "0" + value
	}
	return hex.DecodeString(value)
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func normalizeHex(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "0x") && !strings.HasPrefix(value, "0X") {
		value = "0x" + value
	}
	return strings.ToLower(value)
}
