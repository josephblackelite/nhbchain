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

	gatewayauth "nhbchain/gateway/auth"
	swap "nhbchain/native/swap"
)

// Storage wraps the swapd persistence layer.
type Storage struct {
	db *sql.DB
}

var (
	// ErrPathRequired is returned when the backing store path is missing.
	ErrPathRequired = errors.New("swapd storage path must be configured")

	// fallbackMemoryDSN is populated by explicit test builds that need an
	// in-memory database. Production binaries must provide an on-disk DSN.
	fallbackMemoryDSN string
)

// Open initialises the backing store using sqlite-compatible DSN.
func Open(path string) (*Storage, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		trimmed = fallbackMemoryDSN
		if trimmed == "" {
			return nil, ErrPathRequired
		}
	}
	db, err := sql.Open("sqlite", trimmed)
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

// EnsureNonce persists the API key nonce usage, returning true when the record
// was already present.
func (s *Storage) EnsureNonce(ctx context.Context, rec gatewayauth.NonceRecord) (bool, error) {
	if s == nil {
		return false, fmt.Errorf("storage not configured")
	}
	apiKey := strings.TrimSpace(rec.APIKey)
	ts := strings.TrimSpace(rec.Timestamp)
	nonce := strings.TrimSpace(rec.Nonce)
	if apiKey == "" || ts == "" || nonce == "" {
		return false, fmt.Errorf("nonce record incomplete")
	}
	observed := rec.ObservedAt.UTC()
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, `
        INSERT INTO api_nonce_usage(api_key, timestamp, nonce, observed_at)
        VALUES(?, ?, ?, ?)
        ON CONFLICT(api_key, timestamp, nonce) DO NOTHING
    `, apiKey, ts, nonce, observed)
	if err != nil {
		return false, fmt.Errorf("record api nonce: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("rows affected: %w", err)
	}
	return affected == 0, nil
}

// RecentNonces returns persisted API key nonces observed at or after the provided cutoff.
func (s *Storage) RecentNonces(ctx context.Context, cutoff time.Time) ([]gatewayauth.NonceRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("storage not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT api_key, timestamp, nonce, observed_at
        FROM api_nonce_usage
        WHERE observed_at >= ?
        ORDER BY observed_at ASC
    `, cutoff.UTC())
	if err != nil {
		return nil, fmt.Errorf("query api nonces: %w", err)
	}
	defer rows.Close()
	records := make([]gatewayauth.NonceRecord, 0)
	for rows.Next() {
		var rec gatewayauth.NonceRecord
		if err := rows.Scan(&rec.APIKey, &rec.Timestamp, &rec.Nonce, &rec.ObservedAt); err != nil {
			return nil, fmt.Errorf("scan api nonce: %w", err)
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate api nonces: %w", err)
	}
	return records, nil
}

// PruneNonces removes API key nonces observed before the cutoff.
func (s *Storage) PruneNonces(ctx context.Context, cutoff time.Time) error {
	if s == nil {
		return fmt.Errorf("storage not configured")
	}
	if _, err := s.db.ExecContext(ctx, `
        DELETE FROM api_nonce_usage
        WHERE observed_at < ?
    `, cutoff.UTC()); err != nil {
		return fmt.Errorf("prune api nonces: %w", err)
	}
	return nil
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

CREATE TABLE IF NOT EXISTS api_nonce_usage (
    api_key TEXT NOT NULL,
    timestamp TEXT NOT NULL,
    nonce TEXT NOT NULL,
    observed_at TIMESTAMP NOT NULL,
    PRIMARY KEY (api_key, timestamp, nonce)
);
CREATE INDEX IF NOT EXISTS idx_api_nonce_usage_observed ON api_nonce_usage(observed_at);

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

CREATE TABLE IF NOT EXISTS stable_ledger (
    asset TEXT PRIMARY KEY,
    available INTEGER NOT NULL,
    reserved INTEGER NOT NULL,
    payouts INTEGER NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE IF NOT EXISTS stable_reservations (
    id TEXT PRIMARY KEY,
    asset TEXT NOT NULL,
    amount_in INTEGER NOT NULL,
    amount_out INTEGER NOT NULL,
    price INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    account TEXT NOT NULL,
    intent_created INTEGER NOT NULL,
    intent_id TEXT,
    intent_created_at INTEGER,
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

// LedgerBalanceRecord captures the persisted treasury balances for an asset.
type LedgerBalanceRecord struct {
	Asset     string
	Available int64
	Reserved  int64
	Payouts   int64
	UpdatedAt time.Time
}

// SaveLedgerBalance upserts the treasury balances for the supplied asset.
func (s *Storage) SaveLedgerBalance(ctx context.Context, record LedgerBalanceRecord) error {
	if s == nil {
		return fmt.Errorf("storage not configured")
	}
	asset := strings.ToUpper(strings.TrimSpace(record.Asset))
	if asset == "" {
		return fmt.Errorf("asset required")
	}
	if record.Available < 0 {
		record.Available = 0
	}
	if record.Reserved < 0 {
		record.Reserved = 0
	}
	if record.Payouts < 0 {
		record.Payouts = 0
	}
	updatedAt := time.Now().UTC()
	if !record.UpdatedAt.IsZero() {
		updatedAt = record.UpdatedAt.UTC()
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO stable_ledger(asset, available, reserved, payouts, updated_at)
        VALUES(?, ?, ?, ?, ?)
        ON CONFLICT(asset) DO UPDATE SET
            available=excluded.available,
            reserved=excluded.reserved,
            payouts=excluded.payouts,
            updated_at=excluded.updated_at
    `, asset, record.Available, record.Reserved, record.Payouts, updatedAt)
	if err != nil {
		return fmt.Errorf("save ledger: %w", err)
	}
	return nil
}

