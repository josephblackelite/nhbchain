package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	_ "github.com/glebarez/sqlite"

	swap "nhbchain/native/swap"
)

// Storage wraps the swapd persistence layer.
type Storage struct {
	db *sql.DB
}

// Open initialises the backing store using sqlite-compatible DSN.
func Open(path string) (*Storage, error) {
	if strings.TrimSpace(path) == "" {
		path = "file:swapd?mode=memory&cache=shared"
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply schema: %w", err)
	}
	return &Storage{db: db}, nil
}

// Close releases database resources.
func (s *Storage) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// RecordSample persists a raw oracle quote.
func (s *Storage) RecordSample(ctx context.Context, base, quote, source string, data swap.PriceQuote, recorded time.Time) error {
	if s == nil {
		return fmt.Errorf("storage not configured")
	}
	if data.Rate == nil {
		return fmt.Errorf("quote missing rate")
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO oracle_samples(pair, source, rate, observed_at, recorded_at)
        VALUES(?, ?, ?, ?, ?)
    `, pairKey(base, quote), strings.ToLower(source), data.Rate.FloatString(18), data.Timestamp.UTC().Unix(), recorded.UTC())
	if err != nil {
		return fmt.Errorf("insert sample: %w", err)
	}
	return nil
}

// RecordSnapshot stores the aggregated median snapshot.
func (s *Storage) RecordSnapshot(ctx context.Context, base, quote, median string, feeders []string, proofID string, ts time.Time) error {
	if s == nil {
		return fmt.Errorf("storage not configured")
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO oracle_snapshots(pair, median_rate, feeders, proof_id, observed_at, recorded_at)
        VALUES(?, ?, ?, ?, ?, ?)
    `, pairKey(base, quote), strings.TrimSpace(median), strings.Join(feeders, ","), proofID, ts.UTC().Unix(), time.Now().UTC())
	if err != nil {
		return fmt.Errorf("insert snapshot: %w", err)
	}
	return nil
}

// LatestSnapshot returns the most recent aggregated median for the pair.
func (s *Storage) LatestSnapshot(ctx context.Context, base, quote string) (Snapshot, error) {
	result := Snapshot{}
	if s == nil {
		return result, fmt.Errorf("storage not configured")
	}
	row := s.db.QueryRowContext(ctx, `
        SELECT median_rate, feeders, proof_id, observed_at, recorded_at
        FROM oracle_snapshots
        WHERE pair = ?
        ORDER BY id DESC
        LIMIT 1
    `, pairKey(base, quote))
	var feeders string
	if err := row.Scan(&result.MedianRate, &feeders, &result.ProofID, &result.ObservedAtUnix, &result.RecordedAt); err != nil {
		if err == sql.ErrNoRows {
			return result, fmt.Errorf("snapshot not found")
		}
		return result, fmt.Errorf("query snapshot: %w", err)
	}
	if feeders != "" {
		result.Feeders = strings.Split(feeders, ",")
	}
	return result, nil
}

// Snapshot captures the latest oracle aggregate.
type Snapshot struct {
	MedianRate     string
	Feeders        []string
	ProofID        string
	ObservedAtUnix int64
	RecordedAt     time.Time
}

const schema = `
CREATE TABLE IF NOT EXISTS oracle_samples (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pair TEXT NOT NULL,
    source TEXT NOT NULL,
    rate TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    recorded_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oracle_samples_pair_ts ON oracle_samples(pair, observed_at);

CREATE TABLE IF NOT EXISTS oracle_snapshots (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    pair TEXT NOT NULL,
    median_rate TEXT NOT NULL,
    feeders TEXT NOT NULL,
    proof_id TEXT NOT NULL,
    observed_at INTEGER NOT NULL,
    recorded_at TIMESTAMP NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_oracle_snapshots_pair_ts ON oracle_snapshots(pair, observed_at);

CREATE TABLE IF NOT EXISTS throttle_policy (
    id TEXT PRIMARY KEY,
    mint_limit INTEGER NOT NULL,
    redeem_limit INTEGER NOT NULL,
    window_seconds INTEGER NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS throttle_events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    policy_id TEXT NOT NULL,
    action TEXT NOT NULL,
    amount TEXT NOT NULL,
    occurred_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_throttle_events ON throttle_events(policy_id, action, occurred_at);

CREATE TABLE IF NOT EXISTS daily_usage (
    day TEXT PRIMARY KEY,
    amount INTEGER NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
`

func pairKey(base, quote string) string {
	b := strings.ToUpper(strings.TrimSpace(base))
	q := strings.ToUpper(strings.TrimSpace(quote))
	if b == "" && q == "" {
		return ""
	}
	return b + "/" + q
}

// Policy captures mint and redeem throttles.
type Policy struct {
	ID          string
	MintLimit   int
	RedeemLimit int
	Window      time.Duration
}

