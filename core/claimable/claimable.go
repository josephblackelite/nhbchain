package claimable

import (
	"errors"
	"math/big"
)

type ClaimStatus uint8

const (
	ClaimStatusInit ClaimStatus = iota
	ClaimStatusClaimed
	ClaimStatusCancelled
	ClaimStatusExpired
)

func (s ClaimStatus) Valid() bool {
	switch s {
	case ClaimStatusInit, ClaimStatusClaimed, ClaimStatusCancelled, ClaimStatusExpired:
		return true
	default:
		return false
	}
}

func (s ClaimStatus) String() string {
	switch s {
	case ClaimStatusInit:
		return "init"
	case ClaimStatusClaimed:
		return "claimed"
	case ClaimStatusCancelled:
		return "cancelled"
	case ClaimStatusExpired:
		return "expired"
	default:
		return "unknown"
	}
}

var (
	ErrNotFound          = errors.New("claimable: not found")
	ErrInvalidToken      = errors.New("claimable: invalid token")
	ErrInvalidAmount     = errors.New("claimable: amount must be positive")
	ErrInvalidPreimage   = errors.New("claimable: invalid preimage")
	ErrUnauthorized      = errors.New("claimable: unauthorized")
	ErrDeadlineExceeded  = errors.New("claimable: deadline exceeded")
	ErrNotExpired        = errors.New("claimable: not expired")
	ErrInvalidState      = errors.New("claimable: invalid state")
	ErrInsufficientFunds = errors.New("claimable: insufficient funds")
)

type Claimable struct {
	ID            [32]byte
	Payer         [20]byte
	Token         string
	Amount        *big.Int
	HashLock      [32]byte
	RecipientHint [32]byte
	Deadline      int64
	CreatedAt     int64
	Status        ClaimStatus
}

func (c *Claimable) Clone() *Claimable {
	if c == nil {
		return nil
	}
	out := *c
	if c.Amount != nil {
		out.Amount = new(big.Int).Set(c.Amount)
	} else {
		out.Amount = big.NewInt(0)
	}
	return &out
}
