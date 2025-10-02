package events

import (
	"encoding/hex"
	"math/big"
	"strings"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeTxSponsorshipApplied indicates a transaction's gas was paid by a sponsor.
	TypeTxSponsorshipApplied = "tx.sponsorship.applied"
	// TypeTxSponsorshipFailed indicates the sponsorship request was rejected and gas fell back to the sender.
	TypeTxSponsorshipFailed = "tx.sponsorship.failed"
)

// TxSponsorshipApplied captures a successful paymaster sponsorship outcome.
type TxSponsorshipApplied struct {
	TxHash   [32]byte
	Sender   [20]byte
	Sponsor  [20]byte
	GasUsed  uint64
	GasPrice *big.Int
	Charged  *big.Int
	Refund   *big.Int
}

// EventType satisfies the events.Event interface.
func (TxSponsorshipApplied) EventType() string { return TypeTxSponsorshipApplied }

// Event renders the applied sponsorship payload.
func (e TxSponsorshipApplied) Event() *types.Event {
	attrs := map[string]string{
		"txHash":  "0x" + hex.EncodeToString(e.TxHash[:]),
		"gasUsed": new(big.Int).SetUint64(e.GasUsed).String(),
	}
	if e.Sender != ([20]byte{}) {
		attrs["sender"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Sender[:]).String()
	}
	if e.Sponsor != ([20]byte{}) {
		attrs["sponsor"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Sponsor[:]).String()
	}
	if e.GasPrice != nil {
		attrs["gasPriceWei"] = new(big.Int).Set(e.GasPrice).String()
	}
	if e.Charged != nil {
		attrs["chargedWei"] = new(big.Int).Set(e.Charged).String()
	}
	if e.Refund != nil {
		attrs["refundWei"] = new(big.Int).Set(e.Refund).String()
	}
	return &types.Event{Type: TypeTxSponsorshipApplied, Attributes: attrs}
}

// TxSponsorshipFailed captures why a sponsorship attempt was rejected.
type TxSponsorshipFailed struct {
	TxHash  [32]byte
	Sender  [20]byte
	Sponsor [20]byte
	Status  string
	Reason  string
}

// EventType satisfies the events.Event interface.
func (TxSponsorshipFailed) EventType() string { return TypeTxSponsorshipFailed }

// Event renders the failure payload.
func (e TxSponsorshipFailed) Event() *types.Event {
	attrs := map[string]string{
		"txHash": "0x" + hex.EncodeToString(e.TxHash[:]),
		"status": strings.TrimSpace(e.Status),
	}
	if strings.TrimSpace(e.Reason) != "" {
		attrs["reason"] = strings.TrimSpace(e.Reason)
	}
	if e.Sender != ([20]byte{}) {
		attrs["sender"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Sender[:]).String()
	}
	if e.Sponsor != ([20]byte{}) {
		attrs["sponsor"] = crypto.MustNewAddress(crypto.NHBPrefix, e.Sponsor[:]).String()
	}
	return &types.Event{Type: TypeTxSponsorshipFailed, Attributes: attrs}
}
