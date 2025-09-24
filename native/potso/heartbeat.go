package potso

import (
	"errors"
	"time"
)

const (
	// HeartbeatIntervalSeconds defines the minimum spacing between accepted heartbeats.
	HeartbeatIntervalSeconds = 60
	// TimestampToleranceSeconds bounds allowed skew between submitted timestamps and server time.
	TimestampToleranceSeconds = 120
)

// HeartbeatState captures the most recent heartbeat accepted for a participant.
type HeartbeatState struct {
	LastTimestamp uint64
	LastBlock     uint64
	LastHash      []byte
}

// ErrHeartbeatTooSoon indicates a heartbeat arrived before the minimum interval elapsed.
var ErrHeartbeatTooSoon = errors.New("heartbeat interval not satisfied")

// ErrHeartbeatOldTimestamp indicates a non-monotonic timestamp submission.
var ErrHeartbeatOldTimestamp = errors.New("heartbeat timestamp must increase")

// ApplyHeartbeat validates the supplied timestamp against the stored state and returns the
// uptime seconds accrued when the heartbeat is accepted. Callers must persist the updated
// state when the call succeeds.
func (s *HeartbeatState) ApplyHeartbeat(ts int64, block uint64, hash []byte) (uint64, bool, error) {
	if ts <= 0 {
		return 0, false, errors.New("heartbeat timestamp must be positive")
	}
	if s.LastTimestamp != 0 {
		last := int64(s.LastTimestamp)
		if ts < last {
			return 0, false, ErrHeartbeatOldTimestamp
		}
		if ts-last < HeartbeatIntervalSeconds {
			return 0, false, ErrHeartbeatTooSoon
		}
	}
	var delta uint64
	if s.LastTimestamp == 0 {
		delta = HeartbeatIntervalSeconds
	} else {
		delta = uint64(ts - int64(s.LastTimestamp))
	}
	s.LastTimestamp = uint64(ts)
	s.LastBlock = block
	s.LastHash = append([]byte(nil), hash...)
	return delta, true, nil
}

// WithinTolerance reports whether the provided timestamp is within the accepted skew relative
// to the supplied reference time.
func WithinTolerance(ts int64, reference time.Time) bool {
	diff := reference.Unix() - ts
	if diff < 0 {
		diff = -diff
	}
	return diff <= TimestampToleranceSeconds
}
