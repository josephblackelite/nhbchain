package main

import (
	"context"
	"fmt"
	"log"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcutil/base58"

	"nhbchain/services/swapd/settlement"
	swapdstorage "nhbchain/services/swapd/storage"
)

const (
	// redemptionNHBDecimals is NHB's on-chain fixed-point precision --
	// matches mintDecimals (server.go) and the wallet's own
	// parseUnits(amount, 18)/formatUnits(balance, 18) convention.
	redemptionNHBDecimals = 18

	// redemptionSettlementScale is settlement.AmountUnits' fixed-point
	// scale (see services/swapd/stable's amountScale = 1_000_000, i.e. 6
	// decimal places) -- USDT-TRC20's own on-chain precision.
	redemptionSettlementScale = int64(1_000_000)

	// redemptionSettlementPartnerID is the fixed partner identifier used for
	// any redemption with no assigned exchange agent (see partnerIDFor) --
	// the same partner ID every redemption used before the exchange-agent
	// feature existed. main.go never adds a PartnerRails override for this
	// ID, so it always resolves to Config.DefaultRail (RailNowPayments): the
	// automated payout path. Assigned redemptions instead pass their agent's
	// own ID as PartnerID, which main.go/server.go register against
	// RailManualTreasury via settlement.Manager.SetPartnerRail as agents are
	// activated.
	redemptionSettlementPartnerID = "nhb-redeem-nhb"

	// tronAddressVersion is TRON's base58check version byte (mainnet).
	tronAddressVersion = 0x41
	// tronAddressLength is the fixed length of a TRON base58check address
	// string, e.g. "T9yD14Nj9j7xAB4dbGeiX9h8unkKHxuWwb".
	tronAddressLength = 34
	// tronAddressPayloadLength is the decoded (post-checksum-strip) payload
	// length: a 20-byte address hash.
	tronAddressPayloadLength = 20
)

// computeRedemptionPayout converts a redemption's burned NHB amount (wei,
// 18 decimals) into the USDT payout amount, both as a full-precision
// human-readable decimal string (for the audit trail) and as
// settlement.AmountUnits (a 6-decimal fixed-point int64, per
// services/swapd/stable's amountScale).
//
// Per the plan's "redeemer bears all NOWPayments fees, zero cost exposure to
// NHBCoin" rule, payout amount is strictly NHBAmountWei / 1e18 -- no
// separate platform fee. Arithmetic uses math/big exclusively (never float
// parsing of the wei string), and the settlement-unit conversion always
// rounds DOWN (floor, never up): the custody wallet holds exactly 1 USDT for
// every 1 NHB in circulation, so paying out more than a burn is worth would
// overpay it. Any fractional USDT below the 6-decimal settlement floor
// (e.g. a burn of 1 wei NHB, worth 1e-12 USDT) is not paid out at all --
// units will be 0, which the caller must treat as "cannot pay this
// request" (see redeem_watcher.go's discovered-row handling), never as "pay
// zero and call it done."
func computeRedemptionPayout(nhbAmountWei string) (decimalAmount string, units int64, err error) {
	trimmed := strings.TrimSpace(nhbAmountWei)
	weiInt, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return "", 0, fmt.Errorf("invalid NHB amount wei: %q", nhbAmountWei)
	}
	if weiInt.Sign() <= 0 {
		return "", 0, fmt.Errorf("NHB amount wei must be positive: %q", nhbAmountWei)
	}
	weiScale := new(big.Int).Exp(big.NewInt(10), big.NewInt(redemptionNHBDecimals), nil)
	amountRat := new(big.Rat).SetFrac(weiInt, weiScale)

	// Freeze a full-precision, human-readable decimal string for the audit
	// trail (up to 18 fractional digits -- NHB wei's own precision).
	decimalAmount = formatRat(amountRat, redemptionNHBDecimals)

	// Settlement units: floor to redemptionSettlementScale precision. Using
	// big.Int.Quo (truncating division) on a non-negative numerator/
	// denominator pair is exactly a floor -- never rounds up, never
	// overpays.
	scaledRat := new(big.Rat).Mul(amountRat, new(big.Rat).SetInt64(redemptionSettlementScale))
	unitsBig := new(big.Int).Quo(scaledRat.Num(), scaledRat.Denom())
	if !unitsBig.IsInt64() {
		return decimalAmount, 0, fmt.Errorf("payout amount overflows settlement units")
	}
	units = unitsBig.Int64()
	if units <= 0 {
		return decimalAmount, 0, fmt.Errorf("payout amount %s rounds to zero at the settlement rail's minimum payable unit (%d decimals)", decimalAmount, 6)
	}
	return decimalAmount, units, nil
}

// nowPaymentsPayoutCurrency maps a redemption's destination asset label to
// the exact currency code NOWPayments' mass-payout API expects. NOWPayments
// has no generic "USDT" currency at all -- every stablecoin is
// network-qualified (USDTERC20, USDTTRC20, USDTBSC, ...; confirmed live
// against GET /v1/full-currencies on 2026-08-24). This service only ever
// validates and pays out to TRC20 addresses (see isValidTRC20Address), so a
// bare "USDT" always means TRC20 here -- but on-chain redemption requests
// only ever specify the bare label. Sending "usdt" as-is silently resolved
// to a different network's address format on NOWPayments' side and rejected
// two real, valid TRC20 addresses with "Invalid payout address" before this
// mapping existed. Anything already network-qualified passes through
// unchanged.
func nowPaymentsPayoutCurrency(asset string) string {
	if strings.EqualFold(strings.TrimSpace(asset), "USDT") {
		return "USDTTRC20"
	}
	return asset
}

