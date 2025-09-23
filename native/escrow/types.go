package escrow

import (
	"fmt"
	"math/big"
	"strings"
)

// EscrowStatus represents the lifecycle states supported by the hardened
// escrow engine.
type EscrowStatus uint8

const (
	EscrowInit EscrowStatus = iota
	EscrowFunded
	EscrowReleased
	EscrowRefunded
	EscrowExpired
	EscrowDisputed
)

// Escrow captures the immutable metadata and runtime status of a single escrow
// agreement managed by the native engine. The identifier is the keccak256 hash
// of the payer, payee and a caller-supplied nonce, ensuring deterministic IDs
// without storing the nonce on-chain.
type Escrow struct {
	ID        [32]byte
	Payer     [20]byte
	Payee     [20]byte
	Mediator  [20]byte
	Token     string
	Amount    *big.Int
	FeeBps    uint32
	Deadline  int64
	CreatedAt int64
	MetaHash  [32]byte
	Status    EscrowStatus
}

// Clone returns a deep copy of the escrow object so callers can safely mutate
// the copy without affecting the stored instance.
func (e *Escrow) Clone() *Escrow {
	if e == nil {
		return nil
	}
	clone := *e
	if e.Amount != nil {
		clone.Amount = new(big.Int).Set(e.Amount)
	} else {
		clone.Amount = big.NewInt(0)
	}
	return &clone
}

// Valid reports whether the status value is within the supported range.
func (s EscrowStatus) Valid() bool {
	switch s {
	case EscrowInit, EscrowFunded, EscrowReleased, EscrowRefunded, EscrowExpired, EscrowDisputed:
		return true
	default:
		return false
	}
}

// NormalizeToken ensures the provided token symbol matches a supported value
// ("NHB" or "ZNHB") and returns the canonical uppercase form.
func NormalizeToken(symbol string) (string, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(symbol))
	switch trimmed {
	case "NHB", "ZNHB":
		return trimmed, nil
	default:
		return "", fmt.Errorf("unsupported escrow token: %s", symbol)
	}
}

// SanitizeEscrow validates and normalises the supplied escrow definition,
// returning a cloned instance with canonical token casing and a non-nil amount
// field. The function does not mutate the original value.
func SanitizeEscrow(e *Escrow) (*Escrow, error) {
	if e == nil {
		return nil, fmt.Errorf("nil escrow")
	}
	clone := e.Clone()
	token, err := NormalizeToken(clone.Token)
	if err != nil {
		return nil, err
	}
	clone.Token = token
	if clone.Amount == nil {
		clone.Amount = big.NewInt(0)
	}
	if clone.Amount.Sign() < 0 {
		return nil, fmt.Errorf("escrow amount must be non-negative")
	}
	if clone.FeeBps > 10_000 {
		return nil, fmt.Errorf("escrow fee bps out of range: %d", clone.FeeBps)
	}
	if !clone.Status.Valid() {
		return nil, fmt.Errorf("invalid escrow status: %d", clone.Status)
	}
	return clone, nil
}
