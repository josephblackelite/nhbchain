package escrow

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
)

// MilestoneStatus represents the lifecycle of a milestone project.
type MilestoneStatus uint8

const (
	// MilestoneStatusDraft marks projects that have been created but have not
	// received any funding yet.
	MilestoneStatusDraft MilestoneStatus = iota
	// MilestoneStatusActive marks projects that have funding locked for one
	// or more legs.
	MilestoneStatusActive
	// MilestoneStatusCompleted marks projects that have released all funds.
	MilestoneStatusCompleted
	// MilestoneStatusCancelled marks projects that were cancelled before all
	// legs were released. Cancelled projects may still contain historical
	// payouts for auditability but accept no further funding events.
	MilestoneStatusCancelled
)

// MilestoneLegStatus represents the state of an individual leg.
type MilestoneLegStatus uint8

const (
	// MilestoneLegPending indicates a leg is awaiting funding.
	MilestoneLegPending MilestoneLegStatus = iota
	// MilestoneLegFunded indicates funds have been deposited for the leg and
	// are awaiting release.
	MilestoneLegFunded
	// MilestoneLegReleased indicates the leg has been paid out to the
	// recipient.
	MilestoneLegReleased
	// MilestoneLegCancelled indicates the leg was explicitly cancelled prior
	// to release.
	MilestoneLegCancelled
	// MilestoneLegExpired indicates the leg was not released before the
	// deadline elapsed and requires dispute handling off-chain.
	MilestoneLegExpired
)

// MilestoneLegType describes the semantic meaning of the leg within a project.
type MilestoneLegType uint8

const (
	// MilestoneLegTypeUnspecified prevents zero-value legs from being
	// persisted unintentionally.
	MilestoneLegTypeUnspecified MilestoneLegType = iota
	// MilestoneLegTypeDeliverable represents a discrete piece of work such as
	// a feature or asset.
	MilestoneLegTypeDeliverable
	// MilestoneLegTypeTimebox represents a recurring or time-based funding
	// schedule such as retainers or subscriptions.
	MilestoneLegTypeTimebox
)

// ErrInvalidMilestoneLeg describes malformed milestone leg definitions.
var ErrInvalidMilestoneLeg = errors.New("escrow: invalid milestone leg")

// MilestoneLeg captures a single funding leg for a project.
type MilestoneLeg struct {
	ID          uint64
	Type        MilestoneLegType
	Title       string
	Description string
	Token       string
	Amount      *big.Int
	Deadline    int64
	Status      MilestoneLegStatus
	FundedAt    int64
	ReleasedAt  int64
	CancelledAt int64
}

// Clone returns a deep copy of the leg to avoid callers mutating shared state.
func (l *MilestoneLeg) Clone() *MilestoneLeg {
	if l == nil {
		return nil
	}
	clone := *l
	if l.Amount != nil {
		clone.Amount = new(big.Int).Set(l.Amount)
	}
	return &clone
}

// Validate ensures the leg fields are sane prior to persistence.
func (l *MilestoneLeg) Validate() error {
	if l == nil {
		return fmt.Errorf("%w: leg must not be nil", ErrInvalidMilestoneLeg)
	}
	if l.Type == MilestoneLegTypeUnspecified {
		return fmt.Errorf("%w: type required", ErrInvalidMilestoneLeg)
	}
	if strings.TrimSpace(l.Title) == "" {
		return fmt.Errorf("%w: title required", ErrInvalidMilestoneLeg)
	}
	if l.Amount == nil || l.Amount.Sign() <= 0 {
		return fmt.Errorf("%w: amount must be positive", ErrInvalidMilestoneLeg)
	}
	if l.Deadline <= 0 {
		return fmt.Errorf("%w: deadline must be > 0", ErrInvalidMilestoneLeg)
	}
	return nil
}

// MilestoneProject aggregates milestone legs under a shared project ID.
type MilestoneProject struct {
	ID           [32]byte
	Payer        [20]byte
	Payee        [20]byte
	RealmID      string
	CreatedAt    int64
	UpdatedAt    int64
	Status       MilestoneStatus
	Legs         []*MilestoneLeg
	Metadata     []byte
	Subscription *MilestoneSubscription
}

// Clone returns a deep copy of the project.
func (p *MilestoneProject) Clone() *MilestoneProject {
	if p == nil {
		return nil
	}
	clone := *p
	if len(p.Legs) > 0 {
		clone.Legs = make([]*MilestoneLeg, len(p.Legs))
		for i, leg := range p.Legs {
			clone.Legs[i] = leg.Clone()
		}
	}
	if len(p.Metadata) > 0 {
		clone.Metadata = make([]byte, len(p.Metadata))
		copy(clone.Metadata, p.Metadata)
	}
	clone.Subscription = p.Subscription.Clone()
	return &clone
}

// FindLeg returns a pointer to the leg with the supplied identifier.
func (p *MilestoneProject) FindLeg(id uint64) *MilestoneLeg {
	if p == nil {
		return nil
	}
	for _, leg := range p.Legs {
		if leg != nil && leg.ID == id {
			return leg
		}
	}
	return nil
}

// MilestoneSubscription defines an optional recurring payment contract for a
// project. Time-boxed legs can be generated from subscription checkpoints.
type MilestoneSubscription struct {
	IntervalSeconds int64
	NextReleaseAt   int64
	Active          bool
}

// Clone returns a copy safe for modification.
func (s *MilestoneSubscription) Clone() *MilestoneSubscription {
	if s == nil {
		return nil
	}
	clone := *s
	return &clone
}

// Validate ensures the subscription fields are sensible.
func (s *MilestoneSubscription) Validate() error {
	if s == nil {
		return nil
	}
	if s.IntervalSeconds <= 0 {
		return errors.New("escrow: subscription interval must be positive")
	}
	if s.NextReleaseAt <= 0 {
		return errors.New("escrow: subscription next release must be positive")
	}
	return nil
}

// RequiresFunding reports whether any legs are awaiting deposits.
func (p *MilestoneProject) RequiresFunding() bool {
	if p == nil {
		return false
	}
	for _, leg := range p.Legs {
		if leg != nil && leg.Status == MilestoneLegPending {
			return true
		}
	}
	return false
}

// NextDueLeg returns the earliest funded leg that has reached its deadline and
// has not yet been released.
func (p *MilestoneProject) NextDueLeg(now int64) *MilestoneLeg {
	if p == nil {
		return nil
	}
	var selected *MilestoneLeg
	for _, leg := range p.Legs {
		if leg == nil {
			continue
		}
		if leg.Status != MilestoneLegFunded {
			continue
		}
		if leg.Deadline > now {
			continue
		}
		if selected == nil || leg.Deadline < selected.Deadline {
			selected = leg
		}
	}
	return selected
}

// SanitizeMilestoneProject clones the supplied project and validates each leg to
// guarantee deterministic event payloads.
func SanitizeMilestoneProject(p *MilestoneProject) (*MilestoneProject, error) {
	if p == nil {
		return nil, errors.New("escrow: milestone project nil")
	}
	clone := p.Clone()
	for _, leg := range clone.Legs {
		if err := leg.Validate(); err != nil {
			return nil, err
		}
	}
	if err := clone.Subscription.Validate(); err != nil {
		return nil, err
	}
	return clone, nil
}
