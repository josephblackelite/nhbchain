package p2p

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

const (
	protocolVersion       uint32 = 1
	handshakeNonceSize           = 32
	handshakeReplayWindow        = 10 * time.Minute
)

type handshakeMessage struct {
	ProtocolVersion uint32 `json:"protoVersion"`
	ChainID         uint64 `json:"chainId"`
	GenesisHash     string `json:"genesisHash"`
	NodeID          string `json:"nodeId"`
	Nonce           string `json:"nonce"`
	ClientVersion   string `json:"clientVersion"`
}

type handshakePacket struct {
	handshakeMessage
	Signature string `json:"sig"`

	nodeID string
	pubKey *ecdsa.PublicKey
}

func (s *Server) performHandshake(ctx context.Context, conn net.Conn, reader *bufio.Reader) (*handshakePacket, error) {
	local, err := s.buildHandshake()
	if err != nil {
		return nil, fmt.Errorf("prepare handshake: %w", err)
	}
	if err := writeFrame(ctx, conn, local); err != nil {
		return nil, fmt.Errorf("send handshake: %w", err)
	}

	payload, err := readFrame(ctx, conn, reader)
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
	return &remote, nil
}

func (s *Server) buildHandshake() (*handshakePacket, error) {
	nonce := make([]byte, handshakeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate handshake nonce: %w", err)
	}

	payload := handshakeMessage{
		ProtocolVersion: protocolVersion,
		ChainID:         s.cfg.ChainID,
		GenesisHash:     encodeHex(s.genesis),
		NodeID:          s.nodeID,
		Nonce:           encodeHex(nonce),
		ClientVersion:   s.cfg.ClientVersion,
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
		nodeID:           s.nodeID,
		pubKey:           &s.privKey.PrivateKey.PublicKey,
	}
	if !s.nonceGuard.Remember(payload.Nonce, s.now()) {
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
	nonceBytes, err := decodeHex(packet.Nonce)
	if err != nil {
		return fmt.Errorf("invalid nonce encoding: %w", err)
	}
	if len(nonceBytes) != handshakeNonceSize {
		return fmt.Errorf("invalid handshake nonce length: %d", len(nonceBytes))
	}
	if packet.ChainID != s.cfg.ChainID {
		return fmt.Errorf("chain ID mismatch: remote %d local %d", packet.ChainID, s.cfg.ChainID)
	}
	remoteGenesis, err := decodeHex(packet.GenesisHash)
	if err != nil {
		return fmt.Errorf("invalid genesis hash encoding: %w", err)
	}
	if len(remoteGenesis) == 0 {
		return fmt.Errorf("handshake missing genesis hash")
	}
	if !bytesEqual(remoteGenesis, s.genesis) {
		return fmt.Errorf("genesis hash mismatch: remote %x local %x", remoteGenesis, s.genesis)
	}
	sigBytes, err := decodeHex(packet.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}
	if len(sigBytes) != 65 {
		return fmt.Errorf("invalid handshake signature length: %d", len(sigBytes))
	}

	digest, err := handshakeDigest(packet.ChainID, remoteGenesis, nonceBytes, packet.NodeID)
	if err != nil {
		return err
	}
	recovered, err := ethcrypto.SigToPub(digest, sigBytes)
	if err != nil {
		return fmt.Errorf("recover signature: %w", err)
	}
	derived := normalizeHex(deriveNodeIDFromPub(recovered))
	claimed := normalizeHex(packet.NodeID)
	if derived == "" || claimed == "" {
		return fmt.Errorf("unable to derive node identity")
	}
	if !strings.EqualFold(derived, claimed) {
		return fmt.Errorf("node ID mismatch: derived %s claimed %s", derived, claimed)
	}

	if !s.nonceGuard.Remember(packet.Nonce, s.now()) {
		return fmt.Errorf("handshake nonce replay detected")
	}

	packet.nodeID = derived
	packet.pubKey = recovered
	packet.NodeID = derived
	return nil
}

func handshakeDigest(chainID uint64, genesis []byte, nonce []byte, nodeID string) ([]byte, error) {
	nodeBytes, err := decodeHex(nodeID)
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
	data := make([]byte, 0, len(chainBuf)+len(genesis)+len(nonce)+len(nodeBytes))
	data = append(data, chainBuf[:]...)
	data = append(data, genesis...)
	data = append(data, nonce...)
	data = append(data, nodeBytes...)
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

func readFrame(ctx context.Context, conn net.Conn, reader *bufio.Reader) ([]byte, error) {
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return nil, err
		}
		defer conn.SetReadDeadline(time.Time{})
	}
	line, err := reader.ReadBytes('\n')
	if err != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		return nil, err
	}
	return bytesTrimSpace(line), nil
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