// isValidTRC20Address performs a basic TRON/TRC20 address format check:
// base58check-decodable, mainnet version byte (0x41), and a 20-byte payload
// -- catching the overwhelmingly common failure mode (a typo, a copy-pasted
// address for the wrong chain, an EVM/base58-incompatible string) before
// ever calling the payout API, which would reject it anyway but only after
// spending a real NOWPayments API call. This is a format check only, not
// proof the address is actually controlled by anyone or able to receive
// funds.
func isValidTRC20Address(address string) bool {
	trimmed := strings.TrimSpace(address)
	if len(trimmed) != tronAddressLength || !strings.HasPrefix(trimmed, "T") {
		return false
	}
	payload, version, err := base58.CheckDecode(trimmed)
	if err != nil {
		return false
	}
	return version == tronAddressVersion && len(payload) == tronAddressPayloadLength
}

// PayoutStatusChecker polls a rail directly for a batch's real-world status,
// bypassing local bookkeeping entirely -- the same information an operator
// would see by checking the NOWPayments dashboard by hand, just automated.
// *settlement.HTTPPayoutClient satisfies this via its GetPayoutStatus
// method. Kept as a narrow interface (rather than depending on the concrete
// type) so tests can substitute a fake, and so a deployment that hasn't
// configured a status-checker can pass nil and fall back to the admin
// confirm-payout/fail-payout endpoints as the only path to resolution.
type PayoutStatusChecker interface {
	GetPayoutStatus(ctx context.Context, externalRef string) (string, error)
}

// RedeemWatcher drives NHB redemption (swap-out) requests from on-chain burn
// through NOWPayments payout to on-chain attestation. It is a single
// sequential ticker loop, deliberately never run concurrently with itself:
// the attestor's on-chain nonce is fetched once (see node_client.go's
// RPCNodeClient.InitAttestor) and incremented locally on every successful
// submission, which is only safe under single-writer access.
type RedeemWatcher struct {
	store         *SQLiteStore
	node          NodeClient
	settlement    *settlement.Manager
	statusChecker PayoutStatusChecker
	interval      time.Duration
	nowFn         func() time.Time

	// mu serializes every tick (runOnce) against the operator-triggered
	// ConfirmPayout/FailPayout/RetryPayout methods (called from server.go's
	// admin endpoints). Without this, an admin call and an in-flight tick
	// could both read a row's status before either writes back -- e.g. a
	// tick decides to attest a settlement failed at the same moment an
	// operator's retry-payout call decides the same settlement is safely
	// retryable, and the retried (real, money-moving) payout ends up
	// orphaned by the tick's already-in-flight failed attestation landing
	// afterward. A single coarse lock is deliberate, mirroring
	// settlement.Manager.mu's own reasoning: these are low-throughput,
	// operator/ticker-triggered operations, not a hot path, so serializing
	// all of them has no meaningful cost and is simplest to reason about
	// correctly.
	mu sync.Mutex

	// stuckReviewSafetyMargin gates reconcileStuckManualReview -- see its
	// doc comment. Defaults to defaultStuckReviewSafetyMargin; override via
	// WithStuckReviewSafetyMargin.
	stuckReviewSafetyMargin time.Duration

	// stuckReviewAlerted tracks, per requestID, the last time
	// reconcileStuckManualReview logged its CRITICAL action-needed alert --
	// so an unresolved row re-alerts periodically (stuckReviewAlertInterval)
	// rather than either spamming every tick or (the bug this replaces)
	// silently auto-acting exactly once. In-memory only and reset on
	// restart is intentional: a restart is itself a reasonable moment to
	// re-surface any still-unresolved manual-review item. Only ever
	// accessed from runOnce's locked pipeline (single-threaded), so no
	// separate mutex is needed.
	stuckReviewAlerted map[string]time.Time

	// notifier reports a redemption's confirmed on-chain outcome to nhbportal
	// for customer email notification. Optional (nil is a valid, inert
	// state) -- see WithNotifier and processAttesting's call site.
	notifier RedemptionNotifier
}

// NewRedeemWatcher constructs a RedeemWatcher. interval <= 0 falls back to
// defaultRedeemWatcherInterval. statusChecker may be nil -- a submitted
// settlement then only ever advances via an operator calling the
// confirm-payout/fail-payout admin endpoints, exactly as before this field
// existed.
func NewRedeemWatcher(store *SQLiteStore, node NodeClient, mgr *settlement.Manager, statusChecker PayoutStatusChecker, interval time.Duration) *RedeemWatcher {
	if interval <= 0 {
		interval = defaultRedeemWatcherInterval
	}
	return &RedeemWatcher{
		store:                   store,
		node:                    node,
		settlement:              mgr,
		statusChecker:           statusChecker,
		interval:                interval,
		nowFn:                   time.Now,
		stuckReviewSafetyMargin: defaultStuckReviewSafetyMargin,
	}
}

// WithStuckReviewSafetyMargin overrides how long a stuck_manual_review row's
// settlement must sit exactly Pending (no external ref) before
// reconcileStuckManualReview will auto-resolve it -- see that method's doc
// comment for the safety reasoning behind the default. Mirrors settlement.
// Manager.WithClock/WithIDFunc's post-construction-override shape so every
// existing NewRedeemWatcher call site (13 across this package's tests, plus
// main.go) keeps working unchanged.
func (w *RedeemWatcher) WithStuckReviewSafetyMargin(d time.Duration) {
	if w == nil || d <= 0 {
		return
	}
	w.stuckReviewSafetyMargin = d
}

