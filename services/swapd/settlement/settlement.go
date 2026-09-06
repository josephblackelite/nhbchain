// Package settlement tracks how a swapd stable-swap cash-out intent is
// actually turned into moved money. Two rails are supported side by side:
//
//   - nowpayments: an automated payout submitted through NOWPayments' mass
//     withdrawal API. NOWPayments payouts require an operator-completed
//     email 2FA step on their dashboard before funds actually move, so a
//     successful CreatePayout call only ever reaches "submitted" here, never
//     "settled" -- an operator must confirm completion via ConfirmSettled
//     once they've verified the payout cleared.
//   - manual_treasury: no automated call at all. The intent sits "pending"
//     until an operator wires funds out of band (bank/SWIFT/ACH) and
//     confirms via ConfirmSettled with the wire reference as evidence.
//
// Which rail a given partner uses is a config-level choice (see
// Config.RailFor), not something this package decides -- it defaults to the
// safest option (manual_treasury) when unconfigured, since silently
// attempting an automated payout is worse than requiring an explicit choice.
package settlement

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"nhbchain/services/swapd/stable"
	"nhbchain/services/swapd/storage"
)

// Rail identifies which mechanism actually moves money for a settlement.
type Rail string

const (
	RailNowPayments    Rail = "nowpayments"
	RailManualTreasury Rail = "manual_treasury"
)

// Status tracks a settlement's lifecycle.
type Status string

const (
	StatusPending   Status = "pending"
	StatusSubmitted Status = "submitted"
	StatusSettled   Status = "settled"
	StatusFailed    Status = "failed"
)

var (
	ErrUnknownRail         = errors.New("settlement: unknown rail")
	ErrRailNotConfigured   = errors.New("settlement: rail not configured")
	ErrNotConfirmable      = errors.New("settlement: not in a confirmable state")
	ErrNotRetryable        = errors.New("settlement: not in a retryable state")
	ErrNotNowPayments      = errors.New("settlement: not a nowpayments settlement")
	ErrReceiptRequired     = errors.New("settlement: receipt reference required")
	ErrManagerUnconfigured = errors.New("settlement: manager not configured")
)

// Store is the narrow persistence surface Manager needs, mirroring the
// DailyUsageStore/LedgerReservationStore pattern already used by the stable
// engine: a package-local interface that *storage.Storage happens to
// satisfy, rather than a direct dependency on the concrete type.
type Store interface {
	SaveSettlement(ctx context.Context, record storage.SettlementRecord) error
	GetSettlement(ctx context.Context, id string) (storage.SettlementRecord, error)
	ListSettlements(ctx context.Context, partnerID, status string, limit int) ([]storage.SettlementRecord, error)
}

// PayoutRequest describes an automated payout to submit to a rail.
type PayoutRequest struct {
	SettlementID string
	PartnerID    string
	Asset        string
	Amount       float64
	Address      string
}

// PayoutResult captures the outcome of a submitted automated payout.
type PayoutResult struct {
	ExternalRef string
}

// PayoutClient is the subset of an automated-payout rail's behaviour
// Manager needs. The real implementation (NOWPayments) lives in
// nowpayments.go; tests substitute a fake.
type PayoutClient interface {
	CreatePayout(ctx context.Context, req PayoutRequest) (PayoutResult, error)
}

// Receipt captures operator-supplied evidence that a settlement actually
// completed in the real world -- a bank wire reference, a verified
// NOWPayments payout confirmation, etc.
type Receipt struct {
	Reference string
	Note      string
	Operator  string
}

// Config selects which rail handles a given partner's settlements.
type Config struct {
	DefaultRail  Rail
	PartnerRails map[string]Rail
}

// RailFor resolves the rail for a partner: a per-partner override if one is
// configured, otherwise the configured default, otherwise the safest
// fallback (manual_treasury -- never silently attempt an automated payout
// for a partner nobody explicitly configured).
func (c Config) RailFor(partnerID string) Rail {
	if c.PartnerRails != nil {
		if override, ok := c.PartnerRails[strings.TrimSpace(partnerID)]; ok && override != "" {
			return override
		}
	}
	if c.DefaultRail != "" {
		return c.DefaultRail
	}
	return RailManualTreasury
}

