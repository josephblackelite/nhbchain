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

// VoucherDomainV2 defines the voucher domain string for the second swap
// voucher version (see VoucherV2). NHB-AUDIT-C8 follow-up: a V1 voucher's
// signature (VoucherV1.Hash) never commits to Provider or ProviderTxID, so
// anyone holding any validly-signed V1 voucher can pair it with an
// arbitrary, self-chosen ProviderTxID -- including one that collides with a
// different, unrelated, not-yet-processed order, permanently blocking that
// legitimate order under the ledger's ProviderTxID-keyed storage (see
// core.ErrSwapProviderTxIDCollision). A V2 voucher's signature (VoucherV2.Hash)
// commits to both fields, so its signature is only valid for the exact
// ProviderTxID the mint authority actually signed -- closing that gap. V1
// vouchers keep today's existing, narrower (already-mitigated by the signed
// OrderID nonce check) protection unchanged; this new domain never affects
// how a V1 voucher is verified.
const VoucherDomainV2 = "NHB_SWAP_VOUCHER_V2"

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
//
// Exactly one of Voucher (V1 schema) or VoucherV2 (V2 schema) is populated
// for a given submission, never both. NHB-AUDIT-C8 follow-up: for a V1
// submission, the Provider/ProviderTxID fields below are NOT covered by
// Signature (see VoucherV1.Hash) and are only ever as trustworthy as
// whoever assembled this submission -- the signed OrderID nonce check
// remains the only real anti-double-mint control for V1 (see
// core.ErrSwapNonceUsed / core.ErrSwapProviderTxIDCollision). For a V2
// submission, Provider/ProviderTxID are expected to exactly mirror
// VoucherV2.Provider/VoucherV2.ProviderTxID; the on-chain decode path
// (core.decodeSwapVoucherMintTransaction) always derives these two fields
// FROM the signed VoucherV2 payload rather than trusting a separately
// supplied copy, which is what makes the V2 guarantee hold.
type VoucherSubmission struct {
	Voucher      *VoucherV1
	VoucherV2    *VoucherV2
	Signature    []byte
	Provider     string
	ProviderTxID string
	Username     string
	Address      string
	USDAmount    string
	PriceProof   *PriceProof
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
		recipient = repoCrypto.MustNewAddress(repoCrypto.NHBPrefix, v.Recipient[:]).String()
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

// VoucherV2 captures the structured payload authorised by the fiat gateway
// under the V2 schema (see VoucherDomainV2). It carries the same voucher
// fields as VoucherV1 (embedded as Voucher, reusing VoucherV1's own
// MarshalJSON/UnmarshalJSON/wire shape unchanged) plus Provider and
// ProviderTxID, both of which are folded into the signed digest by Hash --
// unlike VoucherV1.Hash, which never covers either field. Voucher.Domain is
// expected to equal VoucherDomainV2 for a valid V2 voucher (verified
// on-chain, exactly mirroring how VoucherV1.Domain is checked against
// VoucherDomainV1).
type VoucherV2 struct {
	Voucher      VoucherV1 `json:"voucher"`
	Provider     string    `json:"provider"`
	ProviderTxID string    `json:"providerTxId"`
}

// Hash reconstructs the canonical message digest signed by the mint
// authority for a V2 voucher. NHB-AUDIT-C8 follow-up: this digest extends
// VoucherV1.Hash's payload string with the Provider and ProviderTxID
// fields, so a V2 voucher's signature is invalidated by ANY change to
// either -- the mint authority commits, at signing time, to the exact
// provider transaction identifier this voucher may ever be submitted with.
// That closes the griefing gap described on VoucherDomainV2: a V2
// signature simply does not exist for any ProviderTxID other than the one
// the signer chose, so nobody downstream of the signer -- including
// whoever assembles and broadcasts the on-chain transaction -- can pair a
// valid V2 signature with a different, self-chosen ProviderTxID the way
// they still can for V1.
func (v VoucherV2) Hash() []byte {
	amountStr := "0"
	if v.Voucher.Amount != nil {
		amountStr = v.Voucher.Amount.String()
	}
	payload := fmt.Sprintf("%s|chain=%d|token=%s|to=%s|amount=%s|fiat=%s|fiatAmt=%s|rate=%s|order=%s|nonce=%s|exp=%d|provider=%s|providerTxId=%s",
		strings.TrimSpace(v.Voucher.Domain),
		v.Voucher.ChainID,
		strings.TrimSpace(v.Voucher.Token),
		hex.EncodeToString(v.Voucher.Recipient[:]),
		amountStr,
		strings.TrimSpace(v.Voucher.Fiat),
		strings.TrimSpace(v.Voucher.FiatAmount),
		strings.TrimSpace(v.Voucher.Rate),
		strings.TrimSpace(v.Voucher.OrderID),
		strings.ToLower(hex.EncodeToString(v.Voucher.Nonce)),
		v.Voucher.Expiry,
		strings.TrimSpace(v.Provider),
		strings.TrimSpace(v.ProviderTxID),
	)
	return ethcrypto.Keccak256([]byte(payload))
}