// WithNotifier wires an optional RedemptionNotifier -- see processAttesting's
// call site for exactly when and how it's invoked, and its own doc comment
// for why a failure there is logged, never retried, and never allowed to
// affect the state machine. A nil argument (the default, e.g. no notify URL
// configured) leaves the watcher exactly as it behaved before this feature
// existed.
func (w *RedeemWatcher) WithNotifier(n RedemptionNotifier) {
	if w == nil {
		return
	}
	w.notifier = n
}

// Recover runs once at startup, BEFORE Run's ticker loop starts. Any row
// still in redemptionStatusInitiating is proof a previous process crashed
// (or was killed) somewhere between committing to a payout attempt and
// durably recording its outcome -- see redemptionStatusInitiating's doc
// comment in redeem_storage.go for why this is never auto-retried or
// silently ignored. This is the single most safety-critical behavior in the
// whole feature: an untested version of this could either double-pay a
// redeemer (auto-retry) or strand a real payout unattested forever (ignore
// it). A row parked here by this method is not necessarily stuck forever,
// though: every subsequent tick's reconcileStuckManualReview re-examines it
// and safely auto-resolves the common case (settlement never even reached
// NOWPayments) once enough time has passed -- see that method's doc
// comment. Only a genuinely ambiguous row (settlement has a real external
// reference) waits on a human indefinitely.
func (w *RedeemWatcher) Recover(ctx context.Context) error {
	stuck, err := w.store.ListRedemptionWatchByStatus(ctx, redemptionStatusInitiating)
	if err != nil {
		return fmt.Errorf("redeem watcher: recover: list initiating rows: %w", err)
	}
	for i := range stuck {
		row := stuck[i]
		row.LocalStatus = redemptionStatusStuckManualReview
		row.UpdatedAt = w.nowFn().UTC()
		if err := w.store.UpdateRedemptionWatch(ctx, row); err != nil {
			log.Printf("payments-gateway: redeem watcher: CRITICAL: failed to move stuck request %s to stuck_manual_review: %v -- manual database intervention required", row.RequestID, err)
			continue
		}
		log.Printf("payments-gateway: redeem watcher: CRITICAL: redemption request %s was left in %q by a previous process crash/restart (settlement_id=%q) -- moved to stuck_manual_review; an operator must verify payout status (via the settlement record and/or the NOWPayments dashboard) before this can proceed further", row.RequestID, redemptionStatusInitiating, row.SettlementID)
	}
	return nil
}