// InitiateRequest captures everything Manager needs to open a settlement
// for a freshly created cash-out intent.
type InitiateRequest struct {
	IntentID      string
	ReservationID string
	PartnerID     string
	Asset         string
	AmountUnits   int64
	Account       string
}

// Manager orchestrates settlement initiation, confirmation, retry, and
// failure across both rails, persisting durable state at every step.
type Manager struct {
	store       Store
	config      Config
	nowPayments PayoutClient
	now         func() time.Time
	idFunc      func() string

	// mu serializes every mutating operation (Initiate/ConfirmSettled/
	// RetryNowPayments/MarkFailed) across all settlement IDs. Each of those
	// methods is read-check-act-write (GetSettlement, inspect status,
	// possibly call an external payout API, SaveSettlement), and without
	// this lock two concurrent calls against the *same* settlement ID (an
	// operator double-clicking retry, two admin sessions) can both pass the
	// status check before either writes back -- for the nowpayments rail
	// that means two independent real payout submissions for one
	// settlement. This is a single coarse lock rather than per-ID locking
	// because settlement operations are low-throughput and operator/API-
	// triggered, not a hot path -- serializing all of them has no
	// meaningful performance cost and is simplest to reason about
	// correctly.
	mu sync.Mutex
}

// NewManager constructs a Manager. nowPayments may be nil if the
// nowpayments rail is never used by any partner -- Initiate/RetryNowPayments
// fail closed with ErrRailNotConfigured in that case rather than panicking.
func NewManager(store Store, config Config, nowPayments PayoutClient) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("settlement: store required")
	}
	return &Manager{
		store:       store,
		config:      config,
		nowPayments: nowPayments,
		now:         time.Now,
		idFunc:      defaultIDFunc,
	}, nil
}

// WithClock overrides the manager's time source, for deterministic tests.
func (m *Manager) WithClock(fn func() time.Time) {
	if m == nil || fn == nil {
		return
	}
	m.now = fn
}

// WithIDFunc overrides settlement ID generation, for deterministic tests.
func (m *Manager) WithIDFunc(fn func() string) {
	if m == nil || fn == nil {
		return
	}
	m.idFunc = fn
}

