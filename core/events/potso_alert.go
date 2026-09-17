package events

import "nhbchain/core/types"

const (
	TypePotsoAlertInvariantViolation = "potso.alert.invariant_violation"
)

type PotsoInvariantViolation struct {
	Kind    string
	Details string
}

func (PotsoInvariantViolation) EventType() string { return TypePotsoAlertInvariantViolation }

func (a PotsoInvariantViolation) Event() *types.Event {
	attrs := map[string]string{}
	if a.Kind != "" {
		attrs["kind"] = a.Kind
	}
	if a.Details != "" {
		attrs["details"] = a.Details
	}
	return &types.Event{Type: TypePotsoAlertInvariantViolation, Attributes: attrs}
}
