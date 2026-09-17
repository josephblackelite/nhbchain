package p2p

import (
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"nhbchain/crypto"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// Identity encapsulates the persistent node identity material used by the P2P layer.
type Identity struct {
	PrivateKey *crypto.PrivateKey
	NodeID     string
}

type identityDisk struct {
	PrivateKey string `json:"privateKey"`
}

// LoadOrCreateIdentity reads a secp256k1 private key from disk, generating one if absent.
// The resulting Identity contains the derived NodeID (keccak256 of the uncompressed
// public key) encoded as a 0x-prefixed hex string.
func LoadOrCreateIdentity(path string) (*Identity, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("identity path must be provided")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create identity directory: %w", err)
	}

	if data, err := os.ReadFile(path); err == nil {
		return decodeIdentity(data)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read identity file: %w", err)
	}

	privKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		return nil, fmt.Errorf("generate identity key: %w", err)
	}
	encoded := identityDisk{PrivateKey: hex.EncodeToString(privKey.Bytes())}
	payload, err := json.MarshalIndent(&encoded, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode identity: %w", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		return nil, fmt.Errorf("persist identity: %w", err)
	}
	return &Identity{PrivateKey: privKey, NodeID: deriveNodeID(privKey)}, nil
}

func decodeIdentity(data []byte) (*Identity, error) {
	data = bytesTrimSpace(data)
	if len(data) == 0 {
		return nil, fmt.Errorf("identity file empty")
	}
	// Accept both raw hex and JSON for forwards compatibility.
	if data[0] != '{' {
		keyBytes, err := hex.DecodeString(strings.TrimSpace(string(data)))
		if err != nil {
			return nil, fmt.Errorf("decode legacy identity: %w", err)
		}
		privKey, err := crypto.PrivateKeyFromBytes(keyBytes)
		if err != nil {
			return nil, fmt.Errorf("parse legacy identity key: %w", err)
		}
		return &Identity{PrivateKey: privKey, NodeID: deriveNodeID(privKey)}, nil
	}

	var stored identityDisk
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("decode identity JSON: %w", err)
	}
	raw, err := hex.DecodeString(strings.TrimSpace(stored.PrivateKey))
	if err != nil {
		return nil, fmt.Errorf("decode identity key material: %w", err)
	}
	privKey, err := crypto.PrivateKeyFromBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("parse identity key: %w", err)
	}
	return &Identity{PrivateKey: privKey, NodeID: deriveNodeID(privKey)}, nil
}

func deriveNodeID(priv *crypto.PrivateKey) string {
	if priv == nil {
		return ""
	}
	return deriveNodeIDFromPub(priv.PubKey().PublicKey)
}

func deriveNodeIDFromPub(pub *ecdsa.PublicKey) string {
	if pub == nil {
		return ""
	}
	pubBytes := ethcrypto.FromECDSAPub(pub)
	if len(pubBytes) == 0 {
		return ""
	}
	hash := ethcrypto.Keccak256(pubBytes[1:])
	return "0x" + hex.EncodeToString(hash)
}

func bytesTrimSpace(b []byte) []byte {
	i := 0
	for i < len(b) && (b[i] == ' ' || b[i] == '\n' || b[i] == '\t' || b[i] == '\r') {
		i++
	}
	j := len(b)
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\t' || b[j-1] == '\r') {
		j--
	}
	return b[i:j]
}
