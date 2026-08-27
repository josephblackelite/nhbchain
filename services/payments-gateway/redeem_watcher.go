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

	// redemptionSettlementPartnerID is the fixed partner identifier this
	// service passes to settlement.Manager for every redemption settlement.
	// Redemption settlements have no partner concept of their own (unlike
	// swapd's real partner-scoped cash-out intents) -- this constant only
	// matters if main.go's settlement.Config ever grows a PartnerRails
	// override, which it does not: DefaultRail is always RailNowPayments, so
	// every redemption always resolves to the same rail regardless of this
	// value.
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
	return &RedeemWatcher{store: store, node: node, settlement: mgr, statusChecker: statusChecker, interval: interval, nowFn: time.Now}
}

// Recover runs once at startup, BEFORE Run's ticker loop starts. Any row
// still in redemptionStatusInitiating is proof a previous process crashed
// (or was killed) somewhere between committing to a payout attempt and
// durably recording its outcome -- see redemptionStatusInitiating's doc
// comment in redeem_storage.go for why this is never auto-retried or
// silently ignored. This is the single most safety-critical behavior in the
// whole feature: an untested version of this could either double-pay a
// redeemer (auto-retry) or strand a real payout unattested forever (ignore
// it).
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
// with itself (Run's single select loop guarantees this), but IS
// serialized against ConfirmPayout/FailPayout/RetryPayout via w.mu -- see
// its doc comment.
func (w *RedeemWatcher) runOnce(ctx context.Context) {
	w.mu.Lock()
	defer w.mu.Unlock()
	pending, err := w.node.ListPendingRedemptions(ctx)
	if err != nil {
		log.Printf("payments-gateway: redeem watcher: list pending redemptions: %v", err)
		return
	}
	w.discoverNew(ctx, pending)
	w.processDiscovered(ctx)
	w.processInitiating(ctx)
	w.processAttesting(ctx)
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
		if err := w.store.InsertRedemptionWatch(ctx, rec); err != nil {
			log.Printf("payments-gateway: redeem watcher: insert discovered request %s: %v", requestID, err)
			continue
		}
	}
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
			PartnerID:     redemptionSettlementPartnerID,
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
func (w *RedeemWatcher) processAttesting(ctx context.Context) {
	rows, err := w.store.ListRedemptionWatchByStatus(ctx, redemptionStatusAttesting)
	if err != nil {
		log.Printf("payments-gateway: redeem watcher: list attesting rows: %v", err)
		return
	}
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
	}
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
