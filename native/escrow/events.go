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
	EventTypeRealmCreated       = "escrow.realm.created"
	EventTypeRealmUpdated       = "escrow.realm.updated"
	EventTypeTradeCreated       = "escrow.trade.created"
	EventTypeTradePartialFunded = "escrow.trade.partial_funded"
	EventTypeTradeFunded        = "escrow.trade.funded"
	EventTypeTradeDisputed      = "escrow.trade.disputed"
	EventTypeTradeResolved      = "escrow.trade.resolved"
	EventTypeTradeSettled       = "escrow.trade.settled"
	EventTypeTradeExpired       = "escrow.trade.expired"
	EventTypeMilestoneCreated   = "escrow.milestone.created"
	EventTypeMilestoneFunded    = "escrow.milestone.funded"
	EventTypeMilestoneReleased  = "escrow.milestone.released"
	EventTypeMilestoneCancelled = "escrow.milestone.cancelled"
	EventTypeMilestoneDue       = "escrow.milestone.leg_due"
)

// NewRealmCreatedEvent emits the canonical payload for a newly created realm.
func NewRealmCreatedEvent(r *EscrowRealm) *types.Event {
	return newRealmEvent(EventTypeRealmCreated, r)
}

// NewRealmUpdatedEvent emits the canonical payload for an updated realm.
func NewRealmUpdatedEvent(r *EscrowRealm) *types.Event {
	return newRealmEvent(EventTypeRealmUpdated, r)
}

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
// resolved. When supplied, the decision metadata is included for auditability.
func NewResolvedEvent(e *Escrow, outcome DecisionOutcome, metaHash [32]byte, signers [][20]byte) *types.Event {
	evt := newEscrowEvent(EventTypeEscrowResolved, e)
	if evt == nil {
		return nil
	}
	if outcome.Valid() {
		evt.Attributes["decision"] = outcome.String()
	}
	if metaHash != ([32]byte{}) {
		evt.Attributes["decisionMetadata"] = hex.EncodeToString(metaHash[:])
	}
	if len(signers) > 0 {
		evt.Attributes["decisionSigners"] = formatArbitratorMembers(signers)
	}
	return evt
}

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

// NewMilestoneCreatedEvent emits the canonical payload for a newly created
// milestone project.
func NewMilestoneCreatedEvent(project *MilestoneProject) *types.Event {
	return newMilestoneEvent(EventTypeMilestoneCreated, project, nil)
}

// NewMilestoneFundedEvent emits the payload for a funded milestone leg.
func NewMilestoneFundedEvent(project *MilestoneProject, leg *MilestoneLeg) *types.Event {
	return newMilestoneEvent(EventTypeMilestoneFunded, project, leg)
}

// NewMilestoneReleasedEvent emits the payload when a milestone leg is released.
func NewMilestoneReleasedEvent(project *MilestoneProject, leg *MilestoneLeg) *types.Event {
	return newMilestoneEvent(EventTypeMilestoneReleased, project, leg)
}

// NewMilestoneCancelledEvent emits the payload when a leg or project is
// cancelled.
func NewMilestoneCancelledEvent(project *MilestoneProject, leg *MilestoneLeg) *types.Event {
	return newMilestoneEvent(EventTypeMilestoneCancelled, project, leg)
}

// NewMilestoneDueEvent emits the payload when a funded leg reaches its deadline
// without release.
func NewMilestoneDueEvent(project *MilestoneProject, leg *MilestoneLeg) *types.Event {
	return newMilestoneEvent(EventTypeMilestoneDue, project, leg)
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
	if strings.TrimSpace(sanitized.RealmID) != "" {
		attrs["realmId"] = sanitized.RealmID
	}
	if sanitized.FrozenArb != nil {
		attrs["realmVersion"] = strconv.FormatUint(sanitized.FrozenArb.RealmVersion, 10)
		attrs["policyNonce"] = strconv.FormatUint(sanitized.FrozenArb.PolicyNonce, 10)
		attrs["arbScheme"] = strconv.FormatUint(uint64(sanitized.FrozenArb.Scheme), 10)
		attrs["arbThreshold"] = strconv.FormatUint(uint64(sanitized.FrozenArb.Threshold), 10)
		if members := formatArbitratorMembers(sanitized.FrozenArb.Members); members != "" {
			attrs["arbitrators"] = members
		}
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

func newRealmEvent(eventType string, r *EscrowRealm) *types.Event {
	attrs := make(map[string]string)
	if r == nil {
		return &types.Event{Type: eventType, Attributes: attrs}
	}
	sanitized, err := SanitizeEscrowRealm(r)
	if err != nil {
		return &types.Event{Type: eventType, Attributes: attrs}
	}
	attrs["realmId"] = sanitized.ID
	attrs["version"] = strconv.FormatUint(sanitized.Version, 10)
	attrs["nextNonce"] = strconv.FormatUint(sanitized.NextPolicyNonce, 10)
	attrs["createdAt"] = strconv.FormatInt(sanitized.CreatedAt, 10)
	attrs["updatedAt"] = strconv.FormatInt(sanitized.UpdatedAt, 10)
	if sanitized.Arbitrators != nil {
		attrs["arbScheme"] = strconv.FormatUint(uint64(sanitized.Arbitrators.Scheme), 10)
		attrs["arbThreshold"] = strconv.FormatUint(uint64(sanitized.Arbitrators.Threshold), 10)
		if members := formatArbitratorMembers(sanitized.Arbitrators.Members); members != "" {
			attrs["arbitrators"] = members
		}
	}
	return &types.Event{Type: eventType, Attributes: attrs}
}

func newMilestoneEvent(eventType string, project *MilestoneProject, leg *MilestoneLeg) *types.Event {
	attrs := make(map[string]string)
	if project == nil {
		return &types.Event{Type: eventType, Attributes: attrs}
	}
	sanitized, err := SanitizeMilestoneProject(project)
	if err != nil {
		return &types.Event{Type: eventType, Attributes: attrs}
	}
	attrs["projectId"] = hex.EncodeToString(sanitized.ID[:])
	attrs["payer"] = hex.EncodeToString(sanitized.Payer[:])
	attrs["payee"] = hex.EncodeToString(sanitized.Payee[:])
	attrs["realmId"] = sanitized.RealmID
	attrs["status"] = strconv.FormatUint(uint64(sanitized.Status), 10)
	if sanitized.Subscription != nil {
		attrs["subscriptionInterval"] = strconv.FormatInt(sanitized.Subscription.IntervalSeconds, 10)
		attrs["subscriptionNext"] = strconv.FormatInt(sanitized.Subscription.NextReleaseAt, 10)
		attrs["subscriptionActive"] = strconv.FormatBool(sanitized.Subscription.Active)
	}
	if leg != nil {
		attrs["legId"] = strconv.FormatUint(leg.ID, 10)
		attrs["legType"] = strconv.FormatUint(uint64(leg.Type), 10)
		attrs["legAmount"] = leg.Amount.String()
		attrs["legDeadline"] = strconv.FormatInt(leg.Deadline, 10)
		attrs["legStatus"] = strconv.FormatUint(uint64(leg.Status), 10)
	}
	return &types.Event{Type: eventType, Attributes: attrs}
}

func formatArbitratorMembers(members [][20]byte) string {
	if len(members) == 0 {
		return ""
	}
	parts := make([]string, 0, len(members))
	for _, member := range members {
		if member == ([20]byte{}) {
			continue
		}
		parts = append(parts, hex.EncodeToString(member[:]))
	}
	return strings.Join(parts, ",")
}