// Run starts the ticker loop and blocks until ctx is cancelled, mirroring
// reconciler.go's runPaymentReconciler shape. Callers must call Recover
// first.
func (w *RedeemWatcher) Run(ctx context.Context) {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

// runOnce processes one full tick: discover newly-burned requests, then
// advance every row currently awaiting action. Never called concurrently
// with itself (Run's single select loop guarantees this). The locked
// pipeline (runLockedTick) is serialized against ConfirmPayout/FailPayout/
// RetryPayout via w.mu -- see its doc comment -- but customer-notification
// delivery deliberately happens AFTER that lock is released (see
// processAttesting's doc comment): an audit found that firing Notify() while
// still holding w.mu let a slow or hanging notifier block every admin
// action and every other row in the same tick, contradicting this
// feature's own "never blocks the state machine" design.
func (w *RedeemWatcher) runOnce(ctx context.Context) {
	events := w.runLockedTick(ctx)
	if w.notifier == nil {
		return
	}
	for _, event := range events {
		if err := w.notifier.Notify(ctx, event); err != nil {
			log.Printf("payments-gateway: redeem watcher: notify request %s outcome (non-critical, customer email may not have been sent): %v", event.RequestID, err)
		}
	}
}

// runLockedTick holds w.mu for exactly the state-mutating part of a tick and
// returns any confirmed-attestation events still needing a customer
// notification -- see runOnce's doc comment for why notification delivery
// itself happens outside this lock.
func (w *RedeemWatcher) runLockedTick(ctx context.Context) []RedemptionOutcomeEvent {
	w.mu.Lock()
	defer w.mu.Unlock()
	pending, err := w.node.ListPendingRedemptions(ctx)
	if err != nil {
		log.Printf("payments-gateway: redeem watcher: list pending redemptions: %v", err)
		return nil
	}
	w.discoverNew(ctx, pending)
	w.processDiscovered(ctx)
	w.processInitiating(ctx)
	events := w.processAttesting(ctx)
	w.reconcileStuckManualReview(ctx)
	return events
}

// reconcileStuckManualReview does NOT auto-resolve anything on-chain -- see
// the correction below; it only detects and loudly, repeatedly (once per
// stuckReviewAlertInterval, not once and never again) surfaces a
// stuck_manual_review row (see Recover's doc comment for how a row gets
// here) once its pattern is CONSISTENT WITH -- but does not prove -- no
// NOWPayments payout ever having been created for it. Before this method
// existed, the only path to resolving one was an operator manually
// inspecting the redemption_settlements table and the NOWPayments dashboard
// by hand with no prompting at all; this method automates the DIAGNOSIS
// (so an operator doesn't need to know to go looking) while leaving the
// ACTION -- which is genuinely irreversible and money-affecting -- to the
// existing, already-authenticated fail-payout admin endpoint.
//
// CORRECTED, 2026-09-06, by an adversarial security audit: an earlier
// version of this method called settlement.MarkFailed + attested "failed"
// (triggering core/state_transition.go's applyAttestRedemption on-chain
// refund) automatically once this pattern held for stuckReviewSafetyMargin.
// That was a real, confirmed bug: settlement.Manager.Initiate's own
// submittedLocked step (services/swapd/settlement/settlement.go) persists
// Submitted+ExternalRef ONLY AFTER CreatePayout already returned success --
// and since NOWPayments 2FA/TOTP is configured live, CreatePayout returning
// success means the real payout was ALREADY DISPATCHED AND VERIFIED, not
// merely "batch created". If THAT persist step itself fails or the process
// is killed in the narrow window between CreatePayout returning and the
// persist completing (submittedLocked now retries this -- see its doc
// comment -- but a retry only shrinks, never eliminates, this window), the
// on-disk settlement is left at Pending/no-ExternalRef: bit-for-bit
// indistinguishable from "CreatePayout was never called at all". No purely
// local signal can tell these two cases apart. Auto-triggering a refund on
// this signal could therefore credit NHB back on top of a real payout that
// already went out -- a genuine double-credit reachable through an ordinary
// infrastructure hiccup, not just a compromised attestor key. There is
// currently no NOWPayments API available to this codebase that can search
// for a payout by anything other than a batch ID we don't have in this
// exact failure case, so this ambiguity cannot be safely resolved
// automatically today -- a human checking NOWPayments' own dashboard (or
// waiting for a delayed real response to surface some other way) is the
// only sound resolution, exactly as Recover()'s original, more
// conservative design already assumed before this feature first added
// (then had to remove) automatic action here.
//
// EXTENDED, same day, by a follow-up verification pass on the fix above: the
// first corrected version only ever alerted on the "safe-looking" pattern
// (pending, no external ref, aged past the margin). A row whose settlement
// carries a REAL external reference -- the genuinely higher-stakes case,
// since a real payout may already be dispatched or fully verified -- got
// exactly one CRITICAL log line from Recover() at the moment it was first
// parked, then silence forever: it could sit unresolved indefinitely with
// zero further prompting, undermining the entire point of this alerting
// mechanism for precisely the case where getting it wrong matters most. This
// method now alerts on every stuck_manual_review row needing attention,
// with a message tailored to what's actually known about it, all on the
// same rate-limited cadence.
func (w *RedeemWatcher) reconcileStuckManualReview(ctx context.Context) {
	rows, err := w.store.ListRedemptionWatchByStatus(ctx, redemptionStatusStuckManualReview)
	if err != nil {
		log.Printf("payments-gateway: redeem watcher: list stuck_manual_review rows: %v", err)
		return
	}
	now := w.nowFn().UTC()

	// Prune resolved rows out of the alert map -- once a requestID is no
	// longer stuck_manual_review (an operator acted on it), there is no
	// reason to keep remembering it for the rest of this process's
	// lifetime. Keeps this map's size bounded by "currently stuck", not
	// "ever been stuck since the last restart".
	if len(w.stuckReviewAlerted) > 0 {
		stillStuck := make(map[string]struct{}, len(rows))
		for _, row := range rows {
			stillStuck[row.RequestID] = struct{}{}
		}
		for id := range w.stuckReviewAlerted {
			if _, ok := stillStuck[id]; !ok {
				delete(w.stuckReviewAlerted, id)
			}
		}
	}

	for _, row := range rows {
		if strings.TrimSpace(row.SettlementID) == "" {
			continue
		}
		rec, err := w.store.GetSettlement(ctx, row.SettlementID)
		if err != nil {
			log.Printf("payments-gateway: redeem watcher: load settlement %s for stuck request %s: %v", row.SettlementID, row.RequestID, err)
			continue
		}

		hasRef := strings.TrimSpace(rec.ExternalRef) != ""
		pending := rec.Status == string(settlement.StatusPending)
		agedPastMargin := now.Sub(rec.CreatedAt.UTC()) >= w.stuckReviewSafetyMargin

		var message string
		switch {
		case pending && !hasRef && !agedPastMargin:
			// Still within the window a genuinely in-flight CreatePayout call
			// could occupy -- Recover() already logged once when this row was
			// first parked; no need to nag again yet.
			continue
		case pending && !hasRef && agedPastMargin:
			message = fmt.Sprintf("this pattern is CONSISTENT WITH (but does not prove) no NOWPayments payout ever having been dispatched -- has been stuck for over %s with its settlement still exactly pending and no external reference. Verify directly against NOWPayments (dashboard or GetPayoutStatus, if a batch ID can be found any other way) before acting. If confirmed no payout occurred: POST /admin/redemptions/%s/fail-payout to close it out -- this will automatically refund the burned NHB on-chain. If a payout DID occur: POST /admin/redemptions/%s/confirm-payout instead.", w.stuckReviewSafetyMargin.Round(time.Minute), row.RequestID, row.RequestID)
		case pending && hasRef:
			// The genuinely ambiguous, highest-stakes case: CreatePayout's
			// HTTP call actually completed and NOWPayments acknowledged a
			// real batch (external ref present) -- a payout may already be
			// dispatched and even fully verified. This needs a human to
			// check NOWPayments directly, not a timer; alerted on every
			// cycle (rate-limited the same as every other case) rather than
			// only once at the moment Recover() first parked it, so it can
			// never silently fall out of view for the rest of this
			// process's lifetime.
			message = fmt.Sprintf("its settlement has a REAL external reference (%s) -- a real payout may already be dispatched or fully verified by NOWPayments. Check NOWPayments' dashboard/GetPayoutStatus for this reference NOW. If verified paid: POST /admin/redemptions/%s/confirm-payout. If verified NOT paid/rejected: POST /admin/redemptions/%s/fail-payout (this will automatically refund the burned NHB on-chain).", rec.ExternalRef, row.RequestID, row.RequestID)
		default:
			// Settlement already resolved (Settled/Failed) by some other
			// path (e.g. a direct settlement-level action) but this row was
			// never resumed back out of stuck_manual_review -- also urgent:
			// the local bookkeeping has fallen behind a real outcome.
			message = fmt.Sprintf("its settlement already resolved to status=%q but this request was never resumed out of stuck_manual_review -- call the matching admin endpoint (/admin/redemptions/%s/confirm-payout or /fail-payout) to bring local state back in sync.", rec.Status, row.RequestID)
		}

		if last, alerted := w.stuckReviewAlerted[row.RequestID]; alerted && now.Sub(last) < stuckReviewAlertInterval {
			continue
		}
		if w.stuckReviewAlerted == nil {
			w.stuckReviewAlerted = make(map[string]time.Time)
		}
		w.stuckReviewAlerted[row.RequestID] = now
		log.Printf("payments-gateway: redeem watcher: CRITICAL: ACTION NEEDED: redemption request %s (settlement %s, account %s, %s NHB) -- %s Never guess.", row.RequestID, rec.ID, row.Account, row.PayoutAmountDecimal, message)
	}
}

// discoverNew inserts a local tracking row (status discovered) for every
// on-chain pending request not already known locally, freezing the payout
// amount exactly once at insertion time.
func (w *RedeemWatcher) discoverNew(ctx context.Context, pending []RedemptionRequest) {
	now := w.nowFn().UTC()
	for _, req := range pending {
		if !strings.EqualFold(strings.TrimSpace(req.Status), "pending") {
			// Defensive: swap_listPendingRedemptions should only ever return
			// pending requests (see core/state/redemption.go's pending
			// index), but never treat a non-pending entry as fresh.
			continue
		}
		requestID := strings.TrimSpace(req.RequestID)
		if requestID == "" {
			continue
		}
		existing, err := w.store.GetRedemptionWatch(ctx, requestID)
		if err != nil {
			log.Printf("payments-gateway: redeem watcher: check existing request %s: %v", requestID, err)
			continue
		}
		if existing != nil {
			continue
		}
		rec := RedemptionWatchRecord{
			RequestID:          requestID,
			Account:            strings.TrimSpace(req.Account),
			NHBAmountWei:       strings.TrimSpace(req.NHBAmountWei),
			DestinationAsset:   nowPaymentsPayoutCurrency(strings.ToUpper(strings.TrimSpace(req.DestinationAsset))),
			DestinationAddress: strings.TrimSpace(req.DestinationAddress),
			LocalStatus:        redemptionStatusDiscovered,
			CreatedAt:          now,
			UpdatedAt:          now,
		}
		decimalAmount, units, computeErr := computeRedemptionPayout(req.NHBAmountWei)
		if computeErr != nil {
			// Freeze the failure itself: processDiscovered routes any row
			// with PayoutAmountUnits <= 0 straight to attestRedemption(failed)
			// without ever attempting a payout, using FailureReason as the
			// on-chain failure reason.
			rec.FailureReason = computeErr.Error()
		} else {
			rec.PayoutAmountDecimal = decimalAmount
			rec.PayoutAmountUnits = units
		}
		rec.AssignedAgentID = w.assignAgent(ctx)
		if err := w.store.InsertRedemptionWatch(ctx, rec); err != nil {
			log.Printf("payments-gateway: redeem watcher: insert discovered request %s: %v", requestID, err)
			continue
		}
	}
}

// assignAgent picks which active exchange agent (if any) a freshly-
// discovered redemption should route to, or "" if no agent is active --
// in which case the request falls back to redemptionSettlementPartnerID and
// the automated NOWPayments rail, exactly as every redemption behaved before
// this feature existed. Distribution is a simple round robin: the agent at
// index (rows already assigned so far) % (active agent count), ordered by
// agent ID. This doesn't need to be perfectly fair under concurrent ticks --
// runOnce is never called concurrently with itself (see RedeemWatcher's doc
// comment) -- it only needs to spread load roughly evenly across however
// many agents are active, which today is expected to be exactly one.
// Errors reading the agent list are logged and treated as "no agent
// active" -- never block discovery of a real burn over a local bookkeeping
// read failure.
func (w *RedeemWatcher) assignAgent(ctx context.Context) string {
	agents, err := w.store.ListActiveExchangeAgentIDs(ctx)
	if err != nil {
		log.Printf("payments-gateway: redeem watcher: list active exchange agents: %v -- leaving new request unassigned", err)
		return ""
	}
	if len(agents) == 0 {
		return ""
	}
	assignedSoFar, err := w.store.CountAssignedRedemptionWatch(ctx)
	if err != nil {
		log.Printf("payments-gateway: redeem watcher: count assigned redemptions: %v -- leaving new request unassigned", err)
		return ""
	}
	return agents[assignedSoFar%len(agents)]
}

// partnerIDFor resolves the settlement.InitiateRequest.PartnerID for a
// redemption row: its assigned agent, if any (routing it to that agent's
// RailManualTreasury override -- see main.go's SetPartnerRail wiring), else
// the shared redemptionSettlementPartnerID constant every redemption used
// before this feature existed (routing it to the automated NOWPayments
// rail). A deployment with zero exchange agents configured behaves
// identically to before this feature existed.
func partnerIDFor(row RedemptionWatchRecord) string {
	if id := strings.TrimSpace(row.AssignedAgentID); id != "" {
		return id
	}
	return redemptionSettlementPartnerID
}

// processDiscovered advances every discovered row: either straight to an
// on-chain failure attestation (invalid amount or destination address,
// never touching the payout API), or through a fresh on-chain re-read and
// into settlement.Manager.Initiate.
func (w *RedeemWatcher) processDiscovered(ctx context.Context) {
	rows, err := w.store.ListRedemptionWatchByStatus(ctx, redemptionStatusDiscovered)
	if err != nil {
		log.Printf("payments-gateway: redeem watcher: list discovered rows: %v", err)
		return
	}
	for _, row := range rows {
		if row.PayoutAmountUnits <= 0 {
			reason := row.FailureReason
			if reason == "" {
				reason = "payout amount computation failed"
			}
			w.attestOutcome(ctx, row, redemptionOutcomeFailed, "", reason)
			continue
		}
		if !isValidTRC20Address(row.DestinationAddress) {
			w.attestOutcome(ctx, row, redemptionOutcomeFailed, "", "invalid destination address format")
			continue
		}

		// Fresh re-read, separate from the tick's initial list: guards
		// against a race with another watcher instance (or a stale list)
		// having already resolved this request between discovery and now.
		fresh, err := w.node.ListPendingRedemptions(ctx)
		if err != nil {
			log.Printf("payments-gateway: redeem watcher: fresh re-read for request %s: %v", row.RequestID, err)
			continue
		}
		stillPending := false
		for _, r := range fresh {
			if strings.TrimSpace(r.RequestID) == row.RequestID && strings.EqualFold(strings.TrimSpace(r.Status), "pending") {
				stillPending = true
				break
			}
		}
		if !stillPending {
			row.LocalStatus = redemptionStatusSkippedAlreadySettled
			row.UpdatedAt = w.nowFn().UTC()
			if err := w.store.UpdateRedemptionWatch(ctx, row); err != nil {
				log.Printf("payments-gateway: redeem watcher: mark request %s skipped_already_settled: %v", row.RequestID, err)
			}
			continue
		}

		// Durably transition to 'initiating' BEFORE calling the payout API --
		// the one step that makes crash recovery possible: a crash after
		// this point leaves a row Recover() can find and flag for manual
		// review on restart, rather than a real payout attempt silently
		// vanishing from view.
		row.LocalStatus = redemptionStatusInitiating
		row.UpdatedAt = w.nowFn().UTC()
		if err := w.store.UpdateRedemptionWatch(ctx, row); err != nil {
			log.Printf("payments-gateway: redeem watcher: persist initiating for request %s: %v", row.RequestID, err)
			continue
		}

		rec, initErr := w.settlement.Initiate(ctx, settlement.InitiateRequest{
			IntentID:      row.RequestID,
			ReservationID: row.RequestID,
			PartnerID:     partnerIDFor(row),
			Asset:         row.DestinationAsset,
			AmountUnits:   row.PayoutAmountUnits,
			Account:       row.DestinationAddress,
		})
		row.SettlementID = rec.ID
		row.UpdatedAt = w.nowFn().UTC()
		if persistErr := w.store.UpdateRedemptionWatch(ctx, row); persistErr != nil {
			log.Printf("payments-gateway: redeem watcher: CRITICAL: request %s settlement %s initiated but failed to persist settlement_id locally: %v -- inspect the redemption_settlements table directly", row.RequestID, rec.ID, persistErr)
		}
		if initErr != nil {
			if rec.Status == string(settlement.StatusFailed) {
				w.attestOutcome(ctx, row, redemptionOutcomeFailed, "", settlementFailureReason(rec))
			} else {
				// Submitted-but-persist-failed ("CRITICAL" case documented on
				// settlement.Manager.submittedLocked) or an unexpected rail
				// state -- leave the row 'initiating' either way; the next
				// tick's processInitiating re-reads the settlement record
				// directly from the database, so it doesn't depend on this
				// in-memory rec at all.
				log.Printf("payments-gateway: redeem watcher: request %s settlement %s initiate returned an error but status=%s -- leaving in-flight for next tick: %v", row.RequestID, rec.ID, rec.Status, initErr)
			}
			continue
		}
		// initErr == nil: normally Submitted. Leave as initiating;
		// processInitiating picks it up next tick.
	}
}

// processInitiating advances every in-flight row by re-reading its
// settlement record: attests paid once Settled, attests failed once Failed,
// otherwise leaves it for the next tick (still awaiting NOWPayments/operator
// confirmation).
func (w *RedeemWatcher) processInitiating(ctx context.Context) {
	rows, err := w.store.ListRedemptionWatchByStatus(ctx, redemptionStatusInitiating)
	if err != nil {
		log.Printf("payments-gateway: redeem watcher: list initiating rows: %v", err)
		return
	}
	for _, row := range rows {
		if strings.TrimSpace(row.SettlementID) == "" {
			// Should be unreachable -- initiating is only ever set alongside
			// a settlement_id in the same UpdateRedemptionWatch call above.
			// Leave for manual investigation rather than guessing.
			continue
		}
		rec, err := w.store.GetSettlement(ctx, row.SettlementID)
		if err != nil {
			log.Printf("payments-gateway: redeem watcher: load settlement %s for request %s: %v", row.SettlementID, row.RequestID, err)
			continue
		}
		switch rec.Status {
		case string(settlement.StatusSettled):
			w.attestOutcome(ctx, row, redemptionOutcomePaid, rec.ExternalRef, "")
		case string(settlement.StatusFailed):
			w.attestOutcome(ctx, row, redemptionOutcomeFailed, "", settlementFailureReason(rec))
		case string(settlement.StatusSubmitted):
			w.pollSubmitted(ctx, row, rec)
		default:
			// pending -- unreachable in practice for the nowpayments rail
			// (Initiate always calls CreatePayout synchronously, landing on
			// submitted or failed), but leave for next tick rather than
			// guessing at a status transition here.
		}
	}
}

// pollSubmitted checks a submitted settlement's real status directly against
// NOWPayments (when a statusChecker is configured) and automatically
// confirms or fails it -- closing the loop without requiring an operator to
// find the batch on the NOWPayments dashboard and call the confirm-payout/
// fail-payout admin endpoints by hand. Those endpoints remain available as a
// manual override; this is purely an automatic fast path.
func (w *RedeemWatcher) pollSubmitted(ctx context.Context, row RedemptionWatchRecord, rec swapdstorage.SettlementRecord) {
	if w.statusChecker == nil {
		// No automated status source configured -- an operator must resolve
		// this via the admin endpoints. Nothing to do this tick.
		return
	}
	if strings.TrimSpace(rec.ExternalRef) == "" {
		log.Printf("payments-gateway: redeem watcher: request %s settlement %s is submitted but has no external ref to poll -- leaving for manual investigation", row.RequestID, rec.ID)
		return
	}
	status, err := w.statusChecker.GetPayoutStatus(ctx, rec.ExternalRef)
	if err != nil {
		log.Printf("payments-gateway: redeem watcher: poll payout status for request %s (batch=%s): %v", row.RequestID, rec.ExternalRef, err)
		return
	}
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "FINISHED":
		confirmed, err := w.settlement.ConfirmSettled(ctx, rec.ID, settlement.Receipt{
			Reference: rec.ExternalRef,
			Operator:  "redeem-watcher-auto-poll",
			Note:      "auto-confirmed: NOWPayments payout status reported FINISHED",
		})
		if err != nil {
			log.Printf("payments-gateway: redeem watcher: auto-confirm request %s settlement %s: %v", row.RequestID, rec.ID, err)
			return
		}
		w.attestOutcome(ctx, row, redemptionOutcomePaid, confirmed.ExternalRef, "")
	case "REJECTED", "REJECTED_NOT_CHECKED":
		failed, err := w.settlement.MarkFailed(ctx, rec.ID, fmt.Sprintf("nowpayments payout status: %s", status))
		if err != nil {
			log.Printf("payments-gateway: redeem watcher: auto-fail request %s settlement %s: %v", row.RequestID, rec.ID, err)
			return
		}
		w.attestOutcome(ctx, row, redemptionOutcomeFailed, "", settlementFailureReason(failed))
	default:
		// NEW/CREATING/WAITING/PROCESSING (or any status NOWPayments adds
		// later) -- still in flight. Check again next tick rather than
		// guessing at an unrecognized value.
	}
}

