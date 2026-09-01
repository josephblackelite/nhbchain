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

// RecipientKind records what RecipientHint actually is, so claim-time
// authorization can apply the right rule for each. The two are NOT
// interchangeable: an alias-derived hint is public by construction
// (keccak256 of a lowercased username anyone can compute), so knowing it
// proves nothing about who the payer intended to pay -- only alias
// ownership does. An opaque hint is a real secret the payer is expected to
// share with the intended recipient out of band (e.g. by email), so
// knowledge of it IS the intended proof, exactly like a classic HTLC.
// Conflating the two (treating a public alias hash as if it were a secret)
// was NHB-TRIAGE-C6: any address that merely knew a target's username could
// drain a claimable meant for them, with no ownership check at all.
type RecipientKind uint8

const (
	// RecipientKindNone: no recipient hint, or an opaque hint meant to be a
	// genuine bearer secret. Authorized by the hashlock alone -- unchanged,
	// intentional bearer-instrument behavior.
	RecipientKindNone RecipientKind = 0
	// RecipientKindAlias: RecipientHint is identity.DeriveAliasID(alias), a
	// publicly-computable pointer, not a secret. Authorization MUST come
	// from the claimer owning that alias, checked at claim time (not
	// creation time, since the alias may not be registered yet) -- the
	// hashlock/preimage check on this kind of record is not, by itself, a
	// security boundary.
	RecipientKindAlias RecipientKind = 1
)

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
	RecipientKind RecipientKind
	Deadline      int64
	CreatedAt     int64
	Nonce         uint64
	ExpiresAt     int64
	ChainID       string
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
	out.ChainID = c.ChainID
	return &out
}
