package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	swapdstorage "nhbchain/services/swapd/storage"
)

// Local redemption-watch statuses. See redeem_watcher.go's RedeemWatcher for
// the state machine these drive; kept here (not there) since they describe
// the shape of persisted data, mirroring how storage.go and server.go split
// persistence concerns from request-handling logic.
const (
	// redemptionStatusDiscovered marks a row just inserted from a fresh
	// swap_listPendingRedemptions read, before any validation or payout
	// attempt.
	redemptionStatusDiscovered = "discovered"
	// redemptionStatusInitiating marks a row durably committed to attempting
	// a payout -- written BEFORE calling settlement.Manager.Initiate, so a
	// process crash after this point is detectable on restart (see
	// RedeemWatcher.Recover) instead of silently vanishing. Also covers the
	// window between a settlement reaching Settled/Failed and the
	// corresponding attestRedemption transaction being durably recorded as
	// submitted (redemptionStatusAttesting) -- deliberately conservative:
	// any row found in this state on restart goes to
	// redemptionStatusStuckManualReview rather than being auto-resumed.
	redemptionStatusInitiating = "initiating"
	// redemptionStatusSkippedAlreadySettled marks a row whose fresh on-chain
	// re-read found the request no longer pending (already attested by
	// another watcher instance, or otherwise resolved) before this instance
	// ever called the payout API. Terminal; never processed again.
	redemptionStatusSkippedAlreadySettled = "skipped_already_settled"
	// redemptionStatusStuckManualReview marks a row a previous process left
	// in redemptionStatusInitiating -- proof of a crash/restart mid-payout.
	// Never auto-retried (a real payout may already be in flight -- retrying
	// could double-pay) and never silently ignored (a real payout may
	// already be settled and simply never got attested). Requires operator
	// intervention; the watcher never touches this row again on its own.
	redemptionStatusStuckManualReview = "stuck_manual_review"
	// redemptionStatusAttesting marks a row whose attestRedemption
	// transaction has been durably submitted (tx hash recorded) but not yet
	// confirmed on-chain.
	redemptionStatusAttesting = "attesting"
	// redemptionStatusAttested marks a row whose attestRedemption
	// transaction has been confirmed on-chain. Terminal.
	redemptionStatusAttested = "attested"
)

// redemptionOutcomePaid/Failed are the on-chain attestation statuses this
// service can submit -- see core/state_transition.go's applyAttestRedemption.
const (
	redemptionOutcomePaid   = "paid"
	redemptionOutcomeFailed = "failed"
)

