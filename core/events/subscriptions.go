package events

import (
	"math/big"
	"strconv"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	// TypeSubscriptionPlanCreated is emitted when a merchant creates a new
	// recurring-billing Plan.
	TypeSubscriptionPlanCreated = "subscriptions.plan.created"
	// TypeSubscriptionPlanUpdated is emitted when a merchant updates a
	// Plan's Name/Active flag.
	TypeSubscriptionPlanUpdated = "subscriptions.plan.updated"
	// TypeSubscriptionCreated is emitted when a payer signs the standing
	// mandate transaction (TxTypeSubscriptionSubscribe) against a Plan.
	TypeSubscriptionCreated = "subscriptions.subscription.created"
	// TypeSubscriptionCancelled is emitted when a payer, merchant, or
	// admin cancels a subscription.
	TypeSubscriptionCancelled = "subscriptions.subscription.cancelled"
	// TypeSubscriptionSuspended is emitted when a subscription is
	// force-terminated after exhausting its retry budget.
	TypeSubscriptionSuspended = "subscriptions.subscription.suspended"
	// TypeSubscriptionChargeSucceeded is emitted once per successful
	// settlement attempt.
	TypeSubscriptionChargeSucceeded = "subscriptions.charge.succeeded"
	// TypeSubscriptionChargeFailed is emitted once per failed settlement
	// attempt -- the portal's dunning-email scheduler and merchant
	// webhooks are both driven by this event.
	TypeSubscriptionChargeFailed = "subscriptions.charge.failed"
)

// SubscriptionPlanCreated records a merchant's new recurring price.
type SubscriptionPlanCreated struct {
	PlanID          uint64
	Merchant        [20]byte
	PriceWei        *big.Int
	Asset           string
	IntervalSeconds uint64
}

func (SubscriptionPlanCreated) EventType() string { return TypeSubscriptionPlanCreated }

func (p SubscriptionPlanCreated) Event() *types.Event {
	price := big.NewInt(0)
	if p.PriceWei != nil {
		price = new(big.Int).Set(p.PriceWei)
	}
	attrs := map[string]string{
		"planId":          strconv.FormatUint(p.PlanID, 10),
		"priceWei":        price.String(),
		"asset":           p.Asset,
		"intervalSeconds": strconv.FormatUint(p.IntervalSeconds, 10),
	}
	if p.Merchant != ([20]byte{}) {
		attrs["merchant"] = crypto.MustNewAddress(crypto.NHBPrefix, p.Merchant[:]).String()
	}
	return &types.Event{Type: TypeSubscriptionPlanCreated, Attributes: attrs}
}

// SubscriptionPlanUpdated records a mutable-field update to an existing Plan.
type SubscriptionPlanUpdated struct {
	PlanID uint64
	Name   string
	Active bool
}

func (SubscriptionPlanUpdated) EventType() string { return TypeSubscriptionPlanUpdated }

func (p SubscriptionPlanUpdated) Event() *types.Event {
	return &types.Event{Type: TypeSubscriptionPlanUpdated, Attributes: map[string]string{
		"planId": strconv.FormatUint(p.PlanID, 10),
		"name":   p.Name,
		"active": strconv.FormatBool(p.Active),
	}}
}

// SubscriptionCreated records a payer's new standing mandate.
type SubscriptionCreated struct {
	SubscriptionID uint64
	PlanID         uint64
	Payer          [20]byte
	Merchant       [20]byte
	PriceWei       *big.Int
	Asset          string
	NextChargeAt   uint64
}

func (SubscriptionCreated) EventType() string { return TypeSubscriptionCreated }

func (s SubscriptionCreated) Event() *types.Event {
	price := big.NewInt(0)
	if s.PriceWei != nil {
		price = new(big.Int).Set(s.PriceWei)
	}
	attrs := map[string]string{
		"subscriptionId": strconv.FormatUint(s.SubscriptionID, 10),
		"planId":         strconv.FormatUint(s.PlanID, 10),
		"priceWei":       price.String(),
		"asset":          s.Asset,
		"nextChargeAt":   strconv.FormatUint(s.NextChargeAt, 10),
	}
	if s.Payer != ([20]byte{}) {
		attrs["payer"] = crypto.MustNewAddress(crypto.NHBPrefix, s.Payer[:]).String()
	}
	if s.Merchant != ([20]byte{}) {
		attrs["merchant"] = crypto.MustNewAddress(crypto.NHBPrefix, s.Merchant[:]).String()
	}
	return &types.Event{Type: TypeSubscriptionCreated, Attributes: attrs}
}

// SubscriptionCancelled records an explicit payer/merchant/admin cancellation.
type SubscriptionCancelled struct {
	SubscriptionID uint64
	Payer          [20]byte
	Merchant       [20]byte
}

func (SubscriptionCancelled) EventType() string { return TypeSubscriptionCancelled }

