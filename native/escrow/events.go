package escrow

import (
	"encoding/hex"
	"strconv"

	"nhbchain/core/types"
)

const (
	EventTypeEscrowCreated  = "escrow.created"
	EventTypeEscrowFunded   = "escrow.funded"
	EventTypeEscrowReleased = "escrow.released"
	EventTypeEscrowRefunded = "escrow.refunded"
	EventTypeEscrowExpired  = "escrow.expired"
	EventTypeEscrowDisputed = "escrow.disputed"
	EventTypeEscrowResolved = "escrow.resolved"
)

// NewCreatedEvent returns the canonical event payload for a newly created
// escrow.
func NewCreatedEvent(e *Escrow) *types.Event { return newEscrowEvent(EventTypeEscrowCreated, e) }

// NewFundedEvent returns the canonical event payload emitted when an escrow is
// funded by the payer.
func NewFundedEvent(e *Escrow) *types.Event { return newEscrowEvent(EventTypeEscrowFunded, e) }

// NewReleasedEvent returns the canonical event payload for a release of escrow
// funds to the payee.
func NewReleasedEvent(e *Escrow) *types.Event { return newEscrowEvent(EventTypeEscrowReleased, e) }

// NewRefundedEvent returns the canonical event payload for an escrow refund to
// the payer.
func NewRefundedEvent(e *Escrow) *types.Event { return newEscrowEvent(EventTypeEscrowRefunded, e) }

// NewExpiredEvent returns the canonical event payload emitted when an escrow
// expires prior to release.
func NewExpiredEvent(e *Escrow) *types.Event { return newEscrowEvent(EventTypeEscrowExpired, e) }

// NewDisputedEvent returns the canonical event payload emitted when an escrow is
// marked as disputed.
func NewDisputedEvent(e *Escrow) *types.Event { return newEscrowEvent(EventTypeEscrowDisputed, e) }

// NewResolvedEvent returns the canonical event payload emitted when a dispute is
// resolved.
func NewResolvedEvent(e *Escrow) *types.Event { return newEscrowEvent(EventTypeEscrowResolved, e) }

func newEscrowEvent(eventType string, e *Escrow) *types.Event {
	attrs := make(map[string]string)
	if e == nil {
		return &types.Event{Type: eventType, Attributes: attrs}
	}
	sanitized, err := SanitizeEscrow(e)
	if err != nil {
		return &types.Event{Type: eventType, Attributes: attrs}
	}
	attrs["id"] = hex.EncodeToString(sanitized.ID[:])
	attrs["payer"] = hex.EncodeToString(sanitized.Payer[:])
	attrs["payee"] = hex.EncodeToString(sanitized.Payee[:])
	attrs["token"] = sanitized.Token
	attrs["amount"] = sanitized.Amount.String()
	attrs["feeBps"] = strconv.FormatUint(uint64(sanitized.FeeBps), 10)
	attrs["createdAt"] = strconv.FormatInt(sanitized.CreatedAt, 10)
	if sanitized.Mediator != ([20]byte{}) {
		attrs["mediator"] = hex.EncodeToString(sanitized.Mediator[:])
	}
	return &types.Event{Type: eventType, Attributes: attrs}
}