// RedemptionWatchRecord tracks one NHB redemption (swap-out) request through
// discovery, payout, and on-chain attestation. PayoutAmountDecimal/Units are
// frozen once at discovery time (see computeRedemptionPayout in
// redeem_watcher.go) and never recomputed -- the whole point of freezing
// them is that every later step (settlement, attestation, audit) uses
// exactly the same number, computed exactly once, from the exact NHB amount
// that was actually burned.
type RedemptionWatchRecord struct {
	RequestID           string
	Account             string
	NHBAmountWei        string
	PayoutAmountDecimal string
	PayoutAmountUnits   int64
	DestinationAsset    string
	DestinationAddress  string
	LocalStatus         string
	SettlementID        string
	Outcome             string
	PayoutReference     string
	FailureReason       string
	AttestTxHash        string
	// AssignedAgentID is the exchange-agent partner ID (see exchange_agents /
	// ExchangeAgent) this redemption was routed to at discovery time, or ""
	// if none was active when the row was created -- in which case
	// processDiscovered falls back to redemptionSettlementPartnerID and the
	// payout continues through the automated NOWPayments rail exactly as it
	// did before this feature existed. Frozen once at discovery, same as
	// PayoutAmountDecimal/Units -- reassigning a request mid-flight would let
	// two different humans both believe they're responsible for the same
	// payout.
	AssignedAgentID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ExchangeAgent is a human "exchange agent" payments-gateway knows about for
// purposes of routing redemption settlements to RailManualTreasury (see
// redeem_watcher.go's assignAgent). This is a deliberately thin local mirror
// of nhbportal's ExchangeAgentAccount -- id, display name, active flag --
// kept in sync via POST /admin/agents whenever nhbportal approves,
// activates, or deactivates an agent. payments-gateway never needs to know
// anything else about an agent (bank details, USDT address, etc all stay in
// nhbportal); it only needs enough to pick a partner ID and keep its rail
// routing accurate.
type ExchangeAgent struct {
	ID        string
	Name      string
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// initRedemptionTables creates the redemption-payout tables if they don't
// already exist, mirroring storage.go's init()/CREATE TABLE IF NOT EXISTS
// pattern. Called explicitly by main.go (and by tests) after
// NewSQLiteStore, rather than folded into SQLiteStore.init() itself, so the
// redemption feature's schema stays isolated in this file -- consistent
// with the payout side otherwise never touching storage.go at all.
func (s *SQLiteStore) initRedemptionTables() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS redemption_watch (
            request_id             TEXT PRIMARY KEY,
            account                 TEXT NOT NULL,
            nhb_amount_wei          TEXT NOT NULL,
            payout_amount_decimal   TEXT NOT NULL DEFAULT '',
            payout_amount_units     INTEGER NOT NULL DEFAULT 0,
            destination_asset       TEXT NOT NULL,
            destination_address     TEXT NOT NULL,
            local_status            TEXT NOT NULL,
            settlement_id           TEXT NOT NULL DEFAULT '',
            outcome                 TEXT NOT NULL DEFAULT '',
            payout_reference        TEXT NOT NULL DEFAULT '',
            failure_reason          TEXT NOT NULL DEFAULT '',
            attest_tx_hash          TEXT NOT NULL DEFAULT '',
            created_at              TIMESTAMP NOT NULL,
            updated_at              TIMESTAMP NOT NULL
        );`,
		`CREATE INDEX IF NOT EXISTS idx_redemption_watch_status ON redemption_watch(local_status);`,
		// redemption_settlements gives services/swapd/settlement.Manager a
		// local home for its Pending->Submitted->Settled/Failed state
		// machine, shaped identically to swapd's own swap_settlements table
		// (see services/swapd/storage/storage.go's SettlementRecord) so the
		// SaveSettlement/GetSettlement/ListSettlements methods below can
		// satisfy settlement.Store by construction. This is a NEW table
		// local to payments-gateway -- swapd's actual database is never
		// touched by this service.
		`CREATE TABLE IF NOT EXISTS redemption_settlements (
            id             TEXT PRIMARY KEY,
            intent_id      TEXT NOT NULL,
            reservation_id TEXT NOT NULL,
            partner_id     TEXT NOT NULL,
            asset          TEXT NOT NULL,
            amount_units   INTEGER NOT NULL,
            account        TEXT NOT NULL,
            rail           TEXT NOT NULL,
            status         TEXT NOT NULL,
            external_ref   TEXT NOT NULL,
            detail         TEXT NOT NULL,
            created_at     TIMESTAMP NOT NULL,
            updated_at     TIMESTAMP NOT NULL,
            settled_at     TIMESTAMP
        );`,
		`CREATE INDEX IF NOT EXISTS idx_redemption_settlements_status ON redemption_settlements(status);`,
		`CREATE INDEX IF NOT EXISTS idx_redemption_settlements_intent ON redemption_settlements(intent_id);`,
		// exchange_agents: see ExchangeAgent's doc comment. Kept in its own
		// table (not folded into redemption_watch) since an agent's identity
		// outlives any single redemption and is written independently, via
		// POST /admin/agents, from an entirely separate call path.
		`CREATE TABLE IF NOT EXISTS exchange_agents (
            id         TEXT PRIMARY KEY,
            name       TEXT NOT NULL,
            active     INTEGER NOT NULL DEFAULT 1,
            created_at TIMESTAMP NOT NULL,
            updated_at TIMESTAMP NOT NULL
        );`,
		`CREATE INDEX IF NOT EXISTS idx_exchange_agents_active ON exchange_agents(active);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	// assigned_agent_id: added after redemption_watch's original release --
	// use ensureColumn (storage.go), not the CREATE TABLE above, so an
	// already-deployed database picks it up idempotently on next startup
	// rather than needing a manual migration step.
	if err := s.ensureColumn("redemption_watch", "assigned_agent_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return nil
}

// InsertRedemptionWatch inserts a new tracking row. Callers (see
// RedeemWatcher.discoverNew) are expected to have already checked
// GetRedemptionWatch for this request ID, so a primary-key collision here
// indicates a genuine race (two watcher ticks, or two instances) rather than
// the normal path -- surfaced as a plain error rather than a typed
// already-exists sentinel, since the caller's response either way is just
// "skip it, we'll pick it up correctly next tick."
func (s *SQLiteStore) InsertRedemptionWatch(ctx context.Context, rec RedemptionWatchRecord) error {
	const stmt = `INSERT INTO redemption_watch(
            request_id, account, nhb_amount_wei, payout_amount_decimal, payout_amount_units,
            destination_asset, destination_address, local_status, settlement_id, outcome,
            payout_reference, failure_reason, attest_tx_hash, assigned_agent_id, created_at, updated_at
        ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`
	_, err := s.db.ExecContext(ctx, stmt,
		rec.RequestID, rec.Account, rec.NHBAmountWei, rec.PayoutAmountDecimal, rec.PayoutAmountUnits,
		rec.DestinationAsset, rec.DestinationAddress, rec.LocalStatus, rec.SettlementID, rec.Outcome,
		rec.PayoutReference, rec.FailureReason, rec.AttestTxHash, rec.AssignedAgentID, rec.CreatedAt, rec.UpdatedAt)
	return err
}

// GetRedemptionWatch loads a tracking row by request ID, or returns
// (nil, nil) if none exists yet -- the discovery-time "have we seen this
// request before" check.
func (s *SQLiteStore) GetRedemptionWatch(ctx context.Context, requestID string) (*RedemptionWatchRecord, error) {
	const query = `SELECT request_id, account, nhb_amount_wei, payout_amount_decimal, payout_amount_units,
            destination_asset, destination_address, local_status, settlement_id, outcome,
            payout_reference, failure_reason, attest_tx_hash, assigned_agent_id, created_at, updated_at
        FROM redemption_watch WHERE request_id = ?`
	row := s.db.QueryRowContext(ctx, query, requestID)
	return scanRedemptionWatch(row)
}

// UpdateRedemptionWatch overwrites every mutable column of an existing row,
// keyed by request_id -- mirroring UpdatePayment's whole-row-update
// convention in storage.go. Callers always hold the full record already
// (fetched via GetRedemptionWatch or just-inserted), so this stays simple
// rather than growing a family of single-column setters.
func (s *SQLiteStore) UpdateRedemptionWatch(ctx context.Context, rec RedemptionWatchRecord) error {
	const stmt = `UPDATE redemption_watch SET
            account = ?, nhb_amount_wei = ?, payout_amount_decimal = ?, payout_amount_units = ?,
            destination_asset = ?, destination_address = ?, local_status = ?, settlement_id = ?,
            outcome = ?, payout_reference = ?, failure_reason = ?, attest_tx_hash = ?, assigned_agent_id = ?, updated_at = ?
        WHERE request_id = ?`
	_, err := s.db.ExecContext(ctx, stmt,
		rec.Account, rec.NHBAmountWei, rec.PayoutAmountDecimal, rec.PayoutAmountUnits,
		rec.DestinationAsset, rec.DestinationAddress, rec.LocalStatus, rec.SettlementID,
		rec.Outcome, rec.PayoutReference, rec.FailureReason, rec.AttestTxHash, rec.AssignedAgentID, rec.UpdatedAt,
		rec.RequestID)
	return err
}

// ListRedemptionWatchByStatus returns every tracking row currently in the
// given local_status, oldest-created first -- backs each phase of
// RedeemWatcher's per-tick sweep (discovered/initiating/attesting) and the
// startup crash-recovery scan (initiating).
func (s *SQLiteStore) ListRedemptionWatchByStatus(ctx context.Context, status string) ([]RedemptionWatchRecord, error) {
	const query = `SELECT request_id, account, nhb_amount_wei, payout_amount_decimal, payout_amount_units,
            destination_asset, destination_address, local_status, settlement_id, outcome,
            payout_reference, failure_reason, attest_tx_hash, assigned_agent_id, created_at, updated_at
        FROM redemption_watch WHERE local_status = ? ORDER BY created_at ASC, request_id ASC`
	rows, err := s.db.QueryContext(ctx, query, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RedemptionWatchRecord, 0)
	for rows.Next() {
		rec, err := scanRedemptionWatchRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// ListRedemptionWatchByAgent returns tracking rows, most-recently-created
// first, optionally filtered by agent ID and/or local_status (pass "" for
// either to mean "any"). Backs GET /admin/redemptions?agentId=..., which an
// exchange agent's dashboard uses (via nhbportal's server-side proxy,
// always passing a real agentId) to see only requests actually routed to
// them; an admin-facing overview could call it with agentId="" for a
// cross-agent view.
func (s *SQLiteStore) ListRedemptionWatchByAgent(ctx context.Context, agentID, status string, limit int) ([]RedemptionWatchRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	agent := strings.TrimSpace(agentID)
	statusFilter := strings.TrimSpace(status)
	const query = `SELECT request_id, account, nhb_amount_wei, payout_amount_decimal, payout_amount_units,
            destination_asset, destination_address, local_status, settlement_id, outcome,
            payout_reference, failure_reason, attest_tx_hash, assigned_agent_id, created_at, updated_at
        FROM redemption_watch
        WHERE (? = '' OR assigned_agent_id = ?) AND (? = '' OR local_status = ?)
        ORDER BY created_at DESC, request_id DESC
        LIMIT ?`
	rows, err := s.db.QueryContext(ctx, query, agent, agent, statusFilter, statusFilter, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]RedemptionWatchRecord, 0)
	for rows.Next() {
		rec, err := scanRedemptionWatchRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rec)
	}
	return out, rows.Err()
}

// CountAssignedRedemptionWatch returns how many redemption_watch rows
// currently have a non-empty assigned_agent_id -- the running counter
// assignAgent (redeem_watcher.go) uses to round-robin new requests fairly
// across active agents without needing any separate mutable counter state.
func (s *SQLiteStore) CountAssignedRedemptionWatch(ctx context.Context) (int, error) {
	const query = `SELECT COUNT(*) FROM redemption_watch WHERE assigned_agent_id <> ''`
	var count int
	if err := s.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

// UpsertExchangeAgent creates or updates an ExchangeAgent by ID -- called
// from POST /admin/agents whenever nhbportal approves, activates, or
// deactivates an exchange agent, keeping this service's local routing table
// in sync without either side needing shared database access.
func (s *SQLiteStore) UpsertExchangeAgent(ctx context.Context, agent ExchangeAgent) error {
	id := strings.TrimSpace(agent.ID)
	if id == "" {
		return fmt.Errorf("exchange agent id required")
	}
	name := strings.TrimSpace(agent.Name)
	updatedAt := agent.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	createdAt := agent.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = updatedAt
	}
	const stmt = `
        INSERT INTO exchange_agents(id, name, active, created_at, updated_at)
        VALUES(?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            name = excluded.name,
            active = excluded.active,
            updated_at = excluded.updated_at
    `
	_, err := s.db.ExecContext(ctx, stmt, id, name, boolToInt(agent.Active), createdAt, updatedAt)
	return err
}

// ListActiveExchangeAgentIDs returns every currently-active agent's ID,
// ordered for deterministic round-robin assignment (see assignAgent in
// redeem_watcher.go).
func (s *SQLiteStore) ListActiveExchangeAgentIDs(ctx context.Context) ([]string, error) {
	const query = `SELECT id FROM exchange_agents WHERE active = 1 ORDER BY id ASC`
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows, letting
// scanRedemptionWatch/scanRedemptionWatchRow share one Scan call shape.
type rowScanner interface {
	Scan(dest ...interface{}) error
}

func scanRedemptionWatch(row *sql.Row) (*RedemptionWatchRecord, error) {
	rec, err := scanRedemptionWatchRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return rec, err
}

func scanRedemptionWatchRow(row rowScanner) (*RedemptionWatchRecord, error) {
	var rec RedemptionWatchRecord
	if err := row.Scan(
		&rec.RequestID, &rec.Account, &rec.NHBAmountWei, &rec.PayoutAmountDecimal, &rec.PayoutAmountUnits,
		&rec.DestinationAsset, &rec.DestinationAddress, &rec.LocalStatus, &rec.SettlementID, &rec.Outcome,
		&rec.PayoutReference, &rec.FailureReason, &rec.AttestTxHash, &rec.AssignedAgentID, &rec.CreatedAt, &rec.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &rec, nil
}

// --- settlement.Store implementation -------------------------------------
//
// The three methods below let *SQLiteStore satisfy
// nhbchain/services/swapd/settlement.Store directly, so
// settlement.NewManager can be constructed with this same store instance --
// no adapter type needed. Validation and upsert semantics deliberately
// mirror services/swapd/storage.Storage.SaveSettlement/GetSettlement/
// ListSettlements exactly (same required fields, same ON CONFLICT upsert
// shape), since settlement.Manager's crash-safety guarantees depend on this
// contract behaving the same way regardless of which Store implementation
// backs it.

// SaveSettlement upserts a settlement record by ID, matching
// services/swapd/storage.Storage.SaveSettlement's validation and upsert
// semantics.
func (s *SQLiteStore) SaveSettlement(ctx context.Context, record swapdstorage.SettlementRecord) error {
	id := strings.TrimSpace(record.ID)
	if id == "" {
		return fmt.Errorf("settlement id required")
	}
	if strings.TrimSpace(record.IntentID) == "" {
		return fmt.Errorf("settlement intent id required")
	}
	rail := strings.TrimSpace(record.Rail)
	if rail == "" {
		return fmt.Errorf("settlement rail required")
	}
	status := strings.TrimSpace(record.Status)
	if status == "" {
		return fmt.Errorf("settlement status required")
	}
	detail := record.Detail
	if detail == "" {
		detail = "{}"
	}
	updatedAt := record.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = time.Now().UTC()
	}
	createdAt := record.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = updatedAt
	}
	var settledAt interface{}
	if !record.SettledAt.IsZero() {
		settledAt = record.SettledAt.UTC()
	}
	const stmt = `
        INSERT INTO redemption_settlements(id, intent_id, reservation_id, partner_id, asset, amount_units, account, rail, status, external_ref, detail, created_at, updated_at, settled_at)
        VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(id) DO UPDATE SET
            status=excluded.status,
            external_ref=excluded.external_ref,
            detail=excluded.detail,
            updated_at=excluded.updated_at,
            settled_at=excluded.settled_at
    `
	_, err := s.db.ExecContext(ctx, stmt, id, strings.TrimSpace(record.IntentID), strings.TrimSpace(record.ReservationID),
		strings.TrimSpace(record.PartnerID), strings.ToUpper(strings.TrimSpace(record.Asset)), record.AmountUnits,
		strings.TrimSpace(record.Account), rail, status, strings.TrimSpace(record.ExternalRef), detail,
		createdAt, updatedAt, settledAt)
	if err != nil {
		return fmt.Errorf("save settlement: %w", err)
	}
	return nil
}

// GetSettlement loads a single settlement record by ID.
func (s *SQLiteStore) GetSettlement(ctx context.Context, id string) (swapdstorage.SettlementRecord, error) {
	var rec swapdstorage.SettlementRecord
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return rec, fmt.Errorf("settlement id required")
	}
	const query = `
        SELECT id, intent_id, reservation_id, partner_id, asset, amount_units, account, rail, status, external_ref, detail, created_at, updated_at, settled_at
        FROM redemption_settlements
        WHERE id = ?
    `
	row := s.db.QueryRowContext(ctx, query, trimmed)
	var settledAt sql.NullTime
	if err := row.Scan(&rec.ID, &rec.IntentID, &rec.ReservationID, &rec.PartnerID, &rec.Asset, &rec.AmountUnits, &rec.Account,
		&rec.Rail, &rec.Status, &rec.ExternalRef, &rec.Detail, &rec.CreatedAt, &rec.UpdatedAt, &settledAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rec, fmt.Errorf("settlement not found")
		}
		return rec, fmt.Errorf("query settlement: %w", err)
	}
	if settledAt.Valid {
		rec.SettledAt = settledAt.Time
	}
	return rec, nil
}