// processAttesting polls for confirmation of every already-submitted
// attestation transaction, marking the row attested once it lands.
// processAttesting returns one RedemptionOutcomeEvent per row that newly
// reached redemptionStatusAttested THIS call -- the caller (runOnce) fires
// notifications for these AFTER releasing w.mu, so a slow or hanging
// notifier can never block ConfirmPayout/FailPayout/RetryPayout (which also
// need w.mu) or delay processing any other row in the same tick. See
// RedemptionNotifier's doc comment for why a notify failure itself is only
// ever logged, never allowed to affect anything on-chain -- by the time an
// event is returned here, the on-chain outcome (and, for a failed
// redemption, its refund -- see core/state_transition.go's
// applyAttestRedemption) is already fully durable.
func (w *RedeemWatcher) processAttesting(ctx context.Context) []RedemptionOutcomeEvent {
	rows, err := w.store.ListRedemptionWatchByStatus(ctx, redemptionStatusAttesting)
	if err != nil {
		log.Printf("payments-gateway: redeem watcher: list attesting rows: %v", err)
		return nil
	}
	var events []RedemptionOutcomeEvent
	for _, row := range rows {
		if strings.TrimSpace(row.AttestTxHash) == "" {
			continue
		}
		confirmed, err := w.node.GetTransactionReceipt(ctx, row.AttestTxHash)
		if err != nil {
			log.Printf("payments-gateway: redeem watcher: poll receipt for request %s (tx=%s): %v", row.RequestID, row.AttestTxHash, err)
			continue
		}
		if !confirmed {
			continue
		}
		row.LocalStatus = redemptionStatusAttested
		row.UpdatedAt = w.nowFn().UTC()
		if err := w.store.UpdateRedemptionWatch(ctx, row); err != nil {
			log.Printf("payments-gateway: redeem watcher: CRITICAL: request %s attestation confirmed on-chain (tx=%s) but failed to persist locally: %v", row.RequestID, row.AttestTxHash, err)
			continue
		}
		log.Printf("payments-gateway: redeem watcher: request %s attestation confirmed (outcome=%s, tx=%s)", row.RequestID, row.Outcome, row.AttestTxHash)

		if w.notifier != nil {
			events = append(events, RedemptionOutcomeEvent{
				RequestID:       row.RequestID,
				Account:         row.Account,
				Outcome:         row.Outcome,
				NHBAmountWei:    row.NHBAmountWei,
				PayoutReference: row.PayoutReference,
				FailureReason:   row.FailureReason,
				Refunded:        row.Outcome == redemptionOutcomeFailed,
			})
		}
	}
	return events
}

