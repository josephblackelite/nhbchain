package main

import (
	"fmt"
	"net/url"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/crypto"
	escrowpkg "nhbchain/native/escrow"
)

const escrowModuleSeedPrefix = "module/escrow/vault/"

// PayIntent captures the minimal instructions for a client wallet to fund an escrow.
type PayIntent struct {
	Vault  string `json:"vault"`
	Token  string `json:"token"`
	Amount string `json:"amount"`
	Memo   string `json:"memo"`
	QR     string `json:"qr"`
}

// PayIntentBuilder builds pay intents for escrow deposits.
type PayIntentBuilder struct{}

func NewPayIntentBuilder() *PayIntentBuilder {
	return &PayIntentBuilder{}
}

// Build constructs a pay intent for the supplied escrow parameters.
func (b *PayIntentBuilder) Build(token, amount, escrowID string) (PayIntent, error) {
	normalized, err := escrowpkg.NormalizeToken(token)
	if err != nil {
		return PayIntent{}, err
	}
	vaultAddr, err := computeVaultAddress(normalized)
	if err != nil {
		return PayIntent{}, err
	}
	memo := "ESCROW:" + strings.ToUpper(strings.TrimSpace(escrowID))
	qr := buildQRString(vaultAddr, normalized, amount, memo)
	return PayIntent{
		Vault:  vaultAddr,
		Token:  normalized,
		Amount: amount,
		Memo:   memo,
		QR:     qr,
	}, nil
}

func computeVaultAddress(token string) (string, error) {
	seed := escrowModuleSeedPrefix + token
	hash := ethcrypto.Keccak256([]byte(seed))
	var addrBytes [20]byte
	copy(addrBytes[:], hash[len(hash)-20:])
	return crypto.NewAddress(crypto.NHBPrefix, addrBytes[:]).String(), nil
}

func buildQRString(vaultAddr, token, amount, memo string) string {
	values := url.Values{}
	if token != "" {
		values.Set("token", token)
	}
	if amount != "" {
		values.Set("amount", amount)
	}
	if memo != "" {
		values.Set("memo", memo)
	}
	encoded := values.Encode()
	if encoded == "" {
		return fmt.Sprintf("nhb:%s", vaultAddr)
	}
	return fmt.Sprintf("nhb:%s?%s", vaultAddr, encoded)
}
