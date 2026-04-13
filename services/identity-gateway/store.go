package identitygateway

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	bucketEmails      = []byte("emails")
	bucketAliases     = []byte("aliases")
	bucketIdempotency = []byte("idempotency")

	// ErrNotFound is returned when a record does not exist.
	ErrNotFound = errors.New("record not found")
	// ErrAliasConflict is returned when attempting to bind an alias to a different email hash.
	ErrAliasConflict = errors.New("alias already bound to a different email")
	// ErrNotVerified is returned when binding is attempted before verification.
	ErrNotVerified = errors.New("email has not completed verification")
)

// Store persists verification state, bindings, and idempotency responses.
type Store struct {
	db *bolt.DB
}

// EmailRecord models verification metadata tracked per email hash.
type EmailRecord struct {
	EmailHash   string                  `json:"emailHash"`
	VerifiedAt  *time.Time              `json:"verifiedAt,omitempty"`
	CodeDigest  string                  `json:"codeDigest,omitempty"`
	CodeExpires *time.Time              `json:"codeExpires,omitempty"`
	Attempts    []time.Time             `json:"attempts,omitempty"`
	Bindings    map[string]AliasBinding `json:"bindings,omitempty"`
}

// AliasBinding captures opt-in alias linkage metadata.
type AliasBinding struct {
	AliasID      string    `json:"aliasId"`
	Consent      bool      `json:"consent"`
	PublicLookup bool      `json:"publicLookup"`
	LinkedAt     time.Time `json:"linkedAt"`
}

// IdempotencyRecord stores cached responses for an idempotency key.
type IdempotencyRecord struct {
	StatusCode int       `json:"statusCode"`
	Body       []byte    `json:"body"`
	StoredAt   time.Time `json:"storedAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

// aliasIndexRecord mirrors the alias bucket payload.
type aliasIndexRecord struct {
	EmailHash string    `json:"emailHash"`
	LinkedAt  time.Time `json:"linkedAt"`
}

// NewStore initialises (and migrates) the BoltDB-backed store.
func NewStore(path string, options *bolt.Options) (*Store, error) {
	if options == nil {
		options = &bolt.Options{Timeout: time.Second}
	} else if options.Timeout == 0 {
		options.Timeout = time.Second
	}
	db, err := bolt.Open(path, 0o600, options)
	if err != nil {
		return nil, err
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range [][]byte{bucketEmails, bucketAliases, bucketIdempotency} {
			if _, err := tx.CreateBucketIfNotExists(bucket); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close releases the underlying Bolt database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// MutateEmail applies a mutation to the email record. When createIfMissing is false and the record does not
// exist, ErrNotFound is returned.
func (s *Store) MutateEmail(emailHash string, createIfMissing bool, fn func(*EmailRecord) error) (EmailRecord, error) {
	var result EmailRecord
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketEmails)
		raw := bucket.Get([]byte(emailHash))
		var rec EmailRecord
		if raw == nil {
			if !createIfMissing {
				return ErrNotFound
			}
			rec = EmailRecord{
				EmailHash: emailHash,
				Bindings:  make(map[string]AliasBinding),
			}
		} else {
			if err := json.Unmarshal(raw, &rec); err != nil {
				return err
			}
			if rec.Bindings == nil {
				rec.Bindings = make(map[string]AliasBinding)
			}
		}
		if err := fn(&rec); err != nil {
			return err
		}
		encoded, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if err := bucket.Put([]byte(emailHash), encoded); err != nil {
			return err
		}
		result = rec
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return EmailRecord{}, ErrNotFound
	}
	return result, err
}

// GetEmail fetches a snapshot of the email record, if present.
func (s *Store) GetEmail(emailHash string) (EmailRecord, bool, error) {
	var record EmailRecord
	err := s.db.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(bucketEmails).Get([]byte(emailHash))
		if raw == nil {
			return nil
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if record.Bindings == nil {
			record.Bindings = make(map[string]AliasBinding)
		}
		return nil
	})
	if err != nil {
		return EmailRecord{}, false, err
	}
	if record.EmailHash == "" {
		return EmailRecord{}, false, nil
	}
	return record, true, nil
}

// BindAlias links the alias to the supplied email hash, ensuring prior verification.
func (s *Store) BindAlias(emailHash, aliasID string, consent bool, now time.Time) (AliasBinding, error) {
	aliasKey := []byte(strings.ToLower(aliasID))
	var binding AliasBinding
	err := s.db.Update(func(tx *bolt.Tx) error {
		aliasBucket := tx.Bucket(bucketAliases)
		if raw := aliasBucket.Get(aliasKey); raw != nil {
			var existing aliasIndexRecord
			if err := json.Unmarshal(raw, &existing); err != nil {
				return err
			}
			if existing.EmailHash != emailHash {
				return ErrAliasConflict
			}
		}
		emailBucket := tx.Bucket(bucketEmails)
		rawEmail := emailBucket.Get([]byte(emailHash))
		if rawEmail == nil {
			return ErrNotFound
		}
		var rec EmailRecord
		if err := json.Unmarshal(rawEmail, &rec); err != nil {
			return err
		}
		if rec.Bindings == nil {
			rec.Bindings = make(map[string]AliasBinding)
		}
		if rec.VerifiedAt == nil {
			return ErrNotVerified
		}
		existingBinding, ok := rec.Bindings[aliasID]
		if ok {
			// Preserve original linked timestamp if present.
			if !existingBinding.LinkedAt.IsZero() {
				binding.LinkedAt = existingBinding.LinkedAt
			}
		}
		if binding.LinkedAt.IsZero() {
			binding.LinkedAt = now.UTC()
		}
		binding.AliasID = aliasID
		binding.Consent = consent
		binding.PublicLookup = consent
		rec.Bindings[aliasID] = binding
		encodedEmail, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		if err := emailBucket.Put([]byte(emailHash), encodedEmail); err != nil {
			return err
		}
		aliasPayload, err := json.Marshal(aliasIndexRecord{EmailHash: emailHash, LinkedAt: binding.LinkedAt})
		if err != nil {
			return err
		}
		if err := aliasBucket.Put(aliasKey, aliasPayload); err != nil {
			return err
		}
		return nil
	})
	if errors.Is(err, ErrAliasConflict) {
		return AliasBinding{}, ErrAliasConflict
	}
	if errors.Is(err, ErrNotFound) {
		return AliasBinding{}, ErrNotFound
	}
	if errors.Is(err, ErrNotVerified) {
		return AliasBinding{}, ErrNotVerified
	}
	return binding, err
}

// GetIdempotency returns the cached response for a key when it has not expired.
func (s *Store) GetIdempotency(key string, now time.Time) (IdempotencyRecord, bool, error) {
	var record IdempotencyRecord
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(bucketIdempotency)
		raw := bucket.Get([]byte(key))
		if raw == nil {
			return nil
		}
		if err := json.Unmarshal(raw, &record); err != nil {
			return err
		}
		if now.After(record.ExpiresAt) {
			record = IdempotencyRecord{}
			return bucket.Delete([]byte(key))
		}
		return nil
	})
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	if record.StatusCode == 0 && len(record.Body) == 0 {
		return IdempotencyRecord{}, false, nil
	}
	return record, true, nil
}

// PutIdempotency stores the response envelope for the supplied key.
func (s *Store) PutIdempotency(key string, record IdempotencyRecord) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		payload, err := json.Marshal(record)
		if err != nil {
			return err
		}
		return tx.Bucket(bucketIdempotency).Put([]byte(key), payload)
	})
}
