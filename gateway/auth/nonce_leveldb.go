package auth

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/syndtr/goleveldb/leveldb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

const (
	nonceKeyPrefix    = "nonce:"
	observedKeyPrefix = "observed:"
)

// LevelDBNoncePersistence provides a LevelDB-backed NoncePersistence implementation.
type LevelDBNoncePersistence struct {
	db *leveldb.DB
}

// NewLevelDBNoncePersistence opens (or creates) a LevelDB database at the provided path.
func NewLevelDBNoncePersistence(path string) (*LevelDBNoncePersistence, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return nil, fmt.Errorf("leveldb nonce persistence path required")
	}
	abs, err := filepath.Abs(trimmed)
	if err != nil {
		return nil, fmt.Errorf("resolve leveldb nonce path: %w", err)
	}
	db, err := leveldb.OpenFile(abs, nil)
	if err != nil {
		return nil, fmt.Errorf("open leveldb nonce store: %w", err)
	}
	return &LevelDBNoncePersistence{db: db}, nil
}

// Close releases the underlying LevelDB resources.
func (p *LevelDBNoncePersistence) Close() error {
	if p == nil || p.db == nil {
		return nil
	}
	return p.db.Close()
}

// EnsureNonce records a nonce usage if it has not been observed previously.
func (p *LevelDBNoncePersistence) EnsureNonce(ctx context.Context, record NonceRecord) (bool, error) {
	if p == nil || p.db == nil {
		return false, fmt.Errorf("leveldb persistence not configured")
	}
	apiKey := strings.TrimSpace(record.APIKey)
	ts := strings.TrimSpace(record.Timestamp)
	nonce := strings.TrimSpace(record.Nonce)
	if apiKey == "" || ts == "" || nonce == "" {
		return false, fmt.Errorf("nonce record incomplete")
	}
	observed := record.ObservedAt.UTC()
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	composite := compositeKey(apiKey, ts, nonce)
	nonceKey := []byte(nonceKeyPrefix + composite)
	existingVal, err := p.db.Get(nonceKey, nil)
	switch {
	case errors.Is(err, leveldb.ErrNotFound):
		// Not found: insert new entry.
	case err != nil:
		return false, fmt.Errorf("load nonce: %w", err)
	default:
		existing := int64(binary.BigEndian.Uint64(existingVal))
		if observed.UnixNano() > existing {
			if err := p.updateObserved(composite, nonceKey, existing, observed.UnixNano()); err != nil {
				return false, err
			}
		}
		return true, nil
	}

	batch := new(leveldb.Batch)
	nanos := observed.UnixNano()
	batch.Put(nonceKey, encodeUnixNano(nanos))
	batch.Put([]byte(observedKey(nanos, composite)), nil)
	if err := p.db.Write(batch, nil); err != nil {
		return false, fmt.Errorf("record nonce: %w", err)
	}
	return false, nil
}

// RecentNonces returns persisted nonces observed at or after the provided cutoff.
func (p *LevelDBNoncePersistence) RecentNonces(ctx context.Context, cutoff time.Time) ([]NonceRecord, error) {
	if p == nil || p.db == nil {
		return nil, fmt.Errorf("leveldb persistence not configured")
	}
	cutoff = cutoff.UTC()
	cutoffKey := []byte(observedKey(cutoff.UnixNano(), ""))
	iter := p.db.NewIterator(util.BytesPrefix([]byte(observedKeyPrefix)), nil)
	defer iter.Release()

	records := make([]NonceRecord, 0)
	for ok := iter.Seek(cutoffKey); ok; ok = iter.Next() {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		key := append([]byte(nil), iter.Key()...)
		composite, nanos, ok := parseObservedKey(key)
		if !ok {
			continue
		}
		parts := strings.SplitN(composite, "|", 3)
		if len(parts) != 3 {
			continue
		}
		records = append(records, NonceRecord{
			APIKey:     parts[0],
			Timestamp:  parts[1],
			Nonce:      parts[2],
			ObservedAt: time.Unix(0, nanos).UTC(),
		})
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("iterate observed nonces: %w", err)
	}
	return records, nil
}

// PruneNonces deletes entries observed before the provided cutoff time.
func (p *LevelDBNoncePersistence) PruneNonces(ctx context.Context, cutoff time.Time) error {
	if p == nil || p.db == nil {
		return fmt.Errorf("leveldb persistence not configured")
	}
	cutoff = cutoff.UTC()
	cutoffKey := []byte(observedKey(cutoff.UnixNano(), ""))
	iter := p.db.NewIterator(util.BytesPrefix([]byte(observedKeyPrefix)), nil)
	defer iter.Release()

	batch := new(leveldb.Batch)
	for iter.Next() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if compareKeys(iter.Key(), cutoffKey) >= 0 {
			break
		}
		composite, _, ok := parseObservedKey(iter.Key())
		if !ok {
			continue
		}
		batch.Delete(append([]byte(nil), iter.Key()...))
		batch.Delete([]byte(nonceKeyPrefix + composite))
	}
	if err := iter.Error(); err != nil {
		return fmt.Errorf("iterate observed nonces: %w", err)
	}
	if batch.Len() > 0 {
		if err := p.db.Write(batch, nil); err != nil {
			return fmt.Errorf("prune nonces: %w", err)
		}
	}
	return nil
}

func (p *LevelDBNoncePersistence) updateObserved(composite string, nonceKey []byte, previous int64, next int64) error {
	batch := new(leveldb.Batch)
	batch.Put(nonceKey, encodeUnixNano(next))
	batch.Delete([]byte(observedKey(previous, composite)))
	batch.Put([]byte(observedKey(next, composite)), nil)
	if err := p.db.Write(batch, nil); err != nil {
		return fmt.Errorf("update observed nonce: %w", err)
	}
	return nil
}

func observedKey(nanos int64, composite string) string {
	return fmt.Sprintf("%s%020d:%s", observedKeyPrefix, nanos, composite)
}

func parseObservedKey(key []byte) (string, int64, bool) {
	raw := string(key)
	parts := strings.SplitN(raw, ":", 3)
	if len(parts) != 3 {
		return "", 0, false
	}
	nanos, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return parts[2], nanos, true
}

func encodeUnixNano(nanos int64) []byte {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, uint64(nanos))
	return buf
}

func compositeKey(apiKey, timestamp, nonce string) string {
	return strings.Join([]string{apiKey, timestamp, nonce}, "|")
}

func compareKeys(a, b []byte) int {
	min := len(a)
	if len(b) < min {
		min = len(b)
	}
	for i := 0; i < min; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}
