package evidence

import (
	"fmt"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/storage"
)

var (
	recordPrefix = []byte("consensus/potso/evidence/record/")
	indexKey     = []byte("consensus/potso/evidence/index")
)

type Store struct {
	db storage.Database
	mu sync.RWMutex
}

func NewStore(db storage.Database) *Store {
	return &Store{db: db}
}

func (s *Store) Put(hash [32]byte, ev Evidence, receivedAt int64) (*Record, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, fmt.Errorf("evidence store not initialised")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok, err := s.get(hash); err != nil {
		return nil, false, err
	} else if ok {
		return existing.Clone(), false, nil
	}

	if receivedAt == 0 {
		receivedAt = time.Now().Unix()
	}
	record := &Record{Hash: hash, Evidence: ev.Clone(), ReceivedAt: receivedAt}
	encoded, err := rlp.EncodeToBytes(record)
	if err != nil {
		return nil, false, err
	}
	if err := s.db.Put(buildRecordKey(hash), encoded); err != nil {
		return nil, false, err
	}
	if err := s.appendIndex(hash); err != nil {
		return nil, false, err
	}
	return record.Clone(), true, nil
}

func (s *Store) Get(hash [32]byte) (*Record, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, fmt.Errorf("evidence store not initialised")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.get(hash)
}

func (s *Store) List(filter Filter) ([]*Record, int, error) {
	if s == nil || s.db == nil {
		return nil, 0, fmt.Errorf("evidence store not initialised")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	hashes, err := s.loadIndex()
	if err != nil {
		return nil, 0, err
	}
	limit := filter.Limit
	if limit <= 0 {
		limit = DefaultPageLimit
	}
	offset := filter.Offset
	if offset < 0 {
		offset = 0
	}

	matches := make([]*Record, 0, limit)
	matchCount := 0
	hasMore := false

	for i := len(hashes) - 1; i >= 0; i-- {
		var hash [32]byte
		copy(hash[:], hashes[i])
		record, ok, err := s.get(hash)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			continue
		}
		if !matchesFilter(record, filter) {
			continue
		}
		if matchCount < offset {
			matchCount++
			continue
		}
		if len(matches) >= limit {
			hasMore = true
			break
		}
		matches = append(matches, record.Clone())
		matchCount++
	}
	nextOffset := -1
	if hasMore {
		nextOffset = offset + len(matches)
	}
	return matches, nextOffset, nil
}

func (s *Store) get(hash [32]byte) (*Record, bool, error) {
	data, err := s.db.Get(buildRecordKey(hash))
	if err != nil {
		return nil, false, nil
	}
	var record Record
	if err := rlp.DecodeBytes(data, &record); err != nil {
		return nil, false, err
	}
	return record.Clone(), true, nil
}

func (s *Store) appendIndex(hash [32]byte) error {
	hashes, err := s.loadIndex()
	if err != nil {
		return err
	}
	entry := make([]byte, len(hash))
	copy(entry, hash[:])
	hashes = append(hashes, entry)
	encoded, err := rlp.EncodeToBytes(hashes)
	if err != nil {
		return err
	}
	return s.db.Put(indexKey, encoded)
}

func (s *Store) loadIndex() ([][]byte, error) {
	data, err := s.db.Get(indexKey)
	if err != nil {
		return [][]byte{}, nil
	}
	var hashes [][]byte
	if err := rlp.DecodeBytes(data, &hashes); err != nil {
		return nil, err
	}
	return hashes, nil
}

func buildRecordKey(hash [32]byte) []byte {
	key := make([]byte, len(recordPrefix)+len(hash))
	copy(key, recordPrefix)
	copy(key[len(recordPrefix):], hash[:])
	return key
}

func matchesFilter(record *Record, filter Filter) bool {
	if record == nil {
		return false
	}
	if filter.Offender != nil {
		if record.Evidence.Offender != *filter.Offender {
			return false
		}
	}
	if filter.Type != "" && record.Evidence.Type != filter.Type {
		return false
	}
	minHeight := record.MinHeight()
	if filter.FromHeight != nil && minHeight < *filter.FromHeight {
		return false
	}
	if filter.ToHeight != nil && minHeight > *filter.ToHeight {
		return false
	}
	return true
}