// attestOutcome builds, signs, and submits an attestRedemption transaction
// reporting status/payoutReference/failureReason, then durably records the
// submitted tx hash. On any error the row is left exactly as it was (still
// discovered or initiating) so the next tick retries -- this method never
// itself puts a row into a worse-tracked state than it found it in.
func (w *RedeemWatcher) attestOutcome(ctx context.Context, row RedemptionWatchRecord, status, payoutReference, failureReason string) {
	txHash, err := w.node.SendAttestRedemption(ctx, row.RequestID, status, payoutReference, failureReason)
	if err != nil {
		log.Printf("payments-gateway: redeem watcher: submit attestRedemption(%s) for request %s failed: %v -- will retry next tick", status, row.RequestID, err)
		return
	}
	row.Outcome = status
	row.PayoutReference = payoutReference
	row.FailureReason = failureReason
	row.AttestTxHash = txHash
	row.LocalStatus = redemptionStatusAttesting
	row.UpdatedAt = w.nowFn().UTC()
	if err := w.store.UpdateRedemptionWatch(ctx, row); err != nil {
		log.Printf("payments-gateway: redeem watcher: CRITICAL: attestRedemption(%s) for request %s submitted (tx=%s) but failed to persist locally: %v -- verify on-chain state manually", status, row.RequestID, txHash, err)
	}
}

