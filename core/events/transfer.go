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
	// TypeTransferZNHBBlocked is emitted when ZNHB transfers are rejected due to a governance pause.
	TypeTransferZNHBBlocked = "transfer.znhb.paused"
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

// TransferZNHBBlocked captures rejected ZNHB transfers when the pause toggle is active.
type TransferZNHBBlocked struct {
	Asset  string
	From   [20]byte
	To     [20]byte
	Reason string
	TxHash [32]byte
}

// EventType satisfies the Event interface.
func (TransferZNHBBlocked) EventType() string { return TypeTransferZNHBBlocked }

// Event converts the structured payload into a broadcastable event.
func (e TransferZNHBBlocked) Event() *types.Event {
	attrs := map[string]string{}
	if asset := normalizeAsset(e.Asset); asset != "" {
		attrs["asset"] = asset
	}
	if !zeroAddress(e.From) {
		attrs["from"] = crypto.MustNewAddress(crypto.NHBPrefix, e.From[:]).String()
	}
	if !zeroAddress(e.To) {
		attrs["to"] = crypto.MustNewAddress(crypto.NHBPrefix, e.To[:]).String()
	}
	if reason := strings.TrimSpace(e.Reason); reason != "" {
		attrs["reason"] = reason
	}
	if !zeroBytes(e.TxHash[:]) {
		attrs["txHash"] = "0x" + strings.ToLower(hex.EncodeToString(e.TxHash[:]))
	}
	if len(attrs) == 0 {
		return nil
	}
	return &types.Event{Type: TypeTransferZNHBBlocked, Attributes: attrs}
}

func zeroAddress(addr [20]byte) bool {
	return zeroBytes(addr[:])
}
