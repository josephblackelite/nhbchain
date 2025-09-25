package swap

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	repoCrypto "nhbchain/crypto"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// VoucherDomainV1 defines the voucher domain string for the first swap voucher version.
const VoucherDomainV1 = "NHB_SWAP_VOUCHER_V1"

// VoucherV1 captures the structured payload authorised by the fiat gateway.
type VoucherV1 struct {
	Domain     string
	ChainID    uint64
	Token      string
	Recipient  [20]byte
	Amount     *big.Int
	Fiat       string
	FiatAmount string
	Rate       string
	OrderID    string
	Nonce      []byte
	Expiry     int64
}

// VoucherSubmission bundles the payload supplied by the fiat gateway when
// requesting a mint alongside auxiliary metadata captured for auditing.
type VoucherSubmission struct {
	Voucher      *VoucherV1
	Signature    []byte
	Provider     string
	ProviderTxID string
	Username     string
	Address      string
	USDAmount    string
}

type voucherJSON struct {
	Domain     string `json:"domain"`
	ChainID    uint64 `json:"chainId"`
	Token      string `json:"token"`
	Recipient  string `json:"recipient"`
	Amount     string `json:"amount"`
	Fiat       string `json:"fiat"`
	FiatAmount string `json:"fiatAmount"`
	Rate       string `json:"rate"`
	OrderID    string `json:"orderId"`
	Nonce      string `json:"nonce"`
	Expiry     int64  `json:"expiry"`
}

// MarshalJSON encodes the voucher into the JSON representation consumed by RPC clients.
func (v VoucherV1) MarshalJSON() ([]byte, error) {
	amountStr := "0"
	if v.Amount != nil {
		amountStr = strings.TrimSpace(v.Amount.String())
	}
	nonceHex := hex.EncodeToString(v.Nonce)
	recipient := ""
	if v.Recipient != ([20]byte{}) {
		recipient = repoCrypto.NewAddress(repoCrypto.NHBPrefix, v.Recipient[:]).String()
	}
	payload := voucherJSON{
		Domain:     strings.TrimSpace(v.Domain),
		ChainID:    v.ChainID,
		Token:      strings.TrimSpace(v.Token),
		Recipient:  recipient,
		Amount:     amountStr,
		Fiat:       strings.TrimSpace(v.Fiat),
		FiatAmount: strings.TrimSpace(v.FiatAmount),
		Rate:       strings.TrimSpace(v.Rate),
		OrderID:    strings.TrimSpace(v.OrderID),
		Nonce:      strings.ToLower(nonceHex),
		Expiry:     v.Expiry,
	}
	return json.Marshal(payload)
}

// UnmarshalJSON decodes the on-wire representation into the canonical struct.
func (v *VoucherV1) UnmarshalJSON(data []byte) error {
	if v == nil {
		return fmt.Errorf("voucher: nil receiver")
	}
	var payload voucherJSON
	if err := json.Unmarshal(data, &payload); err != nil {
		return err
	}
	domain := strings.TrimSpace(payload.Domain)
	if domain == "" {
		return fmt.Errorf("voucher: domain required")
	}
	token := strings.ToUpper(strings.TrimSpace(payload.Token))
	if token == "" {
		return fmt.Errorf("voucher: token required")
	}
	recipientStr := strings.TrimSpace(payload.Recipient)
	if recipientStr == "" {
		return fmt.Errorf("voucher: recipient required")
	}
	recipientAddr, err := repoCrypto.DecodeAddress(recipientStr)
	if err != nil {
		return fmt.Errorf("voucher: recipient: %w", err)
	}
	var recipient [20]byte
	copy(recipient[:], recipientAddr.Bytes())
	amountStr := strings.TrimSpace(payload.Amount)
	if amountStr == "" {
		return fmt.Errorf("voucher: amount required")
	}
	amount, ok := new(big.Int).SetString(amountStr, 10)
	if !ok {
		return fmt.Errorf("voucher: invalid amount %q", payload.Amount)
	}
	if amount.Sign() <= 0 {
		return fmt.Errorf("voucher: amount must be positive")
	}
	orderID := strings.TrimSpace(payload.OrderID)
	if orderID == "" {
		return fmt.Errorf("voucher: orderId required")
	}
	nonceStr := strings.TrimSpace(payload.Nonce)
	if nonceStr == "" {
		return fmt.Errorf("voucher: nonce required")
	}
	normalizedNonce := strings.TrimPrefix(strings.ToLower(nonceStr), "0x")
	nonce, err := hex.DecodeString(normalizedNonce)
	if err != nil {
		return fmt.Errorf("voucher: nonce: %w", err)
	}
	*v = VoucherV1{
		Domain:     domain,
		ChainID:    payload.ChainID,
		Token:      token,
		Recipient:  recipient,
		Amount:     amount,
		Fiat:       strings.TrimSpace(payload.Fiat),
		FiatAmount: strings.TrimSpace(payload.FiatAmount),
		Rate:       strings.TrimSpace(payload.Rate),
		OrderID:    orderID,
		Nonce:      nonce,
		Expiry:     payload.Expiry,
	}
	return nil
}

// Hash reconstructs the canonical message digest signed by the mint authority.
func (v VoucherV1) Hash() []byte {
	amountStr := "0"
	if v.Amount != nil {
		amountStr = v.Amount.String()
	}
	payload := fmt.Sprintf("%s|chain=%d|token=%s|to=%s|amount=%s|fiat=%s|fiatAmt=%s|rate=%s|order=%s|nonce=%s|exp=%d",
		strings.TrimSpace(v.Domain),
		v.ChainID,
		strings.TrimSpace(v.Token),
		hex.EncodeToString(v.Recipient[:]),
		amountStr,
		strings.TrimSpace(v.Fiat),
		strings.TrimSpace(v.FiatAmount),
		strings.TrimSpace(v.Rate),
		strings.TrimSpace(v.OrderID),
		strings.ToLower(hex.EncodeToString(v.Nonce)),
		v.Expiry,
	)
	return ethcrypto.Keccak256([]byte(payload))
}