// ListSettlements returns persisted settlements in reverse-chronological
// (by ID) order, optionally filtered by partner and/or status.
func (s *SQLiteStore) ListSettlements(ctx context.Context, partnerID, status string, limit int) ([]swapdstorage.SettlementRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	partner := strings.TrimSpace(partnerID)
	statusFilter := strings.TrimSpace(status)
	const query = `
        SELECT id, intent_id, reservation_id, partner_id, asset, amount_units, account, rail, status, external_ref, detail, created_at, updated_at, settled_at
        FROM redemption_settlements
        WHERE (? = '' OR partner_id = ?) AND (? = '' OR status = ?)
        ORDER BY id DESC
        LIMIT ?
    `
	rows, err := s.db.QueryContext(ctx, query, partner, partner, statusFilter, statusFilter, limit)
	if err != nil {
		return nil, fmt.Errorf("query settlements: %w", err)
	}
	defer rows.Close()
	var records []swapdstorage.SettlementRecord
	for rows.Next() {
		var rec swapdstorage.SettlementRecord
		var settledAt sql.NullTime
		if err := rows.Scan(&rec.ID, &rec.IntentID, &rec.ReservationID, &rec.PartnerID, &rec.Asset, &rec.AmountUnits, &rec.Account,
			&rec.Rail, &rec.Status, &rec.ExternalRef, &rec.Detail, &rec.CreatedAt, &rec.UpdatedAt, &settledAt); err != nil {
			return nil, fmt.Errorf("scan settlement: %w", err)
		}
		if settledAt.Valid {
			rec.SettledAt = settledAt.Time
		}
		records = append(records, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate settlements: %w", err)
	}
	return records, nil
}

// settlementFailureReason extracts the human-readable reason
// settlement.Manager.failLocked/MarkFailed records in Detail (a JSON object
// with an "error" field -- see settlement.detailJSON), falling back to a
// generic message if Detail is empty/unparseable so attestRedemption(failed)
// never gets an empty failureReason.
func settlementFailureReason(rec swapdstorage.SettlementRecord) string {
	var detail struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(rec.Detail), &detail); err == nil && strings.TrimSpace(detail.Error) != "" {
		return strings.TrimSpace(detail.Error)
	}
	return "payout failed"
}