// defaultIDFunc combines a nanosecond timestamp with 4 random bytes.
// Timestamp alone is not collision-resistant under concurrent Initiate
// calls (multiple partners cashing out around the same moment, or coarse OS
// clock resolution) -- storage.SaveSettlement's ON CONFLICT(id) DO UPDATE
// would silently merge a colliding ID's status/external_ref onto an
// unrelated settlement's row, corrupting one partner's record with
// another's payout state.
func defaultIDFunc() string {
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		// crypto/rand failing is effectively unrecoverable for the process,
		// but fall back to the timestamp alone rather than panicking here.
		return fmt.Sprintf("settle-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("settle-%d-%s", time.Now().UnixNano(), hex.EncodeToString(suffix[:]))
}

// Initiate opens a settlement for a newly created cash-out intent, resolving
// the partner's rail and, for nowpayments, submitting the automated payout
// immediately. The settlement record is persisted before any external call
// is attempted, so a crash mid-call can never leave a payout with no local
// trace -- worst case is a record stuck "pending" that an operator can
// investigate, never a silently lost one.
func (m *Manager) Initiate(ctx context.Context, req InitiateRequest) (storage.SettlementRecord, error) {
	if m == nil {
		return storage.SettlementRecord{}, ErrManagerUnconfigured
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	rail := m.config.RailFor(req.PartnerID)
	now := m.now()
	record := storage.SettlementRecord{
		ID:            m.idFunc(),
		IntentID:      strings.TrimSpace(req.IntentID),
		ReservationID: strings.TrimSpace(req.ReservationID),
		PartnerID:     strings.TrimSpace(req.PartnerID),
		Asset:         strings.ToUpper(strings.TrimSpace(req.Asset)),
		AmountUnits:   req.AmountUnits,
		Account:       strings.TrimSpace(req.Account),
		Rail:          string(rail),
		Status:        string(StatusPending),
		Detail:        "{}",
		CreatedAt:     now,
		UpdatedAt:     now,
	}

	switch rail {
	case RailManualTreasury:
		if err := m.store.SaveSettlement(ctx, record); err != nil {
			return record, fmt.Errorf("settlement: persist pending record: %w", err)
		}
		return record, nil

	case RailNowPayments:
		if err := m.store.SaveSettlement(ctx, record); err != nil {
			return record, fmt.Errorf("settlement: persist pending record: %w", err)
		}
		if m.nowPayments == nil {
			return m.failLocked(ctx, record, ErrRailNotConfigured)
		}
		// This exact line -- logged BEFORE the call, not just on its outcome
		// -- is the gap a real incident exposed: a settlement stuck at
		// Pending with zero log output anywhere in its lifetime, leaving no
		// way to tell whether CreatePayout was ever even attempted. If the
		// process dies or this call hangs before returning, this line is
		// the only trace that it was tried at all.
		log.Printf("settlement: initiating nowpayments payout for settlement %s (partner=%s asset=%s account=%s amountUnits=%d)", record.ID, record.PartnerID, record.Asset, record.Account, req.AmountUnits)
		result, err := m.nowPayments.CreatePayout(ctx, PayoutRequest{
			SettlementID: record.ID,
			PartnerID:    record.PartnerID,
			Asset:        record.Asset,
			Amount:       fromUnitsFloat(req.AmountUnits),
			Address:      record.Account,
		})
		if err != nil {
			log.Printf("settlement: nowpayments CreatePayout failed for settlement %s: %v", record.ID, err)
			return m.failLocked(ctx, record, err)
		}
		log.Printf("settlement: nowpayments CreatePayout succeeded for settlement %s (external_ref=%s)", record.ID, result.ExternalRef)
		return m.submittedLocked(ctx, record, result)

	default:
		return m.failLocked(ctx, record, fmt.Errorf("%w: %q", ErrUnknownRail, rail))
	}
}

// submittedPersistRetries/Backoff bound submittedLocked's best-effort retry
// of the post-CreatePayout persist below. This is a real, already-verified
// (2FA-completed, per HTTPPayoutClient.CreatePayout) payout at this point --
// unlike every other persist in this package, silently giving up after one
// attempt here is what let a stuck settlement's on-disk state (Pending,
// empty ExternalRef) become indistinguishable from "CreatePayout was never
// even called", which is exactly the ambiguity an automated downstream
// reconciler (payments-gateway's reconcileStuckManualReview) cannot safely
// resolve on its own. A short in-process retry meaningfully shrinks (not
// eliminates -- see this function's doc comment for the residual case) how
// often a merely-transient local storage error (lock contention, a brief
// disk hiccup) produces that ambiguous state at all.
const submittedPersistRetries = 5
const submittedPersistBackoff = 200 * time.Millisecond

// submittedLocked persists a record after a successful CreatePayout call.
// Callers must hold m.mu. If every retry of the persist still fails, the
// external payout has already happened for real -- the returned error
// embeds the external reference directly (rather than discarding it) so it
// reaches whatever logs/audit trail the caller writes to, and the in-memory
// record (with the external ref and submitted status already set) is
// returned rather than a zero value, so a caller can still recover and
// display it even though the database write failed.
//
// IMPORTANT residual risk this retry reduces but does not eliminate: if the
// process is killed (not just a transient write error) at any point between
// CreatePayout returning and this persist finally succeeding, the on-disk
// settlement record is left exactly as Initiate's earlier SaveSettlement
// call wrote it -- Pending, empty ExternalRef -- indistinguishable from
// "CreatePayout was never called at all", even though the real payout has
// already been dispatched and 2FA-verified. No purely-local signal can
// disambiguate this case; a caller reconciling a stuck settlement long
// after the fact MUST NOT treat "still Pending, no ExternalRef" as proof no
// payout occurred without independently corroborating against NOWPayments'
// own records (or a human operator's judgment) -- see
// reconcileStuckManualReview's doc comment in
// services/payments-gateway/redeem_watcher.go for how that reconciler
// handles this.
func (m *Manager) submittedLocked(ctx context.Context, record storage.SettlementRecord, result PayoutResult) (storage.SettlementRecord, error) {
	record.ExternalRef = strings.TrimSpace(result.ExternalRef)
	record.Status = string(StatusSubmitted)
	record.Detail = "{}"
	var lastErr error
retryLoop:
	for attempt := 0; attempt < submittedPersistRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				lastErr = ctx.Err()
				break retryLoop
			case <-time.After(submittedPersistBackoff):
			}
		}
		// Stamped fresh on every attempt (not once before the loop) so a
		// write that only succeeds on a retry persists the time it actually
		// succeeded, not the time the loop started.
		record.UpdatedAt = m.now()
		if err := m.store.SaveSettlement(ctx, record); err != nil {
			lastErr = err
			log.Printf("settlement: persist submitted settlement %s (external_ref=%s) attempt %d/%d failed: %v", record.ID, record.ExternalRef, attempt+1, submittedPersistRetries, err)
			continue
		}
		log.Printf("settlement: settlement %s durably marked submitted (external_ref=%s)", record.ID, record.ExternalRef)
		return record, nil
	}
	log.Printf("settlement: CRITICAL: settlement %s already submitted (external_ref=%s) but failed to persist after %d attempts: %v -- this settlement's on-disk state cannot be trusted to mean \"no payout occurred\"", record.ID, record.ExternalRef, submittedPersistRetries, lastErr)
	return record, fmt.Errorf("settlement: CRITICAL: payout for settlement %s already submitted (external_ref=%s) but failed to persist after %d attempts -- manual reconciliation required, and this settlement's on-disk state cannot be trusted to mean \"no payout occurred\": %w", record.ID, record.ExternalRef, submittedPersistRetries, lastErr)
}

