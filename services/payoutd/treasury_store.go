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

var treasuryInstructionBucket = []byte("treasury_instructions")

// TreasuryInstructionStatus captures the lifecycle state of a treasury workflow item.
type TreasuryInstructionStatus string

const (
	TreasuryInstructionPending  TreasuryInstructionStatus = "pending"
	TreasuryInstructionApproved TreasuryInstructionStatus = "approved"
	TreasuryInstructionRejected TreasuryInstructionStatus = "rejected"
)

// TreasuryInstruction records an auditable operator action for treasury movement.
type TreasuryInstruction struct {
	ID          string                    `json:"id"`
	Action      string                    `json:"action"`
	Asset       string                    `json:"asset"`
	Amount      string                    `json:"amount"`
	Source      string                    `json:"source"`
	Destination string                    `json:"destination"`
	Status      TreasuryInstructionStatus `json:"status"`
	RequestedBy string                    `json:"requested_by"`
	ApprovedBy  string                    `json:"approved_by,omitempty"`
	RejectedBy  string                    `json:"rejected_by,omitempty"`
	Notes       string                    `json:"notes,omitempty"`
	ReviewNotes string                    `json:"review_notes,omitempty"`
	CreatedAt   time.Time                 `json:"created_at"`
	ApprovedAt  *time.Time                `json:"approved_at,omitempty"`
	RejectedAt  *time.Time                `json:"rejected_at,omitempty"`
}

// TreasuryInstructionStore persists treasury instructions for auditability.
type TreasuryInstructionStore interface {
	Put(TreasuryInstruction) error
	Get(id string) (TreasuryInstruction, bool, error)
	List() ([]TreasuryInstruction, error)
	Close() error
}

// BoltTreasuryInstructionStore persists instructions in BoltDB.
type BoltTreasuryInstructionStore struct {
	db *bbolt.DB
}

// NewBoltTreasuryInstructionStore opens or creates the treasury instruction store.
func NewBoltTreasuryInstructionStore(path string) (*BoltTreasuryInstructionStore, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("treasury instruction store path required")
	}
	if err := os.MkdirAll(filepath.Dir(trimmed), 0o755); err != nil {
		return nil, fmt.Errorf("create treasury store dir: %w", err)
	}
	db, err := bbolt.Open(trimmed, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open treasury store: %w", err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(treasuryInstructionBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init treasury store: %w", err)
	}
	return &BoltTreasuryInstructionStore{db: db}, nil
}

// Put stores or replaces a treasury instruction.
func (s *BoltTreasuryInstructionStore) Put(instruction TreasuryInstruction) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("treasury store not initialised")
	}
	if strings.TrimSpace(instruction.ID) == "" {
		return fmt.Errorf("treasury instruction id required")
	}
	payload, err := json.Marshal(instruction)
	if err != nil {
		return fmt.Errorf("marshal treasury instruction: %w", err)
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(treasuryInstructionBucket).Put([]byte(instruction.ID), payload)
	})
}

// Get retrieves a treasury instruction by id.
func (s *BoltTreasuryInstructionStore) Get(id string) (TreasuryInstruction, bool, error) {
	if s == nil || s.db == nil {
		return TreasuryInstruction{}, false, fmt.Errorf("treasury store not initialised")
	}
	var instruction TreasuryInstruction
	err := s.db.View(func(tx *bbolt.Tx) error {
		value := tx.Bucket(treasuryInstructionBucket).Get([]byte(strings.TrimSpace(id)))
		if value == nil {
			return nil
		}
		return json.Unmarshal(value, &instruction)
	})
	if err != nil {
		return TreasuryInstruction{}, false, err
	}
	if instruction.ID == "" {
		return TreasuryInstruction{}, false, nil
	}
	return instruction, true, nil
}

// List returns all treasury instructions ordered newest-first.
func (s *BoltTreasuryInstructionStore) List() ([]TreasuryInstruction, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("treasury store not initialised")
	}
	items := make([]TreasuryInstruction, 0)
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(treasuryInstructionBucket).ForEach(func(_, value []byte) error {
			var instruction TreasuryInstruction
			if err := json.Unmarshal(value, &instruction); err != nil {
				return err
			}
			items = append(items, instruction)
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].CreatedAt.After(items[j].CreatedAt)
	})
	return items, nil
}

// Close releases the underlying database.
func (s *BoltTreasuryInstructionStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
