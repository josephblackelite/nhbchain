package reputation

import (
	"encoding/hex"
	"strconv"
	"strings"

	"nhbchain/core/types"
)

const (
	// EventTypeSkillVerified is emitted when a verifier attests to a skill.
	EventTypeSkillVerified = "reputation.skillVerified"
	// EventTypeSkillRevoked is emitted when a verifier revokes a prior attestation.
	EventTypeSkillRevoked = "reputation.skillRevoked"
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
	if id, err := AttestationID(v); err == nil {
		attrs["attestationId"] = hex.EncodeToString(id[:])
	}
	return &types.Event{Type: EventTypeSkillVerified, Attributes: attrs}
}

// NewSkillRevokedEvent constructs the canonical payload for a revocation audit
// event.
func NewSkillRevokedEvent(r *Revocation) *types.Event {
	attrs := make(map[string]string)
	if r == nil {
		return &types.Event{Type: EventTypeSkillRevoked, Attributes: attrs}
	}
	if err := r.Validate(); err != nil {
		return &types.Event{Type: EventTypeSkillRevoked, Attributes: attrs}
	}
	attrs["attestationId"] = hex.EncodeToString(r.AttestationID[:])
	attrs["subject"] = hex.EncodeToString(r.Subject[:])
	attrs["verifier"] = hex.EncodeToString(r.Verifier[:])
	attrs["skill"] = r.Skill
	attrs["revokedAt"] = strconv.FormatInt(r.RevokedAt, 10)
	reason := strings.TrimSpace(r.Reason)
	if reason != "" {
		attrs["reason"] = reason
	}
	return &types.Event{Type: EventTypeSkillRevoked, Attributes: attrs}
}
