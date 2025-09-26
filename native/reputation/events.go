package reputation

import (
	"encoding/hex"
	"strconv"

	"nhbchain/core/types"
)

const (
	// EventTypeSkillVerified is emitted when a verifier attests to a skill.
	EventTypeSkillVerified = "reputation.skillVerified"
)

// NewSkillVerifiedEvent returns the canonical event payload for a skill
// verification.
func NewSkillVerifiedEvent(v *SkillVerification) *types.Event {
	attrs := make(map[string]string)
	if v == nil {
		return &types.Event{Type: EventTypeSkillVerified, Attributes: attrs}
	}
	if err := v.Validate(); err != nil {
		return &types.Event{Type: EventTypeSkillVerified, Attributes: attrs}
	}
	attrs["subject"] = hex.EncodeToString(v.Subject[:])
	attrs["verifier"] = hex.EncodeToString(v.Verifier[:])
	attrs["skill"] = v.Skill
	attrs["issuedAt"] = strconv.FormatInt(v.IssuedAt, 10)
	if v.ExpiresAt > 0 {
		attrs["expiresAt"] = strconv.FormatInt(v.ExpiresAt, 10)
	}
	return &types.Event{Type: EventTypeSkillVerified, Attributes: attrs}
}
