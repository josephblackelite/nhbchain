package reputation

import "errors"

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
	if len(s.Skill) == 0 {
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
