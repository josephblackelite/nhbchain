package events

import (
	"encoding/hex"
	"math/big"
	"strconv"
	"strings"

	"nhbchain/core/types"
)

const (
	// TypePaymentIntentConsumed is emitted once a POS payment intent reference
	// is successfully consumed on-chain.
	TypePaymentIntentConsumed = "payments.intent_consumed"
	// TypePaymentAuthorized is emitted when funds are locked for a POS
	// authorization.
	TypePaymentAuthorized = "payments.authorized"
	// TypePaymentCaptured is emitted after an authorization is successfully
	// captured and the funds transferred to the merchant.
	TypePaymentCaptured = "payments.captured"
	// TypePaymentVoided marks authorizations that returned funds to the
	// payer either manually or due to expiry.
	TypePaymentVoided = "payments.voided"
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

// PaymentAuthorized summarises a newly created payment authorization that has
// locked funds on the payer account.
type PaymentAuthorized struct {
	AuthorizationID [32]byte
	Payer           [20]byte
	Merchant        [20]byte
	Amount          *big.Int
	Expiry          uint64
	IntentRef       []byte
}

// EventType satisfies the events.Event interface.
func (PaymentAuthorized) EventType() string { return TypePaymentAuthorized }

// Event converts the structured payload into a wire-friendly representation.
func (e PaymentAuthorized) Event() *types.Event {
	if zeroBytes(e.AuthorizationID[:]) {
		return nil
	}
	attrs := map[string]string{
		"authorizationId": hex.EncodeToString(e.AuthorizationID[:]),
	}
	if !zeroBytes(e.Payer[:]) {
		attrs["payer"] = hex.EncodeToString(e.Payer[:])
	}
	if !zeroBytes(e.Merchant[:]) {
		attrs["merchant"] = hex.EncodeToString(e.Merchant[:])
	}
	if e.Amount != nil {
		attrs["amount"] = e.Amount.String()
	}
	if e.Expiry != 0 {
		attrs["expiry"] = strconv.FormatUint(e.Expiry, 10)
	}
	if len(e.IntentRef) > 0 {
		attrs["intentRef"] = hex.EncodeToString(e.IntentRef)
	}
	return &types.Event{Type: TypePaymentAuthorized, Attributes: attrs}
}

// PaymentCaptured reports a successful capture event for a payment
// authorization.
type PaymentCaptured struct {
	AuthorizationID [32]byte
	Payer           [20]byte
	Merchant        [20]byte
	CapturedAmount  *big.Int
	RefundedAmount  *big.Int
}

// EventType satisfies the events.Event interface.
func (PaymentCaptured) EventType() string { return TypePaymentCaptured }

// Event converts the capture payload into a broadcastable event.
func (e PaymentCaptured) Event() *types.Event {
	if zeroBytes(e.AuthorizationID[:]) {
		return nil
	}
	attrs := map[string]string{
		"authorizationId": hex.EncodeToString(e.AuthorizationID[:]),
	}
	if !zeroBytes(e.Payer[:]) {
		attrs["payer"] = hex.EncodeToString(e.Payer[:])
	}
	if !zeroBytes(e.Merchant[:]) {
		attrs["merchant"] = hex.EncodeToString(e.Merchant[:])
	}
	if e.CapturedAmount != nil {
		attrs["capturedAmount"] = e.CapturedAmount.String()
	}
	if e.RefundedAmount != nil {
		attrs["refundedAmount"] = e.RefundedAmount.String()
	}
	return &types.Event{Type: TypePaymentCaptured, Attributes: attrs}
}

// PaymentVoided records the release of an authorization lock back to the payer.
type PaymentVoided struct {
	AuthorizationID [32]byte
	Payer           [20]byte
	Merchant        [20]byte
	RefundedAmount  *big.Int
	Reason          string
	Expired         bool
}

// EventType satisfies the events.Event interface.
func (PaymentVoided) EventType() string { return TypePaymentVoided }

// Event converts the void payload into a broadcastable event.
func (e PaymentVoided) Event() *types.Event {
	if zeroBytes(e.AuthorizationID[:]) {
		return nil
	}
	attrs := map[string]string{
		"authorizationId": hex.EncodeToString(e.AuthorizationID[:]),
		"expired":         strconv.FormatBool(e.Expired),
	}
	if !zeroBytes(e.Payer[:]) {
		attrs["payer"] = hex.EncodeToString(e.Payer[:])
	}
	if !zeroBytes(e.Merchant[:]) {
		attrs["merchant"] = hex.EncodeToString(e.Merchant[:])
	}
	if e.RefundedAmount != nil {
		attrs["refundedAmount"] = e.RefundedAmount.String()
	}
	if reason := strings.TrimSpace(e.Reason); reason != "" {
		attrs["reason"] = reason
	}
	return &types.Event{Type: TypePaymentVoided, Attributes: attrs}
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

func zeroBytes(b []byte) bool {
	for _, v := range b {
		if v != 0 {
			return false
		}
	}
	return true
}
