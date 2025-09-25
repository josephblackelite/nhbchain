package rewards

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/storage"
)

// RewardStatus represents the payable state of a reward ledger entry.
type RewardStatus string

const (
	// RewardStatusReady indicates the reward is ready to be disbursed.
	RewardStatusReady RewardStatus = "ready"
	// RewardStatusPaid indicates the reward has been marked as settled.
	RewardStatusPaid RewardStatus = "paid"

	ledgerIndexKey         = "consensus/potso/rewards/index"
	ledgerEntryKeyFormat   = "consensus/potso/rewards/%020d/%s"
	defaultLedgerPageLimit = 200
)

// RewardEntry tracks the payable state for a participant for a given epoch.
type RewardEntry struct {
	Epoch       uint64
	Address     [20]byte
	Amount      *big.Int
	Currency    string
	Status      RewardStatus
	GeneratedAt time.Time
	UpdatedAt   time.Time
	UpdatedBy   string
	PaidAt      *time.Time
	PaidBy      string
	TxRef       string
	Checksum    string
}

// Clone creates a deep copy of the reward entry to ensure callers cannot mutate
// internal state.
func (e *RewardEntry) Clone() *RewardEntry {
	if e == nil {
		return nil
	}
	clone := &RewardEntry{
		Epoch:       e.Epoch,
		Address:     e.Address,
		Currency:    e.Currency,
		Status:      e.Status,
		GeneratedAt: e.GeneratedAt,
		UpdatedAt:   e.UpdatedAt,
		UpdatedBy:   e.UpdatedBy,
		PaidBy:      e.PaidBy,
		TxRef:       e.TxRef,
		Checksum:    e.Checksum,
	}
	if e.Amount != nil {
		clone.Amount = new(big.Int).Set(e.Amount)
	}
	if e.PaidAt != nil {
		t := *e.PaidAt
		clone.PaidAt = &t
	}
	return clone
}

// Ledger persists reward entries and exposes filtered listings for RPC/export.
type Ledger struct {
	db storage.Database
	mu sync.RWMutex
}

// NewLedger constructs a reward ledger backed by the supplied key-value store.
func NewLedger(db storage.Database) *Ledger {
	return &Ledger{db: db}
}

type storedRewardEntry struct {
	Epoch       uint64
	Address     []byte
	Amount      []byte
	Currency    string
	Status      string
	GeneratedAt uint64
	UpdatedAt   uint64
	UpdatedBy   string
	PaidAt      uint64
	PaidBy      string
	TxRef       string
	Checksum    string
}

type indexEntry struct {
	Epoch   uint64
	Address []byte
}

func (l *Ledger) put(entry *RewardEntry, now time.Time) error {
	if entry == nil {
		return errors.New("rewards: nil entry")
	}
	if entry.Amount == nil || entry.Amount.Sign() < 0 {
		return errors.New("rewards: entry amount must be non-negative")
	}
	if entry.Currency == "" {
		entry.Currency = "ZNHB"
	}
	if entry.Status == "" {
		entry.Status = RewardStatusReady
	}
	if entry.GeneratedAt.IsZero() {
		entry.GeneratedAt = now
	}
	entry.UpdatedAt = now
	encoded, err := rlp.EncodeToBytes(storedRewardEntry{
		Epoch:       entry.Epoch,
		Address:     append([]byte(nil), entry.Address[:]...),
		Amount:      entry.Amount.Bytes(),
		Currency:    entry.Currency,
		Status:      string(entry.Status),
		GeneratedAt: uint64(entry.GeneratedAt.Unix()),
		UpdatedAt:   uint64(entry.UpdatedAt.Unix()),
		UpdatedBy:   entry.UpdatedBy,
		PaidAt: func() uint64 {
			if entry.PaidAt == nil {
				return 0
			}
			return uint64(entry.PaidAt.Unix())
		}(),
		PaidBy:   entry.PaidBy,
		TxRef:    entry.TxRef,
		Checksum: entry.Checksum,
	})
	if err != nil {
		return err
	}
	key := []byte(fmt.Sprintf(ledgerEntryKeyFormat, entry.Epoch, hex.EncodeToString(entry.Address[:])))
	if err := l.db.Put(key, encoded); err != nil {
		return err
	}
	return l.ensureIndexEntry(entry.Epoch, entry.Address)
}

