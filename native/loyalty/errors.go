package loyalty

import "errors"

var (
	ErrNilProgram         = errors.New("loyalty: nil program")
	ErrUnauthorized       = errors.New("loyalty: unauthorized")
	ErrProgramExists      = errors.New("loyalty: program already exists")
	ErrProgramNotFound    = errors.New("loyalty: program not found")
	ErrInvalidProgram     = errors.New("loyalty: invalid program")
	ErrImmutableField     = errors.New("loyalty: immutable field")
	ErrTokenNotRegistered = errors.New("loyalty: token not registered")
	ErrAccrualBpsTooHigh  = errors.New("loyalty: accrual bps too high")
	ErrBusinessNotFound   = errors.New("loyalty: business not found")
	ErrInvalidBusiness    = errors.New("loyalty: invalid business")
	ErrPaymasterConflict  = errors.New("loyalty: paymaster already assigned")
	ErrMerchantAssigned   = errors.New("loyalty: merchant already assigned")
	ErrMerchantNotFound   = errors.New("loyalty: merchant not found")
)
