package main

import (
	"encoding/hex"
	"fmt"
	"strings"

	repoCrypto "nhbchain/crypto"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// VoucherV1 mirrors the payload the gateway submits to the node.
type VoucherV1 struct {
	Domain     string `json:"domain"`
	ChainID    int64  `json:"chainId"`
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

func (v VoucherV1) Hash() ([]byte, error) {
	if v.Domain == "" {
		return nil, fmt.Errorf("domain required")
	}
	if v.Recipient == "" {
		return nil, fmt.Errorf("recipient required")
	}
	recipientBytes, err := decodeBech32Address(v.Recipient)
	if err != nil {
		return nil, fmt.Errorf("decode recipient: %w", err)
	}
	payload := fmt.Sprintf("%s|chain=%d|token=%s|to=%s|amount=%s|fiat=%s|fiatAmt=%s|rate=%s|order=%s|nonce=%s|exp=%d",
		v.Domain,
		v.ChainID,
		v.Token,
		hex.EncodeToString(recipientBytes),
		v.Amount,
		v.Fiat,
		v.FiatAmount,
		v.Rate,
		v.OrderID,
		strings.ToLower(v.Nonce),
		v.Expiry,
	)
	hash := ethcrypto.Keccak256([]byte(payload))
	return hash, nil
}

func SignVoucher(v VoucherV1, privKeyHex string) ([]byte, error) {
	hash, err := v.Hash()
	if err != nil {
		return nil, err
	}
	pkHex := strings.TrimPrefix(privKeyHex, "0x")
	if pkHex == "" {
		return nil, fmt.Errorf("empty private key")
	}
	key, err := ethcrypto.HexToECDSA(pkHex)
	if err != nil {
		return nil, fmt.Errorf("load private key: %w", err)
	}
	sig, err := ethcrypto.Sign(hash, key)
	if err != nil {
		return nil, fmt.Errorf("sign voucher: %w", err)
	}
	return sig, nil
}

func RecoverVoucherSignerAddress(v VoucherV1, sig []byte) (string, error) {
	hash, err := v.Hash()
	if err != nil {
		return "", err
	}
	pub, err := ethcrypto.SigToPub(hash, sig)
	if err != nil {
		return "", fmt.Errorf("recover pubkey: %w", err)
	}
	addrBytes := ethcrypto.PubkeyToAddress(*pub).Bytes()
	addr, err := repoCrypto.NewAddress(repoCrypto.NHBPrefix, addrBytes)
	if err != nil {
		return "", err
	}
	return addr.String(), nil
}

func decodeBech32Address(addr string) ([]byte, error) {
	decoded, err := repoCrypto.DecodeAddress(addr)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(decoded.Bytes()))
	copy(out, decoded.Bytes())
	return out, nil
}