func (l *Ledger) ensureIndexEntry(epoch uint64, addr [20]byte) error {
	index, err := l.loadIndex()
	if err != nil {
		return err
	}
	hexAddr := hex.EncodeToString(addr[:])
	for _, existing := range index {
		if existing.Epoch == epoch && hex.EncodeToString(existing.Address) == hexAddr {
			return nil
		}
	}
	entry := indexEntry{Epoch: epoch, Address: append([]byte(nil), addr[:]...)}
	index = append(index, entry)
	return l.saveIndex(index)
}

func (l *Ledger) loadIndex() ([]indexEntry, error) {
	data, err := l.db.Get([]byte(ledgerIndexKey))
	if err != nil {
		return []indexEntry{}, nil
	}
	var raw []indexEntry
	if err := rlp.DecodeBytes(data, &raw); err != nil {
		return nil, err
	}
	fixed := make([]indexEntry, len(raw))
	for i := range raw {
		fixed[i] = indexEntry{
			Epoch:   raw[i].Epoch,
			Address: append([]byte(nil), raw[i].Address...),
		}
	}
	return fixed, nil
}

func (l *Ledger) saveIndex(entries []indexEntry) error {
	encoded, err := rlp.EncodeToBytes(entries)
	if err != nil {
		return err
	}
	return l.db.Put([]byte(ledgerIndexKey), encoded)
}

func (l *Ledger) get(epoch uint64, addr [20]byte) (*RewardEntry, bool, error) {
	key := []byte(fmt.Sprintf(ledgerEntryKeyFormat, epoch, hex.EncodeToString(addr[:])))
	data, err := l.db.Get(key)
	if err != nil {
		return nil, false, nil
	}
	var stored storedRewardEntry
	if err := rlp.DecodeBytes(data, &stored); err != nil {
		return nil, false, err
	}
	entry := &RewardEntry{
		Epoch:       stored.Epoch,
		Currency:    stored.Currency,
		Status:      RewardStatus(stored.Status),
		GeneratedAt: time.Unix(int64(stored.GeneratedAt), 0).UTC(),
		UpdatedAt:   time.Unix(int64(stored.UpdatedAt), 0).UTC(),
		UpdatedBy:   stored.UpdatedBy,
		PaidBy:      stored.PaidBy,
		TxRef:       stored.TxRef,
		Checksum:    stored.Checksum,
	}
	copy(entry.Address[:], stored.Address)
	if len(stored.Amount) == 0 {
		entry.Amount = big.NewInt(0)
	} else {
		entry.Amount = new(big.Int).SetBytes(stored.Amount)
	}
	if stored.PaidAt > 0 {
		ts := time.Unix(int64(stored.PaidAt), 0).UTC()
		entry.PaidAt = &ts
	}
	return entry, true, nil
}

