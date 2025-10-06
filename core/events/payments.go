package events

import (
	"encoding/hex"
	"strings"

	"nhbchain/core/types"
)

const (
	// TypePaymentIntentConsumed is emitted once a POS payment intent reference
	// is successfully consumed on-chain.
	TypePaymentIntentConsumed = "payments.intent_consumed"
)

// PaymentIntentConsumed represents a point-of-sale payment intent that has been
// observed on-chain. Downstream consumers can correlate the intent reference
// with the originating merchant request to reconcile settlement.
type PaymentIntentConsumed struct {
	IntentRef []byte
	TxHash    []byte
	Merchant  string
	DeviceID  string
}

// EventType satisfies the events.Event interface.
func (PaymentIntentConsumed) EventType() string { return TypePaymentIntentConsumed }

// Event converts the structured payload into a wire-friendly representation for
// RPC subscribers.
func (e PaymentIntentConsumed) Event() *types.Event {
	if len(e.IntentRef) == 0 || len(e.TxHash) == 0 {
		return nil
	}
	attrs := map[string]string{
		"intentRef": hex.EncodeToString(e.IntentRef),
		"txHash":    withHexPrefix(e.TxHash),
	}
	if merchant := strings.TrimSpace(e.Merchant); merchant != "" {
		attrs["merchantAddr"] = merchant
	}
	if device := strings.TrimSpace(e.DeviceID); device != "" {
		attrs["deviceId"] = device
	}
	return &types.Event{Type: TypePaymentIntentConsumed, Attributes: attrs}
}

func withHexPrefix(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	encoded := hex.EncodeToString(raw)
	if encoded == "" {
		return ""
	}
	return "0x" + encoded
}
