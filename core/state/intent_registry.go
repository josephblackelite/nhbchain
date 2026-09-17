package state

import (
	"errors"
	"fmt"
	"time"
)

var (
	intentRegistryPrefix = []byte("pos/intent/")

	// ErrIntentRefInvalid indicates the supplied reference is empty or exceeds
	// the maximum supported length.
	ErrIntentRefInvalid = errors.New("intent: invalid reference")
	// ErrIntentExpired marks intents that have exceeded their validity window.
	ErrIntentExpired = errors.New("intent: expired")
	// ErrIntentConsumed indicates the reference has already been processed.
	ErrIntentConsumed = errors.New("intent: already consumed")
)

const (
	maxIntentRefLen = 64
)

// IntentRecord tracks the lifecycle state for a POS payment intent reference.
//
// Expiry is expressed as a UNIX timestamp (seconds) and reflects the smaller of
// the merchant-provided deadline and the registry TTL clamp enforced on-chain.
// Consumed marks whether the reference has already been observed in a
// committed block.
//
// Records are stored in the KV namespace under the intent reference key.
// Expired records are lazily removed when encountered during validation.
type IntentRecord struct {
	Expiry   uint64
	Consumed bool
}

func intentRegistryKey(ref []byte) []byte {
	key := make([]byte, len(intentRegistryPrefix)+len(ref))
	copy(key, intentRegistryPrefix)
	copy(key[len(intentRegistryPrefix):], ref)
	return key
}

// IntentRegistryGet loads a previously stored intent registry entry.
func (m *Manager) IntentRegistryGet(ref []byte) (*IntentRecord, bool, error) {
	if len(ref) == 0 {
		return nil, false, ErrIntentRefInvalid
	}
	var record IntentRecord
	ok, err := m.KVGet(intentRegistryKey(ref), &record)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return &record, true, nil
}

// IntentRegistryDelete removes the supplied reference from the registry. Missing
// references are ignored.
func (m *Manager) IntentRegistryDelete(ref []byte) error {
	if len(ref) == 0 {
		return ErrIntentRefInvalid
	}
	return m.KVDelete(intentRegistryKey(ref))
}

// IntentRegistryValidate ensures the provided reference can be consumed at the
// supplied time. The returned expiry is clamped to the configured TTL window and
// should be persisted alongside the consumed record.
func (m *Manager) IntentRegistryValidate(ref []byte, requestedExpiry uint64, now time.Time, ttl time.Duration) (uint64, error) {
	if len(ref) == 0 || len(ref) > maxIntentRefLen {
		return 0, ErrIntentRefInvalid
	}
	unixNow := now.Unix()
	if unixNow < 0 {
		return 0, fmt.Errorf("intent: negative unix time")
	}
	if requestedExpiry == 0 || uint64(unixNow) >= requestedExpiry {
		return 0, ErrIntentExpired
	}
	record, exists, err := m.IntentRegistryGet(ref)
	if err != nil {
		return 0, err
	}
	if exists {
		if record.Expiry != 0 && record.Expiry <= uint64(unixNow) {
			_ = m.IntentRegistryDelete(ref)
			return 0, ErrIntentExpired
		}
		if record.Consumed {
			return 0, ErrIntentConsumed
		}
		return 0, ErrIntentConsumed
	}
	effectiveExpiry := requestedExpiry
	if ttl > 0 {
		ttlSeconds := uint64(ttl / time.Second)
		if ttlSeconds > 0 {
			limit := uint64(unixNow) + ttlSeconds
			if effectiveExpiry == 0 || effectiveExpiry > limit {
				effectiveExpiry = limit
			}
		}
	}
	if effectiveExpiry <= uint64(unixNow) {
		return 0, ErrIntentExpired
	}
	return effectiveExpiry, nil
}

// IntentRegistryConsume marks the supplied reference as processed using the
// provided expiry clamp. Callers must first invoke IntentRegistryValidate to
// ensure the reference is admissible.
func (m *Manager) IntentRegistryConsume(ref []byte, expiry uint64) error {
	if len(ref) == 0 {
		return ErrIntentRefInvalid
	}
	record := IntentRecord{Expiry: expiry, Consumed: true}
	return m.KVPut(intentRegistryKey(ref), record)
}
