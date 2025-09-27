package reputation

import (
	"errors"
	"fmt"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// storage abstracts the subset of state manager functionality required by the
// reputation ledger.
type storage interface {
	KVGet(key []byte, out interface{}) (bool, error)
	KVPut(key []byte, value interface{}) error
}

var skillVerificationPrefix = []byte("reputation/skill/")

func skillVerificationKey(subject [20]byte, skill string, verifier [20]byte) []byte {
	normalized := strings.ToLower(strings.TrimSpace(skill))
	if normalized == "" {
		return nil
	}
	digest := ethcrypto.Keccak256([]byte(normalized))
	return []byte(fmt.Sprintf("%s%x/%x/%x", skillVerificationPrefix, subject, digest, verifier))
}

// Ledger persists skill verifications issued by authorised verifiers.
type Ledger struct {
	store storage
}

// NewLedger constructs a ledger bound to the provided storage backend.
func NewLedger(store storage) *Ledger {
	return &Ledger{store: store}
}

// Put stores the verification record, overwriting any previous attestation from
// the same verifier for the subject and skill combination.
func (l *Ledger) Put(verification *SkillVerification) error {
	if l == nil {
		return errors.New("reputation: ledger not initialised")
	}
	if l.store == nil {
		return errors.New("reputation: storage unavailable")
	}
	if verification == nil {
		return errors.New("reputation: verification required")
	}
	sanitized := *verification
	sanitized.Skill = strings.TrimSpace(sanitized.Skill)
	if err := sanitized.Validate(); err != nil {
		return err
	}
	if sanitized.IssuedAt < 0 {
		return fmt.Errorf("reputation: issuedAt must be positive")
	}
	key := skillVerificationKey(sanitized.Subject, sanitized.Skill, sanitized.Verifier)
	if key == nil {
		return errors.New("reputation: skill required")
	}
	stored := storedSkillVerification{
		Subject:  sanitized.Subject,
		Skill:    sanitized.Skill,
		Verifier: sanitized.Verifier,
		IssuedAt: uint64(sanitized.IssuedAt),
	}
	if sanitized.ExpiresAt > 0 {
		stored.ExpiresAt = uint64(sanitized.ExpiresAt)
	}
	return l.store.KVPut(key, &stored)
}

// Get retrieves the verification record issued by the specified verifier for the
// subject and skill combination.
func (l *Ledger) Get(subject [20]byte, skill string, verifier [20]byte) (*SkillVerification, bool, error) {
	if l == nil {
		return nil, false, errors.New("reputation: ledger not initialised")
	}
	if l.store == nil {
		return nil, false, errors.New("reputation: storage unavailable")
	}
	key := skillVerificationKey(subject, skill, verifier)
	if key == nil {
		return nil, false, errors.New("reputation: skill required")
	}
	var stored storedSkillVerification
	ok, err := l.store.KVGet(key, &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	verification := &SkillVerification{
		Subject:  stored.Subject,
		Skill:    stored.Skill,
		Verifier: stored.Verifier,
		IssuedAt: int64(stored.IssuedAt),
	}
	if stored.ExpiresAt > 0 {
		verification.ExpiresAt = int64(stored.ExpiresAt)
	}
	return verification, true, nil
}

type storedSkillVerification struct {
	Subject   [20]byte
	Skill     string
	Verifier  [20]byte
	IssuedAt  uint64
	ExpiresAt uint64
}
