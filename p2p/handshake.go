package p2p

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"time"

	"nhbchain/crypto"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

type handshakeMessage struct {
	ChainID       uint64 `json:"chainId"`
	GenesisHash   []byte `json:"genesisHash"`
	NodeID        string `json:"nodeId"`
	ClientVersion string `json:"clientVersion"`
	PubKey        []byte `json:"pubKey"`
	Nonce         []byte `json:"nonce"`
	Signature     []byte `json:"signature"`
}

func (s *Server) performHandshake(ctx context.Context, conn net.Conn, reader *bufio.Reader) (*handshakeMessage, error) {
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

	var remote handshakeMessage
	if err := json.Unmarshal(payload, &remote); err != nil {
		return nil, fmt.Errorf("decode handshake: %w", err)
	}

	nodeID, verifyErr := s.verifyHandshake(&remote)
	if verifyErr != nil {
		return nil, verifyErr
	}
	remote.NodeID = nodeID
	return &remote, nil
}

func (s *Server) buildHandshake() (*handshakeMessage, error) {
	nonce := make([]byte, handshakeNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate handshake nonce: %w", err)
	}
	pubKey := s.privKey.PubKey().PublicKey
	digest := handshakeDigest(s.cfg.ChainID, s.genesis, s.cfg.ClientVersion, nonce)
	sig, err := ethcrypto.Sign(digest, s.privKey.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("sign handshake: %w", err)
	}

	return &handshakeMessage{
		ChainID:       s.cfg.ChainID,
		GenesisHash:   cloneBytes(s.genesis),
		NodeID:        s.nodeID,
		ClientVersion: s.cfg.ClientVersion,
		PubKey:        ethcrypto.FromECDSAPub(pubKey),
		Nonce:         nonce,
		Signature:     sig,
	}, nil
}

func handshakeDigest(chainID uint64, genesisHash []byte, clientVersion string, nonce []byte) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, chainID)
	h := sha256.New()
	h.Write(buf)
	h.Write(genesisHash)
	h.Write([]byte(clientVersion))
	h.Write(nonce)
	return h.Sum(nil)
}

func (s *Server) verifyHandshake(msg *handshakeMessage) (string, error) {
	if len(msg.Nonce) != handshakeNonceSize {
		return "", fmt.Errorf("invalid handshake nonce length: %d", len(msg.Nonce))
	}
	if len(msg.Signature) != 65 {
		return "", fmt.Errorf("invalid handshake signature length: %d", len(msg.Signature))
	}
	if len(msg.PubKey) == 0 {
		return "", fmt.Errorf("handshake missing public key")
	}
	if len(msg.GenesisHash) == 0 {
		return "", fmt.Errorf("handshake missing genesis hash")
	}
	if msg.ClientVersion == "" {
		return "", fmt.Errorf("handshake missing client version")
	}

	pubKey, err := ethcrypto.UnmarshalPubkey(msg.PubKey)
	if err != nil {
		return "", fmt.Errorf("invalid public key: %w", err)
	}
	nodeID := crypto.NewAddress(crypto.NHBPrefix, ethcrypto.PubkeyToAddress(*pubKey).Bytes()).String()

	digest := handshakeDigest(msg.ChainID, msg.GenesisHash, msg.ClientVersion, msg.Nonce)
	if !ethcrypto.VerifySignature(msg.PubKey, digest, msg.Signature[:64]) {
		return nodeID, fmt.Errorf("invalid handshake signature")
	}
	if msg.NodeID != "" && msg.NodeID != nodeID {
		return nodeID, fmt.Errorf("node ID mismatch: claimed %s expected %s", msg.NodeID, nodeID)
	}
	if msg.ChainID != s.cfg.ChainID {
		return nodeID, fmt.Errorf("chain ID mismatch: remote %d local %d", msg.ChainID, s.cfg.ChainID)
	}
	if !bytes.Equal(msg.GenesisHash, s.genesis) {
		return nodeID, fmt.Errorf("genesis hash mismatch: remote %x local %x", msg.GenesisHash, s.genesis)
	}
	return nodeID, nil
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
