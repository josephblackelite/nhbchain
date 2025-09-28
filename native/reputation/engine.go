package reputation

// Engine wires higher-level operations against the ledger abstraction. It wraps
// the persistence layer to provide a convenient entry point for modules that
// need to issue, query or revoke attestations without re-implementing storage
// concerns.
type Engine struct {
	ledger *Ledger
}

// NewEngine constructs an engine backed by the provided storage backend.
func NewEngine(store storage) *Engine {
	if store == nil {
		return &Engine{ledger: nil}
	}
	return &Engine{ledger: NewLedger(store)}
}

// SetNowFunc overrides the wall clock used by the underlying ledger.
func (e *Engine) SetNowFunc(now func() int64) {
	if e == nil || e.ledger == nil {
		return
	}
	e.ledger.SetNowFunc(now)
}

// Verify stores the supplied attestation. The sanitized verification payload is
// returned for convenience.
func (e *Engine) Verify(v *SkillVerification) (*SkillVerification, error) {
	if e == nil || e.ledger == nil {
		return nil, ErrAttestationNotFound
	}
	if err := e.ledger.Put(v); err != nil {
		return nil, err
	}
	return v, nil
}

// Get fetches an attestation issued by verifier for subject/skill. Expired or
// revoked attestations are filtered at the ledger layer and will return ok=false.
func (e *Engine) Get(subject [20]byte, skill string, verifier [20]byte) (*SkillVerification, bool, error) {
	if e == nil || e.ledger == nil {
		return nil, false, ErrAttestationNotFound
	}
	return e.ledger.Get(subject, skill, verifier)
}

// Revoke marks the attestation identified by id as revoked, delegating to the
// ledger for enforcement and auditing data.
func (e *Engine) Revoke(id [32]byte, verifier [20]byte, reason string) (*Revocation, error) {
	if e == nil || e.ledger == nil {
		return nil, ErrAttestationNotFound
	}
	return e.ledger.Revoke(id, verifier, reason)
}
