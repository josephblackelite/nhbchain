package core

import (
	"encoding/hex"
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
	// ErrMintInvalidPayload indicates the mint transaction payload could not be decoded.
	ErrMintInvalidPayload = errors.New("mint: invalid payload")
	// ErrMintEmissionCapExceeded indicates the configured emission cap would be exceeded.
	ErrMintEmissionCapExceeded = errors.New("mint: emission cap exceeded")
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

type mintTransactionPayload struct {
	Voucher   MintVoucher `json:"voucher"`
	Signature string      `json:"signature"`
}

func encodeMintTransaction(voucher *MintVoucher, signature []byte) ([]byte, error) {
	if voucher == nil {
		return nil, fmt.Errorf("voucher required")
	}
	if len(signature) == 0 {
		return nil, fmt.Errorf("signature required")
	}
	payload := mintTransactionPayload{
		Voucher:   *voucher,
		Signature: "0x" + strings.ToLower(hex.EncodeToString(signature)),
	}
	return json.Marshal(payload)
}

func decodeMintTransaction(data []byte) (*MintVoucher, []byte, error) {
	if len(data) == 0 {
		return nil, nil, fmt.Errorf("%w: payload required", ErrMintInvalidPayload)
	}
	var payload mintTransactionPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrMintInvalidPayload, err)
	}
	voucher := payload.Voucher
	sig := strings.TrimSpace(payload.Signature)
	if sig == "" {
		return nil, nil, fmt.Errorf("%w: signature required", ErrMintInvalidPayload)
	}
	sig = strings.TrimPrefix(strings.ToLower(sig), "0x")
	signature, err := hex.DecodeString(sig)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrMintInvalidPayload, err)
	}
	if len(signature) == 0 {
		return nil, nil, fmt.Errorf("%w: signature required", ErrMintInvalidPayload)
	}
	return &voucher, append([]byte(nil), signature...), nil
}

// MintVoucherHash returns the keccak256 hash of the canonical voucher payload
// concatenated with the provided signature. This digest was historically used
// for reconciliation and remains available for downstream tooling.
func MintVoucherHash(voucher *MintVoucher, signature []byte) (string, error) {
	if voucher == nil {
		return "", fmt.Errorf("voucher required")
	}
	canonical, err := voucher.CanonicalJSON()
	if err != nil {
		return "", err
	}
	digest := ethcrypto.Keccak256(append([]byte{}, append(canonical, signature...)...))
	return "0x" + strings.ToLower(hex.EncodeToString(digest)), nil
}
