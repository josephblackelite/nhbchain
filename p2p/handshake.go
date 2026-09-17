package p2p

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

const (
	protocolVersion       uint32 = 1
	handshakeNonceSize           = 12
	handshakeReplayWindow        = 10 * time.Minute
	handshakeDomain              = "nhb-handshake-v1"
)

var (
	errHandshakeChainMismatch    = errors.New("handshake chain mismatch")
	errHandshakeGenesisMismatch  = errors.New("handshake genesis mismatch")
	errHandshakeSignatureFailure = errors.New("handshake signature mismatch")
	errHandshakeFrameTooLarge    = errors.New("handshake frame exceeds maximum size")
)

type handshakeMessage struct {
	ProtocolVersion uint32   `json:"protoVersion"`
	ChainID         uint64   `json:"chainId"`
	GenesisHash     string   `json:"genesisHash"`
	NodeID          string   `json:"nodeId"`
	Nonce           string   `json:"nonce"`
	ClientVersion   string   `json:"clientVersion"`
	ListenAddrs     []string `json:"listenAddrs,omitempty"`
}

type handshakePacket struct {
	handshakeMessage
	Signature string `json:"sig"`

	nodeID string
	pubKey *ecdsa.PublicKey
	addrs  []string
}

func (s *Server) performHandshake(ctx context.Context, conn net.Conn, reader *bufio.Reader) (*handshakePacket, error) {
	local, err := s.buildHandshake()
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
	return &remote, nil
}

func (s *Server) buildHandshake() (*handshakePacket, error) {
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
		s.markHandshakeViolation(packet.NodeID)
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
		s.markHandshakeViolation(packet.NodeID)
		return fmt.Errorf("%w: genesis hash mismatch: remote %x local %x", errHandshakeGenesisMismatch, remoteGenesis, s.genesis)
	}
	sigBytes, err := decodeHex(packet.Signature)
	if err != nil {
		return s.signatureMismatch(packet, "invalid signature encoding: %v", err)
	}
	if len(sigBytes) != 65 {
		return s.signatureMismatch(packet, "invalid handshake signature length: %d", len(sigBytes))
	}

	digest, err := handshakeDigest(packet.ChainID, remoteGenesis, nonceBytes, packet.NodeID)
	if err != nil {
		return s.signatureMismatch(packet, "%v", err)
	}
	recovered, err := ethcrypto.SigToPub(digest, sigBytes)
	if err != nil {
		return s.signatureMismatch(packet, "recover signature: %v", err)
	}
	derived := normalizeHex(deriveNodeIDFromPub(recovered))
	claimed := normalizeHex(packet.NodeID)
	if derived == "" || claimed == "" {
		return s.signatureMismatch(packet, "unable to derive node identity")
	}
	if !strings.EqualFold(derived, claimed) {
		return s.signatureMismatch(packet, "node ID mismatch: derived %s claimed %s", derived, claimed)
	}

	nonceKey := hex.EncodeToString(nonceBytes)
	if !s.nonceGuard.Remember(derived, nonceKey, s.now()) {
		fmt.Printf("Handshake nonce replay from %s rejected\n", derived)
		s.markHandshakeViolation(derived)
		return fmt.Errorf("handshake nonce replay detected")
	}

	packet.nodeID = derived
	packet.pubKey = recovered
	packet.NodeID = derived
	return nil
}

func (s *Server) signatureMismatch(packet *handshakePacket, format string, args ...any) error {
	if s != nil && packet != nil {
		s.markHandshakeViolation(packet.NodeID)
	}
	params := make([]any, 0, len(args)+1)
	params = append(params, errHandshakeSignatureFailure)
	params = append(params, args...)
	return fmt.Errorf("%w: "+format, params...)
}

func sanitizeListenAddrs(addrs []string) []string {
	if len(addrs) == 0 {
		return nil
	}
	cleaned := make([]string, 0, len(addrs))
	seen := make(map[string]struct{}, len(addrs))
	for _, raw := range addrs {
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

func (s *Server) markHandshakeViolation(nodeID string) {
	if s == nil {
		return
	}
	normalized := normalizeHex(nodeID)
	if normalized == "" {
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
