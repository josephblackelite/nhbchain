package payoutd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	bbolt "go.etcd.io/bbolt"
)

var payoutHoldBucket = []byte("payout_holds")

// HoldScope captures the entity dimension on which a risk hold applies.
type HoldScope string

const (
	HoldScopeAccount     HoldScope = "account"
	HoldScopeDestination HoldScope = "destination"
	HoldScopePartner     HoldScope = "partner"
	HoldScopeRegion      HoldScope = "region"
)

// HoldRecord stores an operator-managed risk or compliance hold.
type HoldRecord struct {
	ID           string     `json:"id"`
	Scope        HoldScope  `json:"scope"`
	Value        string     `json:"value"`
	Reason       string     `json:"reason"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	ReleasedBy   string     `json:"released_by,omitempty"`
	ReleaseNotes string     `json:"release_notes,omitempty"`
	ReleasedAt   *time.Time `json:"released_at,omitempty"`
	Active       bool       `json:"active"`
}

// HoldStore persists compliance holds.
type HoldStore interface {
	Put(HoldRecord) error
	Get(id string) (HoldRecord, bool, error)
	List() ([]HoldRecord, error)
	Close() error
}

// BoltHoldStore persists hold records in BoltDB.
type BoltHoldStore struct {
	db *bbolt.DB
}

// NewBoltHoldStore opens or creates the payout hold store.
func NewBoltHoldStore(path string) (*BoltHoldStore, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("hold store path required")
	}
	if err := os.MkdirAll(filepath.Dir(trimmed), 0o755); err != nil {
		return nil, fmt.Errorf("create hold store dir: %w", err)
	}
	db, err := bbolt.Open(trimmed, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open hold store: %w", err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(payoutHoldBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init hold store: %w", err)
	}
	return &BoltHoldStore{db: db}, nil
}

// Put stores or replaces a hold record.
func (s *BoltHoldStore) Put(record HoldRecord) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("hold store not initialised")
	}
	record.ID = strings.TrimSpace(record.ID)
	record.Value = strings.TrimSpace(record.Value)
	if record.ID == "" || record.Value == "" {
		return fmt.Errorf("hold id and value required")
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal hold: %w", err)
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(payoutHoldBucket).Put([]byte(record.ID), payload)
	})
}

// Get retrieves a hold by id.
func (s *BoltHoldStore) Get(id string) (HoldRecord, bool, error) {
	if s == nil || s.db == nil {
		return HoldRecord{}, false, fmt.Errorf("hold store not initialised")
	}
	var record HoldRecord
	err := s.db.View(func(tx *bbolt.Tx) error {
		value := tx.Bucket(payoutHoldBucket).Get([]byte(strings.TrimSpace(id)))
		if value == nil {
			return nil
		}
		return json.Unmarshal(value, &record)
	})
	if err != nil {
		return HoldRecord{}, false, err
	}
	if record.ID == "" {
		return HoldRecord{}, false, nil
	}
	return record, true, nil
}

// List returns hold records ordered newest-first.
func (s *BoltHoldStore) List() ([]HoldRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("hold store not initialised")
	}
	items := make([]HoldRecord, 0)
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(payoutHoldBucket).ForEach(func(_, value []byte) error {
			var record HoldRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return err
			}
			items = append(items, record)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].UpdatedAt.After(items[j].UpdatedAt)
	})
	return items, nil
}

// Close releases the underlying database.
func (s *BoltHoldStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
