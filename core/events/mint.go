package events

import (
	"math/big"
	"strings"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeMintSettled is emitted whenever an invoice-backed mint completes.
	TypeMintSettled = "mint.settled"
)

type MintSettled struct {
	InvoiceID   string
	Recipient   [20]byte
	Token       string
	Amount      *big.Int
	TxHash      string
	VoucherHash string
}

func (MintSettled) EventType() string { return TypeMintSettled }

func (e MintSettled) Event() *types.Event {
	if e.Amount == nil {
		e.Amount = big.NewInt(0)
	}
	txHash := strings.TrimSpace(e.TxHash)
	if txHash != "" && !strings.HasPrefix(txHash, "0x") {
		txHash = "0x" + txHash
	}
	voucherHash := strings.TrimSpace(e.VoucherHash)
	if voucherHash != "" && !strings.HasPrefix(voucherHash, "0x") {
		voucherHash = "0x" + voucherHash
	}
	return &types.Event{
		Type: TypeMintSettled,
		Attributes: map[string]string{
			"invoiceId":   e.InvoiceID,
			"recipient":   crypto.MustNewAddress(crypto.NHBPrefix, e.Recipient[:]).String(),
			"token":       e.Token,
			"amount":      e.Amount.String(),
			"txHash":      txHash,
			"voucherHash": voucherHash,
		},
	}
}