// Put inserts or replaces a single ledger entry.
func (l *Ledger) Put(entry *RewardEntry) error {
	if l == nil || l.db == nil {
		return errors.New("rewards: ledger not initialised")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().UTC()
	if entry.Checksum == "" {
		entry.Checksum = EntryChecksum(entry.Epoch, entry.Address, entry.Amount)
	}
	return l.put(entry.Clone(), now)
}

// PutBatch inserts or replaces a batch of ledger entries atomically.
func (l *Ledger) PutBatch(entries []*RewardEntry) error {
	if l == nil || l.db == nil {
		return errors.New("rewards: ledger not initialised")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now().UTC()
	for _, entry := range entries {
		if entry == nil {
			return errors.New("rewards: nil entry in batch")
		}
		clone := entry.Clone()
		if clone.Checksum == "" {
			clone.Checksum = EntryChecksum(clone.Epoch, clone.Address, clone.Amount)
		}
		if err := l.put(clone, now); err != nil {
			return err
		}
	}
	return nil
}

// RewardFilter enables filtering and pagination when listing ledger entries.
type RewardFilter struct {
	Epoch   *uint64
	Address *[20]byte
	Status  RewardStatus
	Cursor  string
	Limit   int
}

// List returns reward entries that satisfy the provided filter along with the
// next cursor (if any).
func (l *Ledger) List(filter RewardFilter) ([]*RewardEntry, string, error) {
	if l == nil || l.db == nil {
		return nil, "", errors.New("rewards: ledger not initialised")
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	index, err := l.loadIndex()
	if err != nil {
		return nil, "", err
	}
	entries := make([]*RewardEntry, 0, len(index))
	for _, idx := range index {
		var addr [20]byte
		copy(addr[:], idx.Address)
		entry, ok, err := l.get(idx.Epoch, addr)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		if filter.Epoch != nil && entry.Epoch != *filter.Epoch {
			continue
		}
		if filter.Address != nil && entry.Address != *filter.Address {
			continue
		}
		if filter.Status != "" && entry.Status != filter.Status {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Epoch == entries[j].Epoch {
			return bytesCompare(entries[i].Address[:], entries[j].Address[:]) < 0
		}
		return entries[i].Epoch < entries[j].Epoch
	})
	limit := filter.Limit
	if limit <= 0 {
		limit = defaultLedgerPageLimit
	}
	offset := 0
	if filter.Cursor != "" {
		off, err := strconv.Atoi(filter.Cursor)
		if err != nil {
			return nil, "", fmt.Errorf("rewards: invalid cursor: %w", err)
		}
		if off < 0 {
			off = 0
		}
		offset = off
	}
	if offset >= len(entries) {
		return []*RewardEntry{}, "", nil
	}
	end := offset + limit
	if end > len(entries) {
		end = len(entries)
	}
	page := make([]*RewardEntry, 0, end-offset)
	for i := offset; i < end; i++ {
		page = append(page, entries[i].Clone())
	}
	nextCursor := ""
	if end < len(entries) {
		nextCursor = strconv.Itoa(end)
	}
	return page, nextCursor, nil
}

// MarkPaidReference identifies a ledger entry when marking rewards settled.
type MarkPaidReference struct {
	Address [20]byte
	Amount  *big.Int
}

// MarkPaid marks the referenced ledger entries as paid when the amount matches
// and returns the number of transitions performed.
func (l *Ledger) MarkPaid(epoch uint64, refs []MarkPaidReference, txRef string, actor string, paidAt time.Time) (int, error) {
	if l == nil || l.db == nil {
		return 0, errors.New("rewards: ledger not initialised")
	}
	if paidAt.IsZero() {
		paidAt = time.Now().UTC()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	updated := 0
	for _, ref := range refs {
		entry, ok, err := l.get(epoch, ref.Address)
		if err != nil {
			return updated, err
		}
		if !ok {
			continue
		}
		if entry.Amount == nil || ref.Amount == nil || entry.Amount.Cmp(ref.Amount) != 0 {
			continue
		}
		if entry.Status == RewardStatusPaid {
			continue
		}
		entry.Status = RewardStatusPaid
		entry.TxRef = txRef
		entry.PaidBy = actor
		entry.UpdatedBy = actor
		entry.UpdatedAt = paidAt
		entry.Checksum = EntryChecksum(entry.Epoch, entry.Address, entry.Amount)
		entry.PaidAt = func() *time.Time {
			ts := paidAt
			return &ts
		}()
		if err := l.put(entry, paidAt); err != nil {
			return updated, err
		}
		updated++
	}
	return updated, nil
}

// Get retrieves a single ledger entry if present.
func (l *Ledger) Get(epoch uint64, addr [20]byte) (*RewardEntry, bool, error) {
	if l == nil || l.db == nil {
		return nil, false, errors.New("rewards: ledger not initialised")
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	entry, ok, err := l.get(epoch, addr)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return entry.Clone(), true, nil
}
