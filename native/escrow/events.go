package escrow

import (
	"encoding/hex"
	"strconv"
	"strings"

	"nhbchain/core/types"
)

const (
	EventTypeEscrowCreated      = "escrow.created"
	EventTypeEscrowFunded       = "escrow.funded"
	EventTypeEscrowReleased     = "escrow.released"
	EventTypeEscrowRefunded     = "escrow.refunded"
	EventTypeEscrowExpired      = "escrow.expired"
	EventTypeEscrowDisputed     = "escrow.disputed"
	EventTypeEscrowResolved     = "escrow.resolved"
	EventTypeTradeCreated       = "escrow.trade.created"
	EventTypeTradePartialFunded = "escrow.trade.partial_funded"
	EventTypeTradeFunded        = "escrow.trade.funded"
	EventTypeTradeDisputed      = "escrow.trade.disputed"
	EventTypeTradeResolved      = "escrow.trade.resolved"
	EventTypeTradeSettled       = "escrow.trade.settled"
	EventTypeTradeExpired       = "escrow.trade.expired"
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

// NewTradeCreatedEvent emits the canonical payload for a newly created trade.
func NewTradeCreatedEvent(t *Trade) *types.Event {
	return newTradeEvent(EventTypeTradeCreated, t, "")
}

// NewTradePartialFundedEvent emits the payload when a trade is partially funded.
func NewTradePartialFundedEvent(t *Trade) *types.Event {
	return newTradeEvent(EventTypeTradePartialFunded, t, "")
}

// NewTradeFundedEvent emits the payload when a trade is fully funded.
func NewTradeFundedEvent(t *Trade) *types.Event {
	return newTradeEvent(EventTypeTradeFunded, t, "")
}

// NewTradeDisputedEvent emits the payload when a trade is disputed.
func NewTradeDisputedEvent(t *Trade) *types.Event {
	return newTradeEvent(EventTypeTradeDisputed, t, "")
}

// NewTradeResolvedEvent emits the payload when a disputed trade is resolved.
func NewTradeResolvedEvent(t *Trade, outcome string) *types.Event {
	return newTradeEvent(EventTypeTradeResolved, t, outcome)
}

// NewTradeSettledEvent emits the payload when a trade is atomically settled.
func NewTradeSettledEvent(t *Trade) *types.Event {
	return newTradeEvent(EventTypeTradeSettled, t, "")
}

// NewTradeExpiredEvent emits the payload when a trade expires.
func NewTradeExpiredEvent(t *Trade) *types.Event {
	return newTradeEvent(EventTypeTradeExpired, t, "")
}

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

func newTradeEvent(eventType string, t *Trade, extra string) *types.Event {
	attrs := make(map[string]string)
	if t == nil {
		return &types.Event{Type: eventType, Attributes: attrs}
	}
	sanitized, err := SanitizeTrade(t)
	if err != nil {
		return &types.Event{Type: eventType, Attributes: attrs}
	}
	attrs["tradeId"] = hex.EncodeToString(sanitized.ID[:])
	attrs["offerId"] = sanitized.OfferID
	attrs["buyer"] = hex.EncodeToString(sanitized.Buyer[:])
	attrs["seller"] = hex.EncodeToString(sanitized.Seller[:])
	attrs["baseToken"] = sanitized.BaseToken
	attrs["baseAmount"] = sanitized.BaseAmount.String()
	attrs["quoteToken"] = sanitized.QuoteToken
	attrs["quoteAmount"] = sanitized.QuoteAmount.String()
	attrs["escrowBaseId"] = hex.EncodeToString(sanitized.EscrowBase[:])
	attrs["escrowQuoteId"] = hex.EncodeToString(sanitized.EscrowQuote[:])
	attrs["deadline"] = strconv.FormatInt(sanitized.Deadline, 10)
	attrs["createdAt"] = strconv.FormatInt(sanitized.CreatedAt, 10)
	attrs["status"] = strconv.FormatUint(uint64(sanitized.Status), 10)
	if strings.TrimSpace(extra) != "" {
		attrs["outcome"] = extra
	}
	return &types.Event{Type: eventType, Attributes: attrs}
}
