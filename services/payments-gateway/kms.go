package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	nhbcrypto "nhbchain/crypto"
)

// Signer defines the interface expected for signing mint vouchers.
type Signer interface {
	Address() string
	Sign(ctx context.Context, payload []byte) ([]byte, error)
}

// EnvKMSSigner implements the Signer interface by sourcing key material from an environment variable.
type EnvKMSSigner struct {
	key *nhbcrypto.PrivateKey
}

// NewEnvKMSSigner creates a signer using the provided environment variable. The material must be a hex-encoded secp256k1 key.
func NewEnvKMSSigner(varName string) (*EnvKMSSigner, error) {
	material := strings.TrimSpace(os.Getenv(varName))
	if material == "" {
		return nil, fmt.Errorf("environment variable %s not set", varName)
	}
	material = strings.TrimPrefix(material, "0x")
	decoded, err := hex.DecodeString(material)
	if err != nil {
		return nil, fmt.Errorf("failed to decode private key material: %w", err)
	}
	key, err := nhbcrypto.PrivateKeyFromBytes(decoded)
	if err != nil {
		return nil, fmt.Errorf("invalid private key material: %w", err)
	}
	return &EnvKMSSigner{key: key}, nil
}

// Address returns the NHB-formatted address of the signer.
func (s *EnvKMSSigner) Address() string {
	if s == nil || s.key == nil {
		return ""
	}
	return s.key.PubKey().Address().String()
}

// LoadPrivateKeyFromEnv loads a raw hex-encoded secp256k1 private key from
// the environment variable named by varName, decoding it the same way
// NewEnvKMSSigner does. Unlike EnvKMSSigner (which wraps the key behind a
// Keccak256-then-secp256k1-sign Sign() method purpose-built for mint
// vouchers), callers here need the raw *nhbcrypto.PrivateKey itself -- the
// redemption attestor signs standard V3 NHB transactions via
// types.Transaction.Sign(privKey.PrivateKey), a different signing scheme
// entirely. Used for PAY_GATEWAY_ATTESTOR_KMS_ENV; kept in this file since
// it duplicates NewEnvKMSSigner's decode logic verbatim rather than
// factoring it out, so neither caller's error messages or behavior change.
func LoadPrivateKeyFromEnv(varName string) (*nhbcrypto.PrivateKey, error) {
	material := strings.TrimSpace(os.Getenv(varName))
	if material == "" {
		return nil, fmt.Errorf("environment variable %s not set", varName)
	}
	material = strings.TrimPrefix(material, "0x")
	decoded, err := hex.DecodeString(material)
	if err != nil {
		return nil, fmt.Errorf("failed to decode private key material: %w", err)
	}
	key, err := nhbcrypto.PrivateKeyFromBytes(decoded)
	if err != nil {
		return nil, fmt.Errorf("invalid private key material: %w", err)
	}
	return key, nil
}

// Sign produces a secp256k1 signature over the payload using the configured key.
func (s *EnvKMSSigner) Sign(ctx context.Context, payload []byte) ([]byte, error) {
	if s == nil || s.key == nil {
		return nil, fmt.Errorf("kms signer not configured")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	hash := ethcrypto.Keccak256(payload)
	sig, err := ethcrypto.Sign(hash, s.key.PrivateKey)
	if err != nil {
		return nil, err
	}
	return sig, nil
}
