package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	nhbcrypto "nhbchain/crypto"
)

// LoadPrivateKeyFromEnv loads a raw hex-encoded secp256k1 private key from
// the environment variable named by varName. Used for
// ESCROW_GATEWAY_RELAYER_KMS_ENV -- the relayer signs standard V3 NHB
// transactions via types.Transaction.Sign(privKey.PrivateKey), the same
// scheme services/payments-gateway's redemption attestor uses (see that
// service's kms.go, which this mirrors; duplicated rather than shared
// since these are separate main packages).
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
