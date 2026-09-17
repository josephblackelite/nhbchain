package p2p

import (
	"math"
	"sync"
	"time"
)

const (
	heartbeatRewardDelta         = 1
	uptimeRewardDelta            = 2
	invalidBlockPenaltyDelta     = -20
	malformedMessagePenaltyDelta = -5
	spamPenaltyDelta             = -10
)

// ReputationConfig defines the thresholds for the reputation engine.
type ReputationConfig struct {
	GreyScore        int
	BanScore         int
	BanDuration      time.Duration
	GreylistDuration time.Duration
	DecayHalfLife    time.Duration
}

// ReputationStatus represents the state of a peer after an adjustment.
type ReputationStatus struct {
	Score       int
	Greylisted  bool
	Banned      bool
	Until       time.Time
	LatencyMS   float64
	Useful      uint64
	Misbehavior uint64
}

type reputationRecord struct {
	score       float64
	updatedAt   time.Time
	bannedTill  time.Time
	greyTill    time.Time
	latencyEWMA float64
	useful      uint64
	misbehavior uint64
}

// ReputationManager keeps per-peer scoring with decay.
type ReputationManager struct {
	cfg ReputationConfig

	mu      sync.Mutex
	records map[string]*reputationRecord
}

// NewReputationManager returns a new reputation tracker.
func NewReputationManager(cfg ReputationConfig) *ReputationManager {
	if cfg.DecayHalfLife <= 0 {
		cfg.DecayHalfLife = 10 * time.Minute
	}
	if cfg.GreylistDuration <= 0 {
		cfg.GreylistDuration = 2 * time.Minute
	}
	if cfg.BanDuration <= 0 {
		cfg.BanDuration = 15 * time.Minute
	}
	return &ReputationManager{cfg: cfg, records: make(map[string]*reputationRecord)}
}

