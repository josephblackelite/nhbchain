package subscriptions

import "math/big"

// PlanID uniquely identifies a merchant-defined recurring price. Assigned
// from a monotonic on-chain counter at creation time (state.Manager's
// SubscriptionsNextPlanID), mirroring native/governance's
// GovernanceNextProposalID precedent exactly -- unlike native/loyalty's
// client-supplied ProgramID, the caller never invents or races an ID.
type PlanID uint64

// SubscriptionID uniquely identifies a payer's standing authorization
// against a Plan. Assigned from a separate monotonic on-chain counter
// (SubscriptionsNextSubscriptionID).
type SubscriptionID uint64

// Asset identifies which native balance a Plan charges against. Mirrors
// native/fees.AssetNHB/AssetZNHB exactly (same two strings) so callers
// never need a translation layer between the two packages.
type Asset string

const (
	AssetNHB  Asset = "NHB"
	AssetZNHB Asset = "ZNHB"
)

// Plan is a merchant-defined recurring price -- deliberately a single
// object where Stripe requires creating a separate Product and Price
// before anything can be charged against it. Once created,
// PriceWei/Asset/IntervalSeconds/TrialPeriodSeconds are immutable
// (mirroring Stripe's own Price immutability, for the same reason: a
// subscriber's Subscription snapshots these values at subscribe time, so
// silently mutating them out from under an active subscription would be a
// retroactive repricing -- a merchant who wants different terms creates a
// new Plan instead). Name and Active remain mutable via UpdatePlan.
//
// New fields must always be appended at the end, never inserted between
// existing ones: Plan is persisted via the generic KVPut/KVGet helpers,
// which RLP-encode structs positionally (by declaration order).
type Plan struct {
	ID                 PlanID
	Merchant           [20]byte
	Name               string
	PriceWei           *big.Int
	Asset              Asset
	IntervalSeconds    uint64
	TrialPeriodSeconds uint64
	Active             bool
	CreatedAt          uint64
}

// SubscriptionStatus mirrors the small, well-understood state machine every
// real-world recurring-billing system converges on (Stripe's own
// subscription statuses are the closest public reference).
type SubscriptionStatus uint8

const (
	// SubscriptionStatusActive means charges are current; the subscription
	// sits in the due-index awaiting its NextChargeAt.
	SubscriptionStatusActive SubscriptionStatus = iota
	// SubscriptionStatusPastDue means the most recent charge attempt
	// failed (insufficient balance) and a retry is scheduled at
	// NextChargeAt, within Config.MaxRetries.
	SubscriptionStatusPastDue
	// SubscriptionStatusCancelled means the payer or merchant explicitly
	// cancelled -- terminal, never re-enters the due-index.
	SubscriptionStatusCancelled
	// SubscriptionStatusSuspended means Config.MaxRetries consecutive
	// charge failures were reached -- terminal, never re-enters the
	// due-index, distinct from Cancelled so portal/API consumers can tell
	// "the payer chose to stop" from "the payer's funding ran out."
	SubscriptionStatusSuspended
)

// ChargeStatus records the outcome of a single settlement attempt.
type ChargeStatus uint8

const (
	ChargeStatusPaid ChargeStatus = iota
	ChargeStatusFailed
)

// Subscription is a payer's standing authorization for the chain to debit
// their account PriceWei every IntervalSeconds, entirely without a fresh
// signature at charge time -- the signature on the original
// TxTypeSubscriptionSubscribe transaction IS the standing mandate. This is
// safe specifically because the authorized amount is fixed and bounded
// (PriceWei, snapshotted from the Plan at subscribe time, never an
// open-ended or caller-suppliable amount) -- the same "bounded, known in
// advance" discipline every other system-initiated debit in this codebase
// already follows (see core/rewards_logic.go's settleEpochRewards and
// core/buyback_settlement.go's settleBuybackEpoch).
type Subscription struct {
	ID               SubscriptionID
	PlanID           PlanID
	Payer            [20]byte
	Merchant         [20]byte
	PriceWei         *big.Int
	Asset            Asset
	IntervalSeconds  uint64
	Status           SubscriptionStatus
	StartAt          uint64
	NextChargeAt     uint64
	CycleCount       uint64
	FailedAttempts   uint32
	LastChargeAt     uint64
	LastChargeStatus ChargeStatus
	CreatedAt        uint64
	CancelledAt      uint64
}

// Charge is an immutable audit record of one settlement attempt --
// successful or failed -- against a Subscription. Portal-side reminder and
// dunning emails, and merchant webhooks, are driven entirely by reading
// these back (via subscriptions_listCharges), not by any separate
// off-chain ledger of truth.
type Charge struct {
	SubscriptionID SubscriptionID
	PlanID         PlanID
	Payer          [20]byte
	Merchant       [20]byte
	Asset          Asset
	AmountWei      *big.Int
	FeeWei         *big.Int
	Status         ChargeStatus
	AttemptNumber  uint32
	ChargedAt      uint64
	FailureReason  string
}
