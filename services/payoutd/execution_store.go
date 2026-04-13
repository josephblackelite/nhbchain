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

var payoutExecutionBucket = []byte("payout_executions")

// PayoutExecutionStatus captures the lifecycle state of a payout execution.
type PayoutExecutionStatus string

const (
	PayoutExecutionProcessing PayoutExecutionStatus = "processing"
	PayoutExecutionSettled    PayoutExecutionStatus = "settled"
	PayoutExecutionAborted    PayoutExecutionStatus = "aborted"
	PayoutExecutionFailed     PayoutExecutionStatus = "failed"
)

// PayoutExecution records a payout intent as it moves through execution.
type PayoutExecution struct {
	IntentID     string                `json:"intent_id"`
	Account      string                `json:"account,omitempty"`
	PartnerID    string                `json:"partner_id,omitempty"`
	Region       string                `json:"region,omitempty"`
	RequestedBy  string                `json:"requested_by,omitempty"`
	ApprovedBy   string                `json:"approved_by,omitempty"`
	ApprovalRef  string                `json:"approval_ref,omitempty"`
	StableAsset  string                `json:"stable_asset,omitempty"`
	StableAmount string                `json:"stable_amount,omitempty"`
	NhbAmount    string                `json:"nhb_amount,omitempty"`
	Destination  string                `json:"destination,omitempty"`
	EvidenceURI  string                `json:"evidence_uri,omitempty"`
	TxHash       string                `json:"tx_hash,omitempty"`
	Status       PayoutExecutionStatus `json:"status"`
	Error        string                `json:"error,omitempty"`
	CreatedAt    time.Time             `json:"created_at"`
	UpdatedAt    time.Time             `json:"updated_at"`
	SettledAt    *time.Time            `json:"settled_at,omitempty"`
}

// PayoutExecutionStore persists payout execution records.
type PayoutExecutionStore interface {
	Put(PayoutExecution) error
	Get(intentID string) (PayoutExecution, bool, error)
	List() ([]PayoutExecution, error)
	Close() error
}

// BoltPayoutExecutionStore persists payout execution state in BoltDB.
type BoltPayoutExecutionStore struct {
	db *bbolt.DB
}

// NewBoltPayoutExecutionStore opens or creates the payout execution store.
func NewBoltPayoutExecutionStore(path string) (*BoltPayoutExecutionStore, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("payout execution store path required")
	}
	if err := os.MkdirAll(filepath.Dir(trimmed), 0o755); err != nil {
		return nil, fmt.Errorf("create payout execution store dir: %w", err)
	}
	db, err := bbolt.Open(trimmed, 0o600, &bbolt.Options{Timeout: time.Second})
	if err != nil {
		return nil, fmt.Errorf("open payout execution store: %w", err)
	}
	if err := db.Update(func(tx *bbolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(payoutExecutionBucket)
		return err
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init payout execution store: %w", err)
	}
	return &BoltPayoutExecutionStore{db: db}, nil
}

// Put stores or replaces a payout execution record.
func (s *BoltPayoutExecutionStore) Put(record PayoutExecution) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("payout execution store not initialised")
	}
	record.IntentID = strings.TrimSpace(record.IntentID)
	if record.IntentID == "" {
		return fmt.Errorf("payout execution intent id required")
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal payout execution: %w", err)
	}
	return s.db.Update(func(tx *bbolt.Tx) error {
		return tx.Bucket(payoutExecutionBucket).Put([]byte(record.IntentID), payload)
	})
}

// Get retrieves a payout execution by intent id.
func (s *BoltPayoutExecutionStore) Get(intentID string) (PayoutExecution, bool, error) {
	if s == nil || s.db == nil {
		return PayoutExecution{}, false, fmt.Errorf("payout execution store not initialised")
	}
	var record PayoutExecution
	err := s.db.View(func(tx *bbolt.Tx) error {
		value := tx.Bucket(payoutExecutionBucket).Get([]byte(strings.TrimSpace(intentID)))
		if value == nil {
			return nil
		}
		return json.Unmarshal(value, &record)
	})
	if err != nil {
		return PayoutExecution{}, false, err
	}
	if record.IntentID == "" {
		return PayoutExecution{}, false, nil
	}
	return record, true, nil
}

// List returns payout executions ordered newest-first.
func (s *BoltPayoutExecutionStore) List() ([]PayoutExecution, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("payout execution store not initialised")
	}
	items := make([]PayoutExecution, 0)
	err := s.db.View(func(tx *bbolt.Tx) error {
		return tx.Bucket(payoutExecutionBucket).ForEach(func(_, value []byte) error {
			var record PayoutExecution
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
func (s *BoltPayoutExecutionStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}
