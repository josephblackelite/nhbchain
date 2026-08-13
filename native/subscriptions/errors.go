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
)