// LoadLedgerBalances returns all persisted treasury balances.
func (s *Storage) LoadLedgerBalances(ctx context.Context) ([]LedgerBalanceRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("storage not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT asset, available, reserved, payouts, updated_at
        FROM stable_ledger
    `)
	if err != nil {
		return nil, fmt.Errorf("query ledger: %w", err)
	}
	defer rows.Close()
	var records []LedgerBalanceRecord
	for rows.Next() {
		var rec LedgerBalanceRecord
		if err := rows.Scan(&rec.Asset, &rec.Available, &rec.Reserved, &rec.Payouts, &rec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan ledger: %w", err)
		}
		rec.Asset = strings.ToUpper(strings.TrimSpace(rec.Asset))
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ledger: %w", err)
	}
	return records, nil
}

// ReservationRecord captures the state required to restore outstanding reservations.
type ReservationRecord struct {
	ID              string
	Asset           string
	AmountIn        int64
	AmountOut       int64
	Price           int64
	ExpiresAt       time.Time
	Account         string
	IntentCreated   bool
	IntentID        string
	IntentCreatedAt time.Time
	UpdatedAt       time.Time
}

// SaveReservation upserts the reservation record.
func (s *Storage) SaveReservation(ctx context.Context, record ReservationRecord) error {
	if s == nil {
		return fmt.Errorf("storage not configured")
	}
	id := strings.TrimSpace(record.ID)
	if id == "" {
		return fmt.Errorf("reservation id required")
	}
	asset := strings.ToUpper(strings.TrimSpace(record.Asset))
	if asset == "" {
		return fmt.Errorf("asset required")
	}
	if record.AmountIn <= 0 || record.AmountOut <= 0 {
		return fmt.Errorf("amounts must be positive")
	}
	if record.Price <= 0 {
		return fmt.Errorf("price must be positive")
	}
	account := strings.TrimSpace(record.Account)
	if account == "" {
		return fmt.Errorf("account required")
	}
	expiresAt := record.ExpiresAt.UTC()
	if record.ExpiresAt.IsZero() {
		expiresAt = time.Unix(0, 0).UTC()
	}
	intentCreated := 0
	if record.IntentCreated {
		intentCreated = 1
	}
	intentCreatedAt := int64(0)
	if !record.IntentCreatedAt.IsZero() {
		intentCreatedAt = record.IntentCreatedAt.UTC().Unix()
	}
	updatedAt := time.Now().UTC()
	if !record.UpdatedAt.IsZero() {
		updatedAt = record.UpdatedAt.UTC()
	}
	_, err := s.db.ExecContext(ctx, `
        INSERT INTO stable_reservations(id, asset, amount_in, amount_out, price, expires_at, account, intent_created, intent_id, intent_created_at, updated_at)
        VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            asset=excluded.asset,
            amount_in=excluded.amount_in,
            amount_out=excluded.amount_out,
            price=excluded.price,
            expires_at=excluded.expires_at,
            account=excluded.account,
            intent_created=excluded.intent_created,
            intent_id=excluded.intent_id,
            intent_created_at=excluded.intent_created_at,
            updated_at=excluded.updated_at
    `, id, asset, record.AmountIn, record.AmountOut, record.Price, expiresAt.Unix(), account, intentCreated, strings.TrimSpace(record.IntentID), intentCreatedAt, updatedAt)
	if err != nil {
		return fmt.Errorf("save reservation: %w", err)
	}
	return nil
}

// DeleteReservation removes the persisted reservation.
func (s *Storage) DeleteReservation(ctx context.Context, id string) error {
	if s == nil {
		return fmt.Errorf("storage not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("reservation id required")
	}
	if _, err := s.db.ExecContext(ctx, `
        DELETE FROM stable_reservations WHERE id = ?
    `, id); err != nil {
		return fmt.Errorf("delete reservation: %w", err)
	}
	return nil
}

// LoadReservations returns all persisted reservations.
func (s *Storage) LoadReservations(ctx context.Context) ([]ReservationRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("storage not configured")
	}
	rows, err := s.db.QueryContext(ctx, `
        SELECT id, asset, amount_in, amount_out, price, expires_at, account, intent_created, intent_id, intent_created_at, updated_at
        FROM stable_reservations
    `)
	if err != nil {
		return nil, fmt.Errorf("query reservations: %w", err)
	}
	defer rows.Close()
	var records []ReservationRecord
	for rows.Next() {
		var rec ReservationRecord
		var expiresAt int64
		var intentCreated int
		var intentCreatedAt sql.NullInt64
		if err := rows.Scan(&rec.ID, &rec.Asset, &rec.AmountIn, &rec.AmountOut, &rec.Price, &expiresAt, &rec.Account, &intentCreated, &rec.IntentID, &intentCreatedAt, &rec.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan reservation: %w", err)
		}
		rec.Asset = strings.ToUpper(strings.TrimSpace(rec.Asset))
		rec.Account = strings.TrimSpace(rec.Account)
		rec.IntentID = strings.TrimSpace(rec.IntentID)
		if expiresAt > 0 {
			rec.ExpiresAt = time.Unix(expiresAt, 0).UTC()
		}
		rec.IntentCreated = intentCreated != 0
		if intentCreatedAt.Valid && intentCreatedAt.Int64 > 0 {
			rec.IntentCreatedAt = time.Unix(intentCreatedAt.Int64, 0).UTC()
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate reservations: %w", err)
	}
	return records, nil
}
