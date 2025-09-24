package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// MintChainID defines the chain identifier expected inside mint vouchers.
const MintChainID uint64 = 187001

var (
	// ErrMintInvalidSigner indicates the recovered signer does not hold the required role.
	ErrMintInvalidSigner = errors.New("mint: invalid signer")
	// ErrMintInvoiceUsed indicates the provided invoice identifier has already been processed.
	ErrMintInvoiceUsed = errors.New("mint: invoice already settled")
	// ErrMintExpired indicates the voucher expiry timestamp has elapsed.
	ErrMintExpired = errors.New("mint: voucher expired")
	// ErrMintInvalidChainID indicates the voucher targets a different chain identifier.
	ErrMintInvalidChainID = errors.New("mint: invalid chain id")
)

// MintVoucher represents the canonical payload that is signed off-chain by a mint authority.
type MintVoucher struct {
	InvoiceID string `json:"invoiceId"`
	Recipient string `json:"recipient"`
	Token     string `json:"token"`
	Amount    string `json:"amount"`
	ChainID   uint64 `json:"chainId"`
	Expiry    int64  `json:"expiry"`
}

// CanonicalJSON returns the canonical JSON encoding used for signing vouchers.
func (v MintVoucher) CanonicalJSON() ([]byte, error) {
	normalizedAmount, err := v.AmountBig()
	if err != nil {
		return nil, err
	}
	canonical := struct {
		InvoiceID string `json:"invoiceId"`
		Recipient string `json:"recipient"`
		Token     string `json:"token"`
		Amount    string `json:"amount"`
		ChainID   uint64 `json:"chainId"`
		Expiry    int64  `json:"expiry"`
	}{
		InvoiceID: strings.TrimSpace(v.InvoiceID),
		Recipient: strings.TrimSpace(v.Recipient),
		Token:     strings.ToUpper(strings.TrimSpace(v.Token)),
		Amount:    normalizedAmount.String(),
		ChainID:   v.ChainID,
		Expiry:    v.Expiry,
	}
	if canonical.InvoiceID == "" {
		return nil, fmt.Errorf("invoiceId required")
	}
	if canonical.Recipient == "" {
		return nil, fmt.Errorf("recipient required")
	}
	if canonical.Token == "" {
		return nil, fmt.Errorf("token required")
	}
	if canonical.ChainID == 0 {
		return nil, fmt.Errorf("chainId required")
	}
	if canonical.Expiry == 0 {
		return nil, fmt.Errorf("expiry required")
	}
	return json.Marshal(canonical)
}

// Digest computes the keccak256 hash over the canonical JSON representation.
func (v MintVoucher) Digest() ([]byte, error) {
	canonical, err := v.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	return ethcrypto.Keccak256(canonical), nil
}

// AmountBig parses the Amount field and returns it as a big integer.
func (v MintVoucher) AmountBig() (*big.Int, error) {
	trimmed := strings.TrimSpace(v.Amount)
	if trimmed == "" {
		return nil, fmt.Errorf("amount required")
	}
	value, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount: %s", v.Amount)
	}
	if value.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	return value, nil
}

// NormalizedToken returns the uppercase token symbol included in the voucher.
func (v MintVoucher) NormalizedToken() string {
	return strings.ToUpper(strings.TrimSpace(v.Token))
}

// TrimmedInvoiceID returns the trimmed invoice identifier.
func (v MintVoucher) TrimmedInvoiceID() string {
	return strings.TrimSpace(v.InvoiceID)
}

// TrimmedRecipient returns the trimmed recipient reference.
func (v MintVoucher) TrimmedRecipient() string {
	return strings.TrimSpace(v.Recipient)
}
