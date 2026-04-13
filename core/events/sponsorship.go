package events

import (
	"encoding/hex"
	"fmt"
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
        // TypePaymasterThrottled indicates a sponsorship request was throttled due to configured caps.
        TypePaymasterThrottled = "paymaster.throttled"
        // TypePaymasterAutoTopUp indicates the automatic top-up engine executed for a paymaster account.
        TypePaymasterAutoTopUp = "paymaster.autotopup"
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

// PaymasterThrottled captures the throttle context for a rejected sponsorship attempt.
type PaymasterThrottled struct {
	TxHash        [32]byte
	Scope         string
	Merchant      string
	DeviceID      string
	Day           string
	LimitWei      *big.Int
	UsedBudgetWei *big.Int
	AttemptWei    *big.Int
	TxCount       uint64
	LimitTxCount  uint64
}

// EventType satisfies the events.Event interface.
func (PaymasterThrottled) EventType() string { return TypePaymasterThrottled }

// Event renders the throttled payload.
func (e PaymasterThrottled) Event() *types.Event {
        attrs := map[string]string{
                "scope": strings.TrimSpace(e.Scope),
        }
        if e.TxHash != ([32]byte{}) {
		attrs["txHash"] = "0x" + hex.EncodeToString(e.TxHash[:])
	}
	if strings.TrimSpace(e.Merchant) != "" {
		attrs["merchant"] = strings.TrimSpace(e.Merchant)
	}
	if strings.TrimSpace(e.DeviceID) != "" {
		attrs["deviceId"] = strings.TrimSpace(e.DeviceID)
	}
	if strings.TrimSpace(e.Day) != "" {
		attrs["day"] = strings.TrimSpace(e.Day)
	}
	if e.LimitWei != nil {
		attrs["limitWei"] = new(big.Int).Set(e.LimitWei).String()
	}
	if e.UsedBudgetWei != nil {
		attrs["usedBudgetWei"] = new(big.Int).Set(e.UsedBudgetWei).String()
	}
	if e.AttemptWei != nil {
		attrs["attemptBudgetWei"] = new(big.Int).Set(e.AttemptWei).String()
	}
	if e.TxCount > 0 {
		attrs["txCount"] = fmt.Sprintf("%d", e.TxCount)
	}
	if e.LimitTxCount > 0 {
		attrs["limitTxCount"] = fmt.Sprintf("%d", e.LimitTxCount)
        }
        return &types.Event{Type: TypePaymasterThrottled, Attributes: attrs}
}

// PaymasterAutoTopUp captures the automatic top-up activity for a paymaster account.
type PaymasterAutoTopUp struct {
        Paymaster [20]byte
        Token     string
        AmountWei *big.Int
        BalanceWei *big.Int
        Day       string
        Status    string
        Reason    string
}

// EventType satisfies the events.Event interface.
func (PaymasterAutoTopUp) EventType() string { return TypePaymasterAutoTopUp }

// Event renders the automatic top-up payload.
func (e PaymasterAutoTopUp) Event() *types.Event {
        attrs := map[string]string{}
        if e.Paymaster != ([20]byte{}) {
                attrs["paymaster"] = crypto.MustNewAddress(crypto.ZNHBPrefix, e.Paymaster[:]).String()
        }
        token := strings.TrimSpace(e.Token)
        if token != "" {
                attrs["token"] = token
        }
        status := strings.TrimSpace(e.Status)
        if status != "" {
                attrs["status"] = status
        }
        reason := strings.TrimSpace(e.Reason)
        if reason != "" {
                attrs["reason"] = reason
        }
        if e.AmountWei != nil {
                attrs["amountWei"] = new(big.Int).Set(e.AmountWei).String()
        }
        if e.BalanceWei != nil {
                attrs["balanceWei"] = new(big.Int).Set(e.BalanceWei).String()
        }
        if strings.TrimSpace(e.Day) != "" {
                attrs["day"] = strings.TrimSpace(e.Day)
        }
        return &types.Event{Type: TypePaymasterAutoTopUp, Attributes: attrs}
}
