package events

import (
	"math/big"
	"strconv"
	"strings"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
        // TypeSwapMinted is emitted whenever a swap voucher mints ZNHB on-chain.
        TypeSwapMinted = "swap.minted"
        // TypeSwapAlertLimitHit indicates that a voucher submission triggered a configured limit.
        TypeSwapAlertLimitHit = "swap.alert.limit_hit"
        // TypeSwapAlertSanction indicates that a voucher recipient failed the sanctions hook.
        TypeSwapAlertSanction = "swap.alert.sanction"
        // TypeSwapAlertVelocity indicates that a voucher breached the mint velocity guardrail.
        TypeSwapAlertVelocity = "swap.alert.velocity"
        // TypeSwapBurnRecorded captures a burn receipt produced by the off-ramp.
        TypeSwapBurnRecorded = "swap.burn.recorded"
        // TypeSwapTreasuryReconciled is emitted when vouchers are marked as reconciled against treasury records.
        TypeSwapTreasuryReconciled = "swap.treasury.reconciled"
)

type SwapMinted struct {
	OrderID    string
	Recipient  [20]byte
	Amount     *big.Int
	Fiat       string
	FiatAmount string
	Rate       string
}

func (SwapMinted) EventType() string { return TypeSwapMinted }

func (e SwapMinted) Event() *types.Event {
	amount := big.NewInt(0)
	if e.Amount != nil {
		amount = new(big.Int).Set(e.Amount)
	}
	recipient := ""
	if e.Recipient != ([20]byte{}) {
		recipient = crypto.NewAddress(crypto.NHBPrefix, e.Recipient[:]).String()
	}
	return &types.Event{
		Type: TypeSwapMinted,
		Attributes: map[string]string{
			"orderId":    strings.TrimSpace(e.OrderID),
			"recipient":  recipient,
			"amount":     amount.String(),
			"fiat":       strings.TrimSpace(e.Fiat),
			"fiatAmount": strings.TrimSpace(e.FiatAmount),
			"rate":       strings.TrimSpace(e.Rate),
		},
	}
}

// SwapLimitAlert captures metadata describing a limit violation.
type SwapLimitAlert struct {
	Address      [20]byte
	Provider     string
	ProviderTxID string
	Limit        string
	Amount       *big.Int
	LimitValue   *big.Int
	CurrentValue *big.Int
}

// EventType returns the canonical event type string.
func (SwapLimitAlert) EventType() string { return TypeSwapAlertLimitHit }

// Event renders the event payload for downstream consumers.
func (a SwapLimitAlert) Event() *types.Event {
	attrs := map[string]string{
		"provider":     strings.TrimSpace(a.Provider),
		"providerTxId": strings.TrimSpace(a.ProviderTxID),
		"limit":        strings.TrimSpace(a.Limit),
	}
	if a.Address != ([20]byte{}) {
		attrs["address"] = crypto.NewAddress(crypto.NHBPrefix, a.Address[:]).String()
	}
	amount := big.NewInt(0)
	if a.Amount != nil {
		amount = new(big.Int).Set(a.Amount)
	}
	attrs["amount"] = amount.String()
	if a.LimitValue != nil {
		attrs["limitValue"] = new(big.Int).Set(a.LimitValue).String()
	}
	if a.CurrentValue != nil {
		attrs["currentValue"] = new(big.Int).Set(a.CurrentValue).String()
	}
	return &types.Event{Type: TypeSwapAlertLimitHit, Attributes: attrs}
}

// SwapSanctionAlert indicates a sanctions failure for a voucher recipient.
type SwapSanctionAlert struct {
	Address      [20]byte
	Provider     string
	ProviderTxID string
}

// EventType returns the canonical event type string.
func (SwapSanctionAlert) EventType() string { return TypeSwapAlertSanction }

// Event renders the sanction alert payload.
func (a SwapSanctionAlert) Event() *types.Event {
	attrs := map[string]string{
		"provider":     strings.TrimSpace(a.Provider),
		"providerTxId": strings.TrimSpace(a.ProviderTxID),
	}
	if a.Address != ([20]byte{}) {
		attrs["address"] = crypto.NewAddress(crypto.NHBPrefix, a.Address[:]).String()
	}
	return &types.Event{Type: TypeSwapAlertSanction, Attributes: attrs}
}