// retryableLocalStatus reports whether a redemption_watch row is in a state
// an operator (or the automatic poller) may safely trigger a
// settlement.Manager mutation from: initiating (still awaiting a terminal
// payout signal) or stuck_manual_review (Recover() parks a crashed row here
// specifically so a human can resolve it once they've verified the real
// payout state -- see Recover's doc comment). Every other status means the
// on-chain attestation has already been submitted (attesting/attested) or
// the request resolved another way (skipped_already_settled, discovered).
// Acting on the settlement again in those states could submit a real second
// NOWPayments payout (RetryPayout) with no way to ever attest it on-chain --
// TxTypeAttestRedemption allows exactly one pending->paid|failed transition,
// ever, per the chain's own design.
func retryableLocalStatus(status string) bool {
	return status == redemptionStatusInitiating || status == redemptionStatusStuckManualReview
}

// lockedRow loads a redemption_watch row and confirms both that it exists
// and that it's in a state retryableLocalStatus permits acting on. Callers
// must hold w.mu.
func (w *RedeemWatcher) lockedRow(ctx context.Context, requestID string) (RedemptionWatchRecord, error) {
	row, err := w.store.GetRedemptionWatch(ctx, requestID)
	if err != nil {
		return RedemptionWatchRecord{}, err
	}
	if row == nil {
		return RedemptionWatchRecord{}, fmt.Errorf("redemption request %s not found", requestID)
	}
	if !retryableLocalStatus(row.LocalStatus) {
		return RedemptionWatchRecord{}, fmt.Errorf("redemption request %s is %q -- only %q or %q may be acted on (the on-chain attestation may already be final)", requestID, row.LocalStatus, redemptionStatusInitiating, redemptionStatusStuckManualReview)
	}
	if strings.TrimSpace(row.SettlementID) == "" {
		return RedemptionWatchRecord{}, fmt.Errorf("redemption request %s has no associated settlement yet", requestID)
	}
	return *row, nil
}

