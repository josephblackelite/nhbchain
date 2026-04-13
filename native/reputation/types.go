package reputation

import (
	"errors"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// SkillVerification captures a statement that a verifier has attested to a
// subject's proficiency in a skill category.
type SkillVerification struct {
	Subject   [20]byte
	Skill     string
	Verifier  [20]byte
	IssuedAt  int64
	ExpiresAt int64
}

// Validate ensures the verification payload is well formed.
func (s *SkillVerification) Validate() error {
	if s == nil {
		return errors.New("reputation: verification nil")
	}
	if len(strings.TrimSpace(s.Skill)) == 0 {
		return errors.New("reputation: skill required")
	}
	if s.Subject == ([20]byte{}) {
		return errors.New("reputation: subject required")
	}
	if s.Verifier == ([20]byte{}) {
		return errors.New("reputation: verifier required")
	}
	if s.IssuedAt <= 0 {
		return errors.New("reputation: issuedAt must be positive")
	}
	if s.ExpiresAt > 0 && s.ExpiresAt <= s.IssuedAt {
		return errors.New("reputation: expiresAt must be after issuedAt")
	}
	return nil
}

// AttestationID derives the stable identifier for a verification record.
func AttestationID(v *SkillVerification) ([32]byte, error) {
	if v == nil {
		return [32]byte{}, errors.New("reputation: verification nil")
	}
	return ComputeAttestationID(v.Subject, v.Skill, v.Verifier)
}

// ComputeAttestationID derives the deterministic attestation identifier based on
// the subject, normalized skill name and verifier.
func ComputeAttestationID(subject [20]byte, skill string, verifier [20]byte) ([32]byte, error) {
	trimmed := strings.TrimSpace(skill)
	if trimmed == "" {
		return [32]byte{}, errors.New("reputation: skill required")
	}
	digest := ethcrypto.Keccak256([]byte(strings.ToLower(trimmed)))
	hash := ethcrypto.Keccak256(subject[:], digest, verifier[:])
	var id [32]byte
	copy(id[:], hash)
	return id, nil
}

// Revocation captures the audit trail emitted when an attestation is revoked.
type Revocation struct {
	AttestationID [32]byte
	Subject       [20]byte
	Verifier      [20]byte
	Skill         string
	RevokedAt     int64
	Reason        string
}

// Validate ensures the revocation payload is well formed before emission.
func (r *Revocation) Validate() error {
	if r == nil {
		return errors.New("reputation: revocation nil")
	}
	if r.AttestationID == ([32]byte{}) {
		return errors.New("reputation: attestation id required")
	}
	if r.Subject == ([20]byte{}) {
		return errors.New("reputation: subject required")
	}
	if r.Verifier == ([20]byte{}) {
		return errors.New("reputation: verifier required")
	}
	if len(strings.TrimSpace(r.Skill)) == 0 {
		return errors.New("reputation: skill required")
	}
	if r.RevokedAt <= 0 {
		return errors.New("reputation: revokedAt must be positive")
	}
	return nil
}
