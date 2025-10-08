package engine

import "errors"

var (
	ErrNotFound               = errors.New("lending: not found")
	ErrInsufficientCollateral = errors.New("lending: insufficient collateral")
	ErrPaused                 = errors.New("lending: operation paused")
	ErrInvalidAmount          = errors.New("lending: invalid amount")
	ErrUnauthorized           = errors.New("lending: unauthorized")
	ErrInternal               = errors.New("lending: internal error")
)