// SwapVelocityAlert records a velocity guardrail violation.
type SwapVelocityAlert struct {
	Address       [20]byte
	Provider      string
	ProviderTxID  string
	WindowSeconds uint64
	ObservedCount int
	AllowedMints  uint64
}

// EventType returns the canonical event type string.
func (SwapVelocityAlert) EventType() string { return TypeSwapAlertVelocity }

// Event renders the velocity alert payload.
func (a SwapVelocityAlert) Event() *types.Event {
        attrs := map[string]string{
                "provider":      strings.TrimSpace(a.Provider),
                "providerTxId":  strings.TrimSpace(a.ProviderTxID),
                "windowSeconds": strconv.FormatUint(a.WindowSeconds, 10),
                "allowedMints":  strconv.FormatUint(a.AllowedMints, 10),
                "observedCount": strconv.Itoa(a.ObservedCount),
        }
        if a.Address != ([20]byte{}) {
                attrs["address"] = crypto.NewAddress(crypto.NHBPrefix, a.Address[:]).String()
        }
        return &types.Event{Type: TypeSwapAlertVelocity, Attributes: attrs}
}

// SwapBurnRecorded encapsulates metadata describing an off-ramp burn receipt.
type SwapBurnRecorded struct {
        ReceiptID    string
        ProviderTxID string
        Token        string
        Amount       *big.Int
        BurnTx       string
        TreasuryTx   string
        VoucherIDs   []string
        ObservedAt   int64
}

// EventType returns the canonical burn receipt event identifier.
func (SwapBurnRecorded) EventType() string { return TypeSwapBurnRecorded }

// Event renders the burn receipt event for downstream consumers.
func (r SwapBurnRecorded) Event() *types.Event {
        amount := big.NewInt(0)
        if r.Amount != nil {
                amount = new(big.Int).Set(r.Amount)
        }
        attrs := map[string]string{
                "receiptId":   strings.TrimSpace(r.ReceiptID),
                "providerTxId": strings.TrimSpace(r.ProviderTxID),
                "token":       strings.TrimSpace(r.Token),
                "amountWei":   amount.String(),
        }
        if strings.TrimSpace(r.BurnTx) != "" {
                attrs["burnTx"] = strings.TrimSpace(r.BurnTx)
        }
        if strings.TrimSpace(r.TreasuryTx) != "" {
                attrs["treasuryTx"] = strings.TrimSpace(r.TreasuryTx)
        }
        if len(r.VoucherIDs) > 0 {
                attrs["vouchers"] = strings.Join(r.VoucherIDs, ",")
        }
        if r.ObservedAt > 0 {
                attrs["observedAt"] = strconv.FormatInt(r.ObservedAt, 10)
        }
        return &types.Event{Type: TypeSwapBurnRecorded, Attributes: attrs}
}

// SwapTreasuryReconciled captures a treasury reconciliation marker for minted vouchers.
type SwapTreasuryReconciled struct {
        VoucherIDs []string
        ReceiptID  string
        ObservedAt int64
}

// EventType returns the canonical reconciliation event identifier.
func (SwapTreasuryReconciled) EventType() string { return TypeSwapTreasuryReconciled }

// Event renders the reconciliation event payload.
func (r SwapTreasuryReconciled) Event() *types.Event {
        if len(r.VoucherIDs) == 0 {
                return nil
        }
        attrs := map[string]string{
                "vouchers": strings.Join(r.VoucherIDs, ","),
        }
        if strings.TrimSpace(r.ReceiptID) != "" {
                attrs["receiptId"] = strings.TrimSpace(r.ReceiptID)
        }
        if r.ObservedAt > 0 {
                attrs["observedAt"] = strconv.FormatInt(r.ObservedAt, 10)
        }
        return &types.Event{Type: TypeSwapTreasuryReconciled, Attributes: attrs}
}