// Adjust updates the score for a peer, returning the latest status.
func (m *ReputationManager) Adjust(id string, delta int, now time.Time, persistent bool) ReputationStatus {
	if id == "" {
		return ReputationStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	rec := m.ensureRecordLocked(id, now)

	m.applyDecayLocked(rec, now)
	rec.score += float64(delta)
	rec.updatedAt = now

	status := m.composeStatusLocked(rec, now)

	if persistent {
		// Persistent peers never enter the ban list but can recover quicker.
		if rec.score > 0 {
			rec.score = 0
		}
		rec.bannedTill = time.Time{}
	} else if status.Score <= -m.cfg.BanScore && m.cfg.BanScore > 0 {
		rec.bannedTill = now.Add(m.cfg.BanDuration)
	}

	if status.Score <= -m.cfg.GreyScore && m.cfg.GreyScore > 0 {
		rec.greyTill = now.Add(m.cfg.GreylistDuration)
	} else if status.Score > -m.cfg.GreyScore {
		rec.greyTill = time.Time{}
	}

	return m.composeStatusLocked(rec, now)
}

// Reward sets a positive delta without triggering ban thresholds.
func (m *ReputationManager) Reward(id string, delta int, now time.Time) ReputationStatus {
	return m.Adjust(id, delta, now, false)
}

// MarkHeartbeat rewards a peer for responding within the heartbeat window.
func (m *ReputationManager) MarkHeartbeat(id string, now time.Time) ReputationStatus {
	return m.Adjust(id, heartbeatRewardDelta, now, false)
}

// MarkUptime rewards a peer for sustained uptime. Duration less than a day defaults to one period.
func (m *ReputationManager) MarkUptime(id string, duration time.Duration, now time.Time) ReputationStatus {
	days := int(duration / (24 * time.Hour))
	if days <= 0 {
		days = 1
	}
	return m.Adjust(id, days*uptimeRewardDelta, now, false)
}

// PenalizeInvalidBlock applies a heavy penalty for invalid or forked blocks.
func (m *ReputationManager) PenalizeInvalidBlock(id string, now time.Time, persistent bool) ReputationStatus {
	return m.Adjust(id, invalidBlockPenaltyDelta, now, persistent)
}

// PenalizeMalformed applies a light penalty for malformed protocol messages.
func (m *ReputationManager) PenalizeMalformed(id string, now time.Time, persistent bool) ReputationStatus {
	return m.Adjust(id, malformedMessagePenaltyDelta, now, persistent)
}

// PenalizeSpam throttles spamming peers without immediately banning them.
func (m *ReputationManager) PenalizeSpam(id string, now time.Time, persistent bool) ReputationStatus {
	return m.Adjust(id, spamPenaltyDelta, now, persistent)
}

// ObserveLatency records a latency measurement for a peer, updating the EWMA.
func (m *ReputationManager) ObserveLatency(id string, latency time.Duration, now time.Time) ReputationStatus {
	if m == nil || id == "" || latency <= 0 {
		return ReputationStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.ensureRecordLocked(id, now)
	m.applyDecayLocked(rec, now)
	ms := float64(latency) / float64(time.Millisecond)
	if rec.latencyEWMA <= 0 {
		rec.latencyEWMA = ms
	} else {
		const alpha = 0.2
		rec.latencyEWMA = alpha*ms + (1-alpha)*rec.latencyEWMA
	}
	rec.updatedAt = now
	return m.composeStatusLocked(rec, now)
}

// MarkUseful increases the usefulness counter for a peer.
func (m *ReputationManager) MarkUseful(id string, now time.Time) ReputationStatus {
	if m == nil || id == "" {
		return ReputationStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.ensureRecordLocked(id, now)
	m.applyDecayLocked(rec, now)
	rec.useful++
	rec.updatedAt = now
	return m.composeStatusLocked(rec, now)
}

// MarkMisbehavior increases the misbehavior counter for a peer without modifying the score directly.
func (m *ReputationManager) MarkMisbehavior(id string, now time.Time) ReputationStatus {
	if m == nil || id == "" {
		return ReputationStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.ensureRecordLocked(id, now)
	m.applyDecayLocked(rec, now)
	rec.misbehavior++
	rec.updatedAt = now
	return m.composeStatusLocked(rec, now)
}

// SetBan overrides the ban expiry for a peer.
func (m *ReputationManager) SetBan(id string, until time.Time, now time.Time) {
	if m == nil || id == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.ensureRecordLocked(id, now)
	if until.After(now) {
		rec.bannedTill = until
	} else {
		rec.bannedTill = time.Time{}
	}
	rec.updatedAt = now
}

// IsBanned returns true if the peer is banned at the provided time.
func (m *ReputationManager) IsBanned(id string, now time.Time) bool {
	banned, _ := m.BanInfo(id, now)
	return banned
}

// IsGreylisted returns true if the peer is currently greylisted.
func (m *ReputationManager) IsGreylisted(id string, now time.Time) bool {
	grey, _ := m.GreyInfo(id, now)
	return grey
}

// Score returns the integer score after decay.
func (m *ReputationManager) Score(id string, now time.Time) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.records[id]
	if rec == nil {
		return 0
	}
	m.applyDecayLocked(rec, now)
	return int(math.Round(rec.score))
}

func (m *ReputationManager) applyDecayLocked(rec *reputationRecord, now time.Time) {
	if rec == nil {
		return
	}
	if now.Before(rec.updatedAt) {
		rec.updatedAt = now
		return
	}
	if m.cfg.DecayHalfLife <= 0 {
		return
	}
	elapsed := now.Sub(rec.updatedAt)
	if elapsed <= 0 {
		return
	}
	halfLife := m.cfg.DecayHalfLife
	periods := float64(elapsed) / float64(halfLife)
	if periods <= 0 {
		rec.updatedAt = now
		return
	}
	factor := math.Pow(0.5, periods)
	rec.score *= factor
	if math.Abs(rec.score) < 1e-6 {
		rec.score = 0
	}
	rec.updatedAt = now
}

// BanInfo returns whether a peer is banned and the expiry time.
func (m *ReputationManager) BanInfo(id string, now time.Time) (bool, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.records[id]
	if rec == nil {
		return false, time.Time{}
	}
	if rec.bannedTill.IsZero() {
		return false, time.Time{}
	}
	if now.After(rec.bannedTill) {
		rec.bannedTill = time.Time{}
		return false, time.Time{}
	}
	return true, rec.bannedTill
}

// GreyInfo returns whether a peer is greylisted and the expiry time.
func (m *ReputationManager) GreyInfo(id string, now time.Time) (bool, time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := m.records[id]
	if rec == nil {
		return false, time.Time{}
	}
	if rec.greyTill.IsZero() {
		return false, time.Time{}
	}
	if now.After(rec.greyTill) {
		rec.greyTill = time.Time{}
		return false, time.Time{}
	}
	return true, rec.greyTill
}

// Snapshot returns a copy of all scores with decay applied at the given time.
func (m *ReputationManager) Snapshot(now time.Time) map[string]ReputationStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]ReputationStatus, len(m.records))
	for id, rec := range m.records {
		m.applyDecayLocked(rec, now)
		out[id] = m.composeStatusLocked(rec, now)
	}
	return out
}

func (m *ReputationManager) ensureRecordLocked(id string, now time.Time) *reputationRecord {
	rec := m.records[id]
	if rec == nil {
		rec = &reputationRecord{updatedAt: now}
		m.records[id] = rec
	}
	return rec
}

func (m *ReputationManager) composeStatusLocked(rec *reputationRecord, now time.Time) ReputationStatus {
	status := ReputationStatus{
		Score:       int(math.Round(rec.score)),
		LatencyMS:   rec.latencyEWMA,
		Useful:      rec.useful,
		Misbehavior: rec.misbehavior,
	}
	if rec.bannedTill.After(now) {
		status.Banned = true
		status.Until = rec.bannedTill
	}
	if rec.greyTill.After(now) {
		status.Greylisted = true
		if status.Until.IsZero() || rec.greyTill.Before(status.Until) {
			status.Until = rec.greyTill
		}
	}
	return status
}
