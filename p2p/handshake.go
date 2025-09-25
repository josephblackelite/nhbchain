package p2p

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"nhbchain/crypto"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

const (
	protocolVersion        uint32        = 1
	handshakeSkewAllowance time.Duration = 5 * time.Minute
)

type handshakeMessage struct {
	ProtocolVersion uint32 `json:"protoVersion"`
	ChainID         uint64 `json:"chainId"`
	GenesisHash     string `json:"genesisHash"`
	NodePubHex      string `json:"nodeIdPub"`
	NodeAddr        string `json:"nodeAddrBech32"`
	Nonce           string `json:"nonce"`
	Timestamp       int64  `json:"ts"`
	ClientVersion   string `json:"clientVersion"`
}

type handshakePacket struct {
	handshakeMessage
	SigAddr   string `json:"sigAddr"`
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

	now := s.now()
	pubKey := s.privKey.PubKey().PublicKey
	pubBytes := ethcrypto.FromECDSAPub(pubKey)
	payload := handshakeMessage{
		ProtocolVersion: protocolVersion,
		ChainID:         s.cfg.ChainID,
		GenesisHash:     encodeHex(s.genesis),
		NodePubHex:      encodeHex(pubBytes),
		NodeAddr:        s.walletAddr,
		Nonce:           encodeHex(nonce),
		Timestamp:       now.Unix(),
		ClientVersion:   s.cfg.ClientVersion,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal handshake payload: %w", err)
	}
	digest := handshakeDigest(body, payload.Timestamp)
	sig, err := ethcrypto.Sign(digest, s.privKey.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("sign handshake: %w", err)
	}

	packet := &handshakePacket{
		handshakeMessage: payload,
		SigAddr:          s.walletAddr,
		Signature:        encodeHex(sig),
	}
	packet.nodeID = s.nodeID
	packet.pubKey = pubKey
	if !s.nonceGuard.Remember(packet.Nonce, now) {
		// Should never happen for a locally generated nonce, but keep defensive.
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
	if strings.TrimSpace(packet.NodeAddr) == "" {
		return fmt.Errorf("handshake missing node address")
	}
	if strings.TrimSpace(packet.SigAddr) == "" {
		return fmt.Errorf("handshake missing signature address")
	}
	if !strings.EqualFold(packet.SigAddr, packet.NodeAddr) {
		return fmt.Errorf("signature address mismatch")
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
	if !bytes.Equal(remoteGenesis, s.genesis) {
		return fmt.Errorf("genesis hash mismatch: remote %x local %x", remoteGenesis, s.genesis)
	}

	ts := time.Unix(packet.Timestamp, 0)
	now := s.now()
	if now.Sub(ts) > handshakeSkewAllowance || ts.Sub(now) > handshakeSkewAllowance {
		return fmt.Errorf("handshake timestamp skew too large")
	}

	payloadJSON, err := json.Marshal(packet.handshakeMessage)
	if err != nil {
		return fmt.Errorf("marshal handshake for verification: %w", err)
	}
	sigBytes, err := decodeHex(packet.Signature)
	if err != nil {
		return fmt.Errorf("invalid signature encoding: %w", err)
	}
	if len(sigBytes) != 65 {
		return fmt.Errorf("invalid handshake signature length: %d", len(sigBytes))
	}

	pub, err := parseHandshakePub(packet.NodePubHex)
	if err != nil {
		return fmt.Errorf("invalid node public key: %w", err)
	}

	addr, err := crypto.DecodeAddress(packet.NodeAddr)
	if err != nil {
		return fmt.Errorf("decode node address: %w", err)
	}
	derivedAddr := ethcrypto.PubkeyToAddress(*pub)
	if !bytes.Equal(addr.Bytes(), derivedAddr.Bytes()) {
		return fmt.Errorf("node address mismatch")
	}

	digest := handshakeDigest(payloadJSON, packet.Timestamp)
	recovered, err := ethcrypto.SigToPub(digest, sigBytes)
	if err != nil {
		return fmt.Errorf("recover signature: %w", err)
	}
	recoveredAddr := ethcrypto.PubkeyToAddress(*recovered)
	if !bytes.Equal(recoveredAddr.Bytes(), addr.Bytes()) {
		return fmt.Errorf("signature does not match address")
	}

	if !s.nonceGuard.Remember(packet.Nonce, now) {
		return fmt.Errorf("handshake nonce replay detected")
	}

	packet.nodeID = addr.String()
	packet.pubKey = pub
	return nil
}

func parseHandshakePub(value string) (*ecdsa.PublicKey, error) {
	if strings.TrimSpace(value) == "" {
		return nil, fmt.Errorf("missing public key")
	}
	bytes, err := decodeHex(value)
	if err != nil {
		return nil, err
	}
	return ethcrypto.UnmarshalPubkey(bytes)
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

func handshakeDigest(payload []byte, timestamp int64) []byte {
	digestInput := fmt.Sprintf("nhb-p2p|hello|%s|%d", payload, timestamp)
	return ethcrypto.Keccak256([]byte(digestInput))
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
	return bytes.TrimSpace(line), nil
}