// SavePolicy upserts the throttle configuration.
func (s *Storage) SavePolicy(ctx context.Context, policy Policy) error {
	if s == nil {
		return fmt.Errorf("storage not configured")
	}
	if strings.TrimSpace(policy.ID) == "" {
		return fmt.Errorf("policy id required")
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO throttle_policy(id, mint_limit, redeem_limit, window_seconds, updated_at)
        VALUES(?, ?, ?, ?, CURRENT_TIMESTAMP)
        ON CONFLICT(id) DO UPDATE SET
            mint_limit=excluded.mint_limit,
            redeem_limit=excluded.redeem_limit,
            window_seconds=excluded.window_seconds,
            updated_at=CURRENT_TIMESTAMP
    `, policy.ID, policy.MintLimit, policy.RedeemLimit, int(policy.Window.Seconds()))
	if err != nil {
		return fmt.Errorf("save policy: %w", err)
	}
	return nil
}

// GetPolicy loads the throttle policy for the supplied identifier.
func (s *Storage) GetPolicy(ctx context.Context, id string) (Policy, error) {
	policy := Policy{ID: id}
	if s == nil {
		return policy, fmt.Errorf("storage not configured")
	}
	row := s.db.QueryRowContext(ctx, `
        SELECT mint_limit, redeem_limit, window_seconds
        FROM throttle_policy
        WHERE id = ?
    `, id)
	var windowSeconds int
	if err := row.Scan(&policy.MintLimit, &policy.RedeemLimit, &windowSeconds); err != nil {
		if err == sql.ErrNoRows {
			return policy, fmt.Errorf("policy not found")
		}
		return policy, fmt.Errorf("query policy: %w", err)
	}
	if windowSeconds > 0 {
		policy.Window = time.Duration(windowSeconds) * time.Second
	}
	return policy, nil
}

// ThrottleAction enumerates rate-limited flows.
type ThrottleAction string

const (
	// ActionMint identifies mint requests.
	ActionMint ThrottleAction = "mint"
	// ActionRedeem identifies redemption requests.
	ActionRedeem ThrottleAction = "redeem"
)

// CheckThrottle records the event if it does not exceed the configured limit.
func (s *Storage) CheckThrottle(ctx context.Context, policyID string, action ThrottleAction, limit int, window time.Duration, amount *big.Int, when time.Time) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("storage not configured")
	}
	if limit <= 0 {
		return true, nil
	}
	normalized := big.NewInt(0)
	if amount != nil {
		normalized = new(big.Int).Set(amount)
	}
	if normalized.Sign() < 0 {
		normalized = big.NewInt(0)
	}
	limitBig := big.NewInt(int64(limit))
	if normalized.Cmp(limitBig) > 0 {
		return false, nil
	}
	cutoff := when.Add(-window).Unix()
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `
        SELECT amount
        FROM throttle_events
        WHERE policy_id = ? AND action = ? AND occurred_at >= ?
    `, policyID, string(action), cutoff)
	if err != nil {
		return false, fmt.Errorf("query throttle events: %w", err)
	}
	defer rows.Close()
	used := big.NewInt(0)
	for rows.Next() {
		var stored string
		if err := rows.Scan(&stored); err != nil {
			return false, fmt.Errorf("scan throttle amount: %w", err)
		}
		stored = strings.TrimSpace(stored)
		if stored == "" {
			continue
		}
		amt := new(big.Int)
		if _, ok := amt.SetString(stored, 10); ok {
			used.Add(used, amt)
			continue
		}
		return false, fmt.Errorf("parse throttle amount: %q", stored)
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("iterate throttle events: %w", err)
	}
	remainder := new(big.Int).Sub(limitBig, used)
	if remainder.Sign() <= 0 {
		return false, nil
	}
	if remainder.Cmp(normalized) < 0 {
		return false, nil
	}
	if _, err := tx.ExecContext(ctx, `
        INSERT INTO throttle_events(policy_id, action, amount, occurred_at)
        VALUES(?, ?, ?, ?)
    `, policyID, string(action), normalized.String(), when.Unix()); err != nil {
		return false, fmt.Errorf("record event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit throttle: %w", err)
	}
	return true, nil
}

// SaveDailyUsage upserts the processed amount for the supplied UTC day.
func (s *Storage) SaveDailyUsage(ctx context.Context, day time.Time, amount int64) error {
	if s == nil {
		return fmt.Errorf("storage not configured")
	}
	day = day.UTC().Truncate(24 * time.Hour)
	if day.IsZero() {
		return fmt.Errorf("day required")
	}
	if amount < 0 {
		amount = 0
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO daily_usage(day, amount, updated_at)
        VALUES(?, ?, CURRENT_TIMESTAMP)
        ON CONFLICT(day) DO UPDATE SET
            amount=excluded.amount,
            updated_at=CURRENT_TIMESTAMP
    `, day.Format(time.DateOnly), amount)
	if err != nil {
		return fmt.Errorf("save daily usage: %w", err)
	}
	return nil
}

// LatestDailyUsage returns the most recent persisted usage record if present.
func (s *Storage) LatestDailyUsage(ctx context.Context) (time.Time, int64, bool, error) {
	if s == nil {
		return time.Time{}, 0, false, fmt.Errorf("storage not configured")
	}
	row := s.db.QueryRowContext(ctx, `
        SELECT day, amount
        FROM daily_usage
        ORDER BY day DESC
        LIMIT 1
    `)
	var dayStr string
	var amount int64
	if err := row.Scan(&dayStr, &amount); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, 0, false, nil
		}
		return time.Time{}, 0, false, fmt.Errorf("query daily usage: %w", err)
	}
	day, err := time.Parse(time.DateOnly, strings.TrimSpace(dayStr))
	if err != nil {
		return time.Time{}, 0, false, fmt.Errorf("parse daily usage day: %w", err)
	}
	return day, amount, true, nil
}
