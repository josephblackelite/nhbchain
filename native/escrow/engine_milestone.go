package escrow

import (
	"errors"
	"fmt"
	"time"
)

// ErrMilestoneNotFound is returned when a leg cannot be located on the project.
var ErrMilestoneNotFound = errors.New("escrow: milestone leg not found")

// ErrMilestoneInvalidTransition marks invalid status transitions.
var ErrMilestoneInvalidTransition = errors.New("escrow: invalid milestone transition")

// MilestoneEngine orchestrates state transitions for milestone projects.
type MilestoneEngine struct {
	now func() time.Time
}

// NewMilestoneEngine initialises a milestone engine using the supplied clock.
func NewMilestoneEngine(now func() time.Time) *MilestoneEngine {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &MilestoneEngine{now: now}
}

// CreateProject performs basic validation on the supplied project prior to
// persistence.
func (e *MilestoneEngine) CreateProject(project *MilestoneProject) error {
	if project == nil {
		return errors.New("escrow: project must not be nil")
	}
	if project.Subscription != nil {
		if err := project.Subscription.Validate(); err != nil {
			return err
		}
	}
	for _, leg := range project.Legs {
		if err := leg.Validate(); err != nil {
			return err
		}
	}
	now := e.now().Unix()
	project.CreatedAt = now
	project.UpdatedAt = now
	project.Status = MilestoneStatusDraft
	return nil
}

// FundLeg marks a leg as funded and transitions the project into the active
// state when required.
func (e *MilestoneEngine) FundLeg(project *MilestoneProject, legID uint64) error {
	leg := project.FindLeg(legID)
	if leg == nil {
		return ErrMilestoneNotFound
	}
	if leg.Status != MilestoneLegPending {
		return fmt.Errorf("%w: leg %d not pending", ErrMilestoneInvalidTransition, legID)
	}
	now := e.now().Unix()
	leg.Status = MilestoneLegFunded
	leg.FundedAt = now
	project.UpdatedAt = now
	if project.Status == MilestoneStatusDraft {
		project.Status = MilestoneStatusActive
	}
	return nil
}

// ReleaseLeg releases funds to the payee.
func (e *MilestoneEngine) ReleaseLeg(project *MilestoneProject, legID uint64) error {
	leg := project.FindLeg(legID)
	if leg == nil {
		return ErrMilestoneNotFound
	}
	if leg.Status != MilestoneLegFunded {
		return fmt.Errorf("%w: leg %d must be funded", ErrMilestoneInvalidTransition, legID)
	}
	now := e.now().Unix()
	leg.Status = MilestoneLegReleased
	leg.ReleasedAt = now
	project.UpdatedAt = now
	if e.allLegsReleased(project) {
		project.Status = MilestoneStatusCompleted
	}
	return nil
}

// CancelLeg voids an individual leg and marks the project as cancelled if no
// other legs remain fundable.
func (e *MilestoneEngine) CancelLeg(project *MilestoneProject, legID uint64) error {
	leg := project.FindLeg(legID)
	if leg == nil {
		return ErrMilestoneNotFound
	}
	if leg.Status == MilestoneLegReleased {
		return fmt.Errorf("%w: leg %d already released", ErrMilestoneInvalidTransition, legID)
	}
	if leg.Status == MilestoneLegCancelled {
		return nil
	}
	now := e.now().Unix()
	leg.Status = MilestoneLegCancelled
	leg.CancelledAt = now
	project.UpdatedAt = now
	if !project.RequiresFunding() {
		project.Status = MilestoneStatusCancelled
	}
	return nil
}

// AdvanceSubscription increments the subscription schedule if active.
func (e *MilestoneEngine) AdvanceSubscription(project *MilestoneProject) {
	if project == nil || project.Subscription == nil {
		return
	}
	if !project.Subscription.Active {
		return
	}
	now := e.now().Unix()
	for project.Subscription.NextReleaseAt <= now {
		project.Subscription.NextReleaseAt += project.Subscription.IntervalSeconds
	}
	project.UpdatedAt = now
}

// ExpireDueLeg marks funded legs that have reached their deadline.
func (e *MilestoneEngine) ExpireDueLeg(project *MilestoneProject) *MilestoneLeg {
	if project == nil {
		return nil
	}
	now := e.now().Unix()
	leg := project.NextDueLeg(now)
	if leg == nil {
		return nil
	}
	leg.Status = MilestoneLegExpired
	project.UpdatedAt = now
	return leg
}

func (e *MilestoneEngine) allLegsReleased(project *MilestoneProject) bool {
	if project == nil {
		return false
	}
	for _, leg := range project.Legs {
		if leg == nil {
			continue
		}
		if leg.Status != MilestoneLegReleased && leg.Status != MilestoneLegCancelled {
			return false
		}
	}
	return true
}