// resumeNormalFlow moves a row back to initiating after a successful
// operator action, so the watcher's own tick machinery (auto-poll,
// attestation) picks it up again exactly like any other in-flight request.
// A no-op if it's already there (the common case -- only a
// stuck_manual_review row needs this). Errors are logged, not returned: the
// settlement-level mutation the caller just made already succeeded and must
// not be lost over a bookkeeping write failure.
func (w *RedeemWatcher) resumeNormalFlow(ctx context.Context, row RedemptionWatchRecord) {
	if row.LocalStatus == redemptionStatusInitiating {
		return
	}
	row.LocalStatus = redemptionStatusInitiating
	row.UpdatedAt = w.nowFn().UTC()
	if err := w.store.UpdateRedemptionWatch(ctx, row); err != nil {
		log.Printf("payments-gateway: redeem watcher: CRITICAL: request %s settlement action succeeded but failed to move local status back to initiating: %v -- fix manually, the watcher won't otherwise pick this row up again", row.RequestID, err)
	}
}

// ConfirmPayout lets an operator record real-world evidence that a
// redemption's payout actually completed (a verified NOWPayments payout, a
// manual wire, etc). Serialized against the watcher's own tick via w.mu, and
// only permitted from a state retryableLocalStatus allows -- see its doc
// comment for why.
func (w *RedeemWatcher) ConfirmPayout(ctx context.Context, requestID string, receipt settlement.Receipt) (swapdstorage.SettlementRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	row, err := w.lockedRow(ctx, requestID)
	if err != nil {
		return swapdstorage.SettlementRecord{}, err
	}
	rec, err := w.settlement.ConfirmSettled(ctx, row.SettlementID, receipt)
	if err != nil {
		return rec, err
	}
	w.resumeNormalFlow(ctx, row)
	return rec, nil
}

// FailPayout lets an operator explicitly close out a redemption whose
// payout is dead (NOWPayments rejected it, a wire bounced, etc). Same
// serialization/state guard as ConfirmPayout.
func (w *RedeemWatcher) FailPayout(ctx context.Context, requestID, reason string) (swapdstorage.SettlementRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	row, err := w.lockedRow(ctx, requestID)
	if err != nil {
		return swapdstorage.SettlementRecord{}, err
	}
	rec, err := w.settlement.MarkFailed(ctx, row.SettlementID, reason)
	if err != nil {
		return rec, err
	}
	w.resumeNormalFlow(ctx, row)
	return rec, nil
}

// RetryPayout re-attempts a payout for a redemption whose settlement
// previously failed -- submitting a brand new, real NOWPayments batch. Same
// serialization/state guard as ConfirmPayout, but this is the single most
// dangerous of the three operator actions: unlike Confirm/Fail it spends
// real money again, so it is never permitted once a request may already
// have been attested on-chain (see retryableLocalStatus).
func (w *RedeemWatcher) RetryPayout(ctx context.Context, requestID string) (swapdstorage.SettlementRecord, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	row, err := w.lockedRow(ctx, requestID)
	if err != nil {
		return swapdstorage.SettlementRecord{}, err
	}
	rec, err := w.settlement.RetryNowPayments(ctx, row.SettlementID)
	if err != nil {
		return rec, err
	}
	w.resumeNormalFlow(ctx, row)
	return rec, nil
}
