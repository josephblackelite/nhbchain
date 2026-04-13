package reputation

import (
	"errors"
	"fmt"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// storage abstracts the subset of state manager functionality required by the
// reputation ledger.
type storage interface {
	KVGet(key []byte, out interface{}) (bool, error)
	KVPut(key []byte, value interface{}) error
}

var (
	skillVerificationPrefix    = []byte("reputation/skill/")
	skillVerificationIndexPref = []byte("reputation/attestation/")
)

func skillVerificationKey(subject [20]byte, skill string, verifier [20]byte) []byte {
	normalized := strings.ToLower(strings.TrimSpace(skill))
	if normalized == "" {
		return nil
	}
	digest := ethcrypto.Keccak256([]byte(normalized))
	return []byte(fmt.Sprintf("%s%x/%x/%x", skillVerificationPrefix, subject, digest, verifier))
}

func skillVerificationIndexKey(id [32]byte) []byte {
	return []byte(fmt.Sprintf("%s%x", skillVerificationIndexPref, id))
}

type skillVerificationIndex struct {
	Subject  [20]byte
	Skill    string
	Verifier [20]byte
}

var (
	// ErrAttestationNotFound marks missing attestation records.
	ErrAttestationNotFound = errors.New("reputation: attestation not found")
	// ErrAttestationRevoked is returned when attempting to revoke an already
	// revoked attestation.
	ErrAttestationRevoked = errors.New("reputation: attestation revoked")
	// ErrRevocationUnauthorized marks revocation attempts from accounts that
	// did not issue the original attestation.
	ErrRevocationUnauthorized = errors.New("reputation: revocation unauthorized")
)

// Ledger persists skill verifications issued by authorised verifiers.
type Ledger struct {
	store storage
	nowFn func() int64
}

// NewLedger constructs a ledger bound to the provided storage backend.
func NewLedger(store storage) *Ledger {
	return &Ledger{
		store: store,
		nowFn: func() int64 { return time.Now().Unix() },
	}
}

// SetNowFunc overrides the wall clock used for expiry checks. Primarily
// leveraged in tests to provide deterministic timestamps.
func (l *Ledger) SetNowFunc(now func() int64) {
	if l == nil {
		return
	}
	if now == nil {
		l.nowFn = func() int64 { return time.Now().Unix() }
		return
	}
	l.nowFn = now
}

func (l *Ledger) now() int64 {
	if l == nil || l.nowFn == nil {
		return time.Now().Unix()
	}
	return l.nowFn()
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
	attestationID, err := AttestationID(&sanitized)
	if err != nil {
		return err
	}
	stored := storedSkillVerification{
		ID:       attestationID,
		Subject:  sanitized.Subject,
		Skill:    sanitized.Skill,
		Verifier: sanitized.Verifier,
		IssuedAt: uint64(sanitized.IssuedAt),
	}
	if sanitized.ExpiresAt > 0 {
		stored.ExpiresAt = uint64(sanitized.ExpiresAt)
	}
	if err := l.store.KVPut(key, &stored); err != nil {
		return err
	}
	index := &skillVerificationIndex{
		Subject:  sanitized.Subject,
		Skill:    sanitized.Skill,
		Verifier: sanitized.Verifier,
	}
	return l.store.KVPut(skillVerificationIndexKey(attestationID), index)
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
	if stored.RevokedAt > 0 {
		return nil, false, nil
	}
	if stored.ExpiresAt > 0 && l.now() >= int64(stored.ExpiresAt) {
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

// Revoke marks the attestation identified by id as revoked. Only the original
// verifier may revoke their attestations.
func (l *Ledger) Revoke(id [32]byte, verifier [20]byte, reason string) (*Revocation, error) {
	if l == nil {
		return nil, errors.New("reputation: ledger not initialised")
	}
	if l.store == nil {
		return nil, errors.New("reputation: storage unavailable")
	}
	if id == ([32]byte{}) {
		return nil, errors.New("reputation: attestation id required")
	}
	trimmedReason := strings.TrimSpace(reason)
	var index skillVerificationIndex
	ok, err := l.store.KVGet(skillVerificationIndexKey(id), &index)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrAttestationNotFound
	}
	key := skillVerificationKey(index.Subject, index.Skill, index.Verifier)
	if key == nil {
		return nil, ErrAttestationNotFound
	}
	var stored storedSkillVerification
	found, err := l.store.KVGet(key, &stored)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrAttestationNotFound
	}
	if stored.ID != ([32]byte{}) && stored.ID != id {
		return nil, ErrAttestationNotFound
	}
	if stored.Verifier != verifier {
		return nil, ErrRevocationUnauthorized
	}
	if stored.RevokedAt > 0 {
		return nil, ErrAttestationRevoked
	}
	revokedAt := l.now()
	if revokedAt < 0 {
		revokedAt = time.Now().Unix()
	}
	stored.RevokedAt = uint64(revokedAt)
	stored.RevokedBy = verifier
	stored.RevocationReason = trimmedReason
	if err := l.store.KVPut(key, &stored); err != nil {
		return nil, err
	}
	revocation := &Revocation{
		AttestationID: id,
		Subject:       stored.Subject,
		Verifier:      stored.Verifier,
		Skill:         stored.Skill,
		RevokedAt:     revokedAt,
		Reason:        trimmedReason,
	}
	return revocation, nil
}

type storedSkillVerification struct {
	ID               [32]byte
	Subject          [20]byte
	Skill            string
	Verifier         [20]byte
	IssuedAt         uint64
	ExpiresAt        uint64
	RevokedAt        uint64
	RevokedBy        [20]byte
	RevocationReason string
}