func (s SubscriptionCancelled) Event() *types.Event {
	attrs := map[string]string{
		"subscriptionId": strconv.FormatUint(s.SubscriptionID, 10),
	}
	if s.Payer != ([20]byte{}) {
		attrs["payer"] = crypto.MustNewAddress(crypto.NHBPrefix, s.Payer[:]).String()
	}
	if s.Merchant != ([20]byte{}) {
		attrs["merchant"] = crypto.MustNewAddress(crypto.NHBPrefix, s.Merchant[:]).String()
	}
	return &types.Event{Type: TypeSubscriptionCancelled, Attributes: attrs}
}

// SubscriptionSuspended records a subscription force-terminated after
// exhausting its retry budget (Config.MaxRetries consecutive failures).
type SubscriptionSuspended struct {
	SubscriptionID uint64
	Payer          [20]byte
	Merchant       [20]byte
	FailedAttempts uint32
}

func (SubscriptionSuspended) EventType() string { return TypeSubscriptionSuspended }

func (s SubscriptionSuspended) Event() *types.Event {
	attrs := map[string]string{
		"subscriptionId": strconv.FormatUint(s.SubscriptionID, 10),
		"failedAttempts": strconv.FormatUint(uint64(s.FailedAttempts), 10),
	}
	if s.Payer != ([20]byte{}) {
		attrs["payer"] = crypto.MustNewAddress(crypto.NHBPrefix, s.Payer[:]).String()
	}
	if s.Merchant != ([20]byte{}) {
		attrs["merchant"] = crypto.MustNewAddress(crypto.NHBPrefix, s.Merchant[:]).String()
	}
	return &types.Event{Type: TypeSubscriptionSuspended, Attributes: attrs}
}

// SubscriptionChargeSucceeded records one successful settlement attempt.
type SubscriptionChargeSucceeded struct {
	SubscriptionID uint64
	Payer          [20]byte
	Merchant       [20]byte
	Asset          string
	AmountWei      *big.Int
	FeeWei         *big.Int
	AttemptNumber  uint32
	NextChargeAt   uint64
}

func (SubscriptionChargeSucceeded) EventType() string { return TypeSubscriptionChargeSucceeded }

func (c SubscriptionChargeSucceeded) Event() *types.Event {
	amount := big.NewInt(0)
	if c.AmountWei != nil {
		amount = new(big.Int).Set(c.AmountWei)
	}
	fee := big.NewInt(0)
	if c.FeeWei != nil {
		fee = new(big.Int).Set(c.FeeWei)
	}
	attrs := map[string]string{
		"subscriptionId": strconv.FormatUint(c.SubscriptionID, 10),
		"asset":          c.Asset,
		"amountWei":      amount.String(),
		"feeWei":         fee.String(),
		"attemptNumber":  strconv.FormatUint(uint64(c.AttemptNumber), 10),
		"nextChargeAt":   strconv.FormatUint(c.NextChargeAt, 10),
	}
	if c.Payer != ([20]byte{}) {
		attrs["payer"] = crypto.MustNewAddress(crypto.NHBPrefix, c.Payer[:]).String()
	}
	if c.Merchant != ([20]byte{}) {
		attrs["merchant"] = crypto.MustNewAddress(crypto.NHBPrefix, c.Merchant[:]).String()
	}
	return &types.Event{Type: TypeSubscriptionChargeSucceeded, Attributes: attrs}
}

// SubscriptionChargeFailed records one failed settlement attempt --
// the portal's dunning-email scheduler and merchant webhooks are both
// driven by reading this event back.
type SubscriptionChargeFailed struct {
	SubscriptionID uint64
	Payer          [20]byte
	Merchant       [20]byte
	AttemptNumber  uint32
	FailureReason  string
	NewStatus      string
	NextChargeAt   uint64
}

func (SubscriptionChargeFailed) EventType() string { return TypeSubscriptionChargeFailed }

func (c SubscriptionChargeFailed) Event() *types.Event {
	attrs := map[string]string{
		"subscriptionId": strconv.FormatUint(c.SubscriptionID, 10),
		"attemptNumber":  strconv.FormatUint(uint64(c.AttemptNumber), 10),
		"failureReason":  c.FailureReason,
		"newStatus":      c.NewStatus,
		"nextChargeAt":   strconv.FormatUint(c.NextChargeAt, 10),
	}
	if c.Payer != ([20]byte{}) {
		attrs["payer"] = crypto.MustNewAddress(crypto.NHBPrefix, c.Payer[:]).String()
	}
	if c.Merchant != ([20]byte{}) {
		attrs["merchant"] = crypto.MustNewAddress(crypto.NHBPrefix, c.Merchant[:]).String()
	}
	return &types.Event{Type: TypeSubscriptionChargeFailed, Attributes: attrs}
}
