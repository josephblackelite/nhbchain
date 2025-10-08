package events

import (
	"encoding/hex"
	"math/big"
	"strings"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeTransfer is emitted for native NHB and ZNHB balance movements.
	TypeTransfer = "transfer.native"
)

type Transfer struct {
	Asset  string
	From   [20]byte
	To     [20]byte
	Amount *big.Int
	TxHash [32]byte
}

func (Transfer) EventType() string { return TypeTransfer }

func (e Transfer) Event() *types.Event {
	attrs := map[string]string{}
	if asset := normalizeAsset(e.Asset); asset != "" {
		attrs["asset"] = asset
	}
	attrs["from"] = crypto.MustNewAddress(crypto.NHBPrefix, e.From[:]).String()
	attrs["to"] = crypto.MustNewAddress(crypto.NHBPrefix, e.To[:]).String()
	attrs["amount"] = formatAmount(e.Amount)
	if !zeroBytes(e.TxHash[:]) {
		attrs["txHash"] = "0x" + strings.ToLower(hex.EncodeToString(e.TxHash[:]))
	}
	return &types.Event{Type: TypeTransfer, Attributes: attrs}
}