// failLocked persists a failure outcome. Callers must hold m.mu.
func (m *Manager) failLocked(ctx context.Context, record storage.SettlementRecord, cause error) (storage.SettlementRecord, error) {
	record.Status = string(StatusFailed)
	record.Detail = detailJSON(map[string]any{"error": cause.Error()})
	record.UpdatedAt = m.now()
	if saveErr := m.store.SaveSettlement(ctx, record); saveErr != nil {
		log.Printf("settlement: CRITICAL: settlement %s failed (%v) AND failed to persist that failure: %v -- this settlement's on-disk status may not reflect its real outcome", record.ID, cause, saveErr)
		return record, fmt.Errorf("settlement: %v (and failed to persist failure: %w)", cause, saveErr)
	}
	log.Printf("settlement: settlement %s durably marked failed: %v", record.ID, cause)
	return record, cause
}

// ConfirmSettled records operator-verified evidence that a settlement
// actually completed -- a manual_treasury settlement that was wired, or a
// nowpayments settlement whose payout was confirmed via NOWPayments'
// dashboard/2FA flow. Valid from pending or submitted only.
func (m *Manager) ConfirmSettled(ctx context.Context, settlementID string, receipt Receipt) (storage.SettlementRecord, error) {
	if m == nil {
		return storage.SettlementRecord{}, ErrManagerUnconfigured
	}
	reference := strings.TrimSpace(receipt.Reference)
	if reference == "" {
		return storage.SettlementRecord{}, ErrReceiptRequired
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, err := m.store.GetSettlement(ctx, settlementID)
	if err != nil {
		return storage.SettlementRecord{}, err
	}
	if record.Status != string(StatusPending) && record.Status != string(StatusSubmitted) {
		return storage.SettlementRecord{}, ErrNotConfirmable
	}
	now := m.now()
	record.Status = string(StatusSettled)
	record.ExternalRef = reference
	record.Detail = detailJSON(map[string]any{"note": receipt.Note, "operator": receipt.Operator})
	record.UpdatedAt = now
	record.SettledAt = now
	if err := m.store.SaveSettlement(ctx, record); err != nil {
		return record, fmt.Errorf("settlement: persist confirmation: %w", err)
	}
	return record, nil
}

// RetryNowPayments re-attempts a failed nowpayments payout submission.
func (m *Manager) RetryNowPayments(ctx context.Context, settlementID string) (storage.SettlementRecord, error) {
	if m == nil {
		return storage.SettlementRecord{}, ErrManagerUnconfigured
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, err := m.store.GetSettlement(ctx, settlementID)
	if err != nil {
		return storage.SettlementRecord{}, err
	}
	if record.Rail != string(RailNowPayments) {
		return storage.SettlementRecord{}, ErrNotNowPayments
	}
	if record.Status != string(StatusFailed) {
		return storage.SettlementRecord{}, ErrNotRetryable
	}
	if m.nowPayments == nil {
		return m.failLocked(ctx, record, ErrRailNotConfigured)
	}
	result, err := m.nowPayments.CreatePayout(ctx, PayoutRequest{
		SettlementID: record.ID,
		PartnerID:    record.PartnerID,
		Asset:        record.Asset,
		Amount:       fromUnitsFloat(record.AmountUnits),
		Address:      record.Account,
	})
	if err != nil {
		return m.failLocked(ctx, record, err)
	}
	return m.submittedLocked(ctx, record, result)
}

// MarkFailed lets an operator explicitly close out a stuck pending or
// submitted settlement (partner cancelled, wire bounced, payout rejected)
// rather than leaving it in limbo forever.
func (m *Manager) MarkFailed(ctx context.Context, settlementID, reason string) (storage.SettlementRecord, error) {
	if m == nil {
		return storage.SettlementRecord{}, ErrManagerUnconfigured
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	record, err := m.store.GetSettlement(ctx, settlementID)
	if err != nil {
		return storage.SettlementRecord{}, err
	}
	if record.Status != string(StatusPending) && record.Status != string(StatusSubmitted) {
		return storage.SettlementRecord{}, ErrNotConfirmable
	}
	record.Status = string(StatusFailed)
	record.Detail = detailJSON(map[string]any{"error": strings.TrimSpace(reason)})
	record.UpdatedAt = m.now()
	if err := m.store.SaveSettlement(ctx, record); err != nil {
		return record, fmt.Errorf("settlement: persist manual failure: %w", err)
	}
	return record, nil
}

// SetPartnerRail registers (or updates) a per-partner rail override at
// runtime -- e.g. so a newly-approved exchange agent's redemptions start
// routing to RailManualTreasury the moment an admin activates them, without
// requiring a process restart to pick up a static Config.PartnerRails map.
// Safe to call concurrently with Initiate/ConfirmSettled/etc (holds the same
// mutex as every other mutating method). A no-op if m is nil or partnerID is
// blank, matching this package's fail-quiet convention for the other
// zero-value-tolerant methods above.
func (m *Manager) SetPartnerRail(partnerID string, rail Rail) {
	if m == nil {
		return
	}
	id := strings.TrimSpace(partnerID)
	if id == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.config.PartnerRails == nil {
		m.config.PartnerRails = make(map[string]Rail)
	}
	m.config.PartnerRails[id] = rail
}

// List returns persisted settlements, optionally filtered by partner and/or
// status.
func (m *Manager) List(ctx context.Context, partnerID, status string, limit int) ([]storage.SettlementRecord, error) {
	if m == nil {
		return nil, ErrManagerUnconfigured
	}
	return m.store.ListSettlements(ctx, partnerID, status, limit)
}

func detailJSON(v map[string]any) string {
	payload, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(payload)
}

func fromUnitsFloat(units int64) float64 {
	return stable.FromAmountUnits(units)
}
