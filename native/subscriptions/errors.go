package subscriptions

import "errors"

var (
	ErrNilPlan               = errors.New("subscriptions: nil plan")
	ErrInvalidPlan           = errors.New("subscriptions: invalid plan")
	ErrPlanExists            = errors.New("subscriptions: plan already exists")
	ErrPlanNotFound          = errors.New("subscriptions: plan not found")
	ErrPlanInactive          = errors.New("subscriptions: plan is not active")
	ErrImmutableField        = errors.New("subscriptions: immutable field")
	ErrUnauthorized          = errors.New("subscriptions: unauthorized")
	ErrNilSubscription       = errors.New("subscriptions: nil subscription")
	ErrSubscriptionExists    = errors.New("subscriptions: subscription already exists")
	ErrSubscriptionNotFound  = errors.New("subscriptions: subscription not found")
	ErrSubscriptionNotActive = errors.New("subscriptions: subscription is not active or past due")
	ErrAlreadyCancelled      = errors.New("subscriptions: subscription already cancelled")
	// ErrSelfSubscription rejects a subscription whose payer is the plan's own
	// merchant. Both addresses are fixed once the plan exists (the merchant is
	// the plan creator's own address) and the payer is the transaction signer,
	// so the outcome is a pure function of the transaction and committed
	// state: core/node.go's classifyProposalError treats it as permanently
	// unexecutable.
	ErrSelfSubscription = errors.New("subscriptions: payer cannot subscribe to its own plan")
)
