package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"nhbchain/services/swapd/settlement"
	"nhbchain/services/swapd/stable"
	"nhbchain/services/swapd/storage"
)

func (s *Server) registerStableHandlers(mux *http.ServeMux) {
	if mux == nil {
		return
	}
	stableMux := http.NewServeMux()
	stableMux.HandleFunc("/v1/stable/quote", s.handleStableQuote)
	stableMux.HandleFunc("/v1/stable/reserve", s.handleStableReserve)
	stableMux.HandleFunc("/v1/stable/cashout", s.handleStableCashOut)
	stableMux.HandleFunc("/v1/stable/status", s.handleStableStatus)
	mux.Handle("/v1/stable/limits", s.requirePartner(http.HandlerFunc(s.handleStableLimits)))
	mux.Handle("/v1/stable/", s.requirePartner(stableMux))
}

func (s *Server) handleStableQuote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.ensureStablePrincipal(w, r) {
		return
	}
	partner, ok := partnerPrincipalFromRequest(r)
	if !ok {
		s.writeStableError(w, http.StatusForbidden, "partner not authorized")
		return
	}
	if !s.stableEngineEnabled() {
		s.writeStableDisabled(w)
		return
	}
	var payload struct {
		Asset   string  `json:"asset"`
		Amount  float64 `json:"amount"`
		Account string  `json:"account"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.writeStableError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	asset := strings.ToUpper(strings.TrimSpace(payload.Asset))
	if asset == "" || payload.Amount <= 0 {
		s.writeStableError(w, http.StatusBadRequest, "asset and positive amount required")
		return
	}
	quote, err := s.stable.engine.Price(r.Context(), stable.QuoteRequest{Asset: asset, Amount: payload.Amount})
	if err != nil {
		status, message := stableErrorStatus(err)
		if status >= http.StatusInternalServerError && s.logger != nil {
			s.logger.Printf("swapd: stable quote error: %v", err)
		}
		s.recordAudit(r.Context(), "quote", partner.ID, "", "error", map[string]any{
			"asset":  asset,
			"amount": payload.Amount,
			"error":  err.Error(),
		})
		s.writeStableError(w, status, message)
		return
	}
	s.recordAudit(r.Context(), "quote", partner.ID, quote.Quote.ID, "success", map[string]any{
		"asset":      quote.Quote.Asset,
		"amount":     payload.Amount,
		"price":      stable.FromRateUnits(quote.Quote.Price),
		"expires_at": quote.Quote.ExpiresAt.UTC().Format(time.RFC3339),
	})
	traceID := traceIDFromContext(r.Context())
	response := map[string]any{
		"quote_id":   quote.Quote.ID,
		"asset":      quote.Quote.Asset,
		"price":      stable.FromRateUnits(quote.Quote.Price),
		"expires_at": quote.Quote.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if traceID != "" {
		response["trace_id"] = traceID
	}
	s.writeStableJSON(w, http.StatusOK, response)
}

func (s *Server) handleStableReserve(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.ensureStablePrincipal(w, r) {
		return
	}
	partner, ok := partnerPrincipalFromRequest(r)
	if !ok {
		s.writeStableError(w, http.StatusForbidden, "partner not authorized")
		return
	}
	if !s.stableEngineEnabled() {
		s.writeStableDisabled(w)
		return
	}
	var payload struct {
		QuoteID  string  `json:"quote_id"`
		AmountIn float64 `json:"amount_in"`
		Account  string  `json:"account"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.writeStableError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	quoteID := strings.TrimSpace(payload.QuoteID)
	account := strings.TrimSpace(payload.Account)
	if quoteID == "" || account == "" || payload.AmountIn <= 0 {
		s.writeStableError(w, http.StatusBadRequest, "quote_id, account, and positive amount_in required")
		return
	}
	reservation, err := s.stable.engine.Reserve(r.Context(), stable.ReserveRequest{QuoteID: quoteID, Account: account, AmountIn: payload.AmountIn})
	if err != nil {
		status, message := stableErrorStatus(err)
		if status >= http.StatusInternalServerError && s.logger != nil {
			s.logger.Printf("swapd: stable reserve error: %v", err)
		}
		s.recordAudit(r.Context(), "reserve", partner.ID, quoteID, "error", map[string]any{
			"account":   account,
			"amount_in": payload.AmountIn,
			"error":     err.Error(),
		})
		s.writeStableError(w, status, message)
		return
	}
	amountOut := reservation.Reservation.AmountOut
	allowed, _, quotaErr := s.enforcePartnerQuota(r.Context(), partner, amountOut)
	if quotaErr != nil {
		if cancelErr := s.stable.engine.CancelReservation(r.Context(), reservation.Reservation.QuoteID); cancelErr != nil && s.logger != nil {
			s.logger.Printf("swapd: revert reservation after quota error: %v", cancelErr)
		}
		if s.logger != nil {
			s.logger.Printf("swapd: partner quota enforcement error: %v", quotaErr)
		}
		s.recordAudit(r.Context(), "reserve", partner.ID, reservation.Reservation.QuoteID, "error", map[string]any{
			"error": "quota enforcement failed: " + quotaErr.Error(),
		})
		s.writeStableError(w, http.StatusInternalServerError, "quota enforcement failed")
		return
	}
	if !allowed {
		if cancelErr := s.stable.engine.CancelReservation(r.Context(), reservation.Reservation.QuoteID); cancelErr != nil && s.logger != nil {
			s.logger.Printf("swapd: revert reservation after quota exhaustion: %v", cancelErr)
		}
		s.recordAudit(r.Context(), "reserve", partner.ID, reservation.Reservation.QuoteID, "quota_exceeded", map[string]any{
			"amount_out": stable.FromAmountUnits(amountOut),
		})
		s.writeStableError(w, http.StatusTooManyRequests, "partner quota exceeded")
		return
	}
	s.setReservationOwner(reservation.Reservation.QuoteID, partner.ID)
	s.recordAudit(r.Context(), "reserve", partner.ID, reservation.Reservation.QuoteID, "success", map[string]any{
		"account":    account,
		"amount_in":  stable.FromAmountUnits(reservation.Reservation.AmountIn),
		"amount_out": stable.FromAmountUnits(reservation.Reservation.AmountOut),
		"expires_at": reservation.Reservation.ExpiresAt.UTC().Format(time.RFC3339),
	})
	traceID := traceIDFromContext(r.Context())
	response := map[string]any{
		"reservation_id": reservation.Reservation.QuoteID,
		"quote_id":       reservation.Reservation.QuoteID,
		"amount_in":      stable.FromAmountUnits(reservation.Reservation.AmountIn),
		"amount_out":     stable.FromAmountUnits(reservation.Reservation.AmountOut),
		"expires_at":     reservation.Reservation.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if traceID != "" {
		response["trace_id"] = traceID
	}
	s.writeStableJSON(w, http.StatusOK, response)
}

func (s *Server) handleStableCashOut(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.ensureStablePrincipal(w, r) {
		return
	}
	partner, ok := partnerPrincipalFromRequest(r)
	if !ok {
		s.writeStableError(w, http.StatusForbidden, "partner not authorized")
		return
	}
	if !s.stableEngineEnabled() {
		s.writeStableDisabled(w)
		return
	}
	var payload struct {
		ReservationID string `json:"reservation_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.writeStableError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	reservationID := strings.TrimSpace(payload.ReservationID)
	if reservationID == "" {
		s.writeStableError(w, http.StatusBadRequest, "reservation_id required")
		return
	}
	// The stable engine itself has no concept of partners (reservation IDs
	// are opaque, engine-generated strings with no owner check), so this
	// HTTP-layer check is the only thing stopping one authenticated partner
	// from cashing out a reservation another partner created. The response
	// is deliberately identical whether the ID is unknown or just not this
	// partner's, to avoid turning it into a reservation-ID existence oracle.
	if owner, ok := s.reservationOwner(reservationID); !ok || owner != partner.ID {
		s.recordAudit(r.Context(), "cashout", partner.ID, reservationID, "error", map[string]any{
			"error": "reservation not owned by this partner",
		})
		s.writeStableError(w, http.StatusForbidden, "reservation not owned by this partner")
		return
	}
	intent, err := s.stable.engine.CashOut(r.Context(), stable.CashOutRequest{ReservationID: reservationID})
	if err != nil {
		status, message := stableErrorStatus(err)
		if status >= http.StatusInternalServerError && s.logger != nil {
			s.logger.Printf("swapd: stable cashout error: %v", err)
		}
		s.recordAudit(r.Context(), "cashout", partner.ID, reservationID, "error", map[string]any{
			"error": err.Error(),
		})
		s.writeStableError(w, status, message)
		return
	}
	s.recordAudit(r.Context(), "cashout", partner.ID, intent.Intent.ID, "success", map[string]any{
		"reservation_id": intent.Intent.ReservationID,
		"amount":         intent.Intent.Amount,
	})

	response := map[string]any{
		"intent_id":      intent.Intent.ID,
		"reservation_id": intent.Intent.ReservationID,
		"amount":         intent.Intent.Amount,
		"created_at":     intent.Intent.CreatedAt.UTC().Format(time.RFC3339),
	}
	if s.settlement != nil {
		settlementRecord, settlementErr := s.settlement.Initiate(r.Context(), settlement.InitiateRequest{
			IntentID:      intent.Intent.ID,
			ReservationID: intent.Intent.ReservationID,
			PartnerID:     partner.ID,
			Asset:         intent.Intent.Asset,
			AmountUnits:   intent.Intent.AmountUnits,
			Account:       intent.Intent.Account,
		})
		// Settlement initiation failure never fails the cashout response --
		// the intent and ledger movement are already durably committed by
		// this point. A failed/stuck settlement is surfaced via the audit
		// trail and the settlement fields below for an operator to act on
		// through /admin/settlements/{id}/retry or /confirm.
		auditOutcome := "success"
		auditDetail := map[string]any{
			"settlement_id": settlementRecord.ID,
			"rail":          settlementRecord.Rail,
			"status":        settlementRecord.Status,
		}
		if settlementErr != nil {
			auditOutcome = "error"
			auditDetail["error"] = settlementErr.Error()
			if s.logger != nil {
				s.logger.Printf("swapd: settlement initiation error for intent %s: %v", intent.Intent.ID, settlementErr)
			}
		}
		s.recordAudit(r.Context(), "settlement", partner.ID, settlementRecord.ID, auditOutcome, auditDetail)
		response["settlement_id"] = settlementRecord.ID
		response["settlement_rail"] = settlementRecord.Rail
		response["settlement_status"] = settlementRecord.Status
	}

	traceID := traceIDFromContext(r.Context())
	if traceID != "" {
		response["trace_id"] = traceID
	}
	s.writeStableJSON(w, http.StatusOK, response)
}

func (s *Server) handleStableStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.ensureStablePrincipal(w, r) {
		return
	}
	if _, ok := partnerPrincipalFromRequest(r); !ok {
		s.writeStableError(w, http.StatusForbidden, "partner not authorized")
		return
	}
	if !s.stableEngineEnabled() {
		s.writeStableDisabled(w)
		return
	}
	snapshot := s.stable.engine.Status(r.Context())
	response := map[string]any{
		"quotes":       snapshot.Quotes,
		"reservations": snapshot.Reservations,
		"assets":       snapshot.Assets,
		"updated_at":   s.stableNow().UTC().Format(time.RFC3339),
	}
	s.writeStableJSON(w, http.StatusOK, response)
}

func (s *Server) handleStableLimits(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !s.ensureStablePrincipal(w, r) {
		return
	}
	if !s.stableEngineEnabled() {
		s.writeStableDisabled(w)
		return
	}
	assetCaps := make(map[string]map[string]any, len(s.stable.assets))
	symbols := make([]string, 0, len(s.stable.assets))
	for symbol := range s.stable.assets {
		symbols = append(symbols, symbol)
	}
	sort.Strings(symbols)
	for _, symbol := range symbols {
		cfg := s.stable.assets[symbol]
		assetCaps[symbol] = map[string]any{
			"max_slippage_bps":  cfg.MaxSlippageBps,
			"quote_ttl_seconds": int(cfg.QuoteTTL.Seconds()),
			"soft_inventory":    cfg.SoftInventory,
		}
	}
	response := map[string]any{
		"daily_cap":  s.stable.limits.DailyCap,
		"asset_caps": assetCaps,
	}
	s.writeStableJSON(w, http.StatusOK, response)
}

func (s *Server) writeStableDisabled(w http.ResponseWriter) {
	s.writeStableJSON(w, http.StatusNotImplemented, map[string]string{"error": "stable engine not enabled"})
}

func (s *Server) writeStableError(w http.ResponseWriter, status int, message string) {
	if status <= 0 {
		status = http.StatusInternalServerError
	}
	s.writeStableJSON(w, status, map[string]string{"error": message})
}

func (s *Server) writeStableJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func (s *Server) ensureStablePrincipal(w http.ResponseWriter, r *http.Request) bool {
	if partner, ok := PartnerFromContext(r.Context()); ok && partner != nil && strings.TrimSpace(partner.ID) != "" {
		return true
	}
	principal, ok := PrincipalFromContext(r.Context())
	if !ok {
		s.writeStableError(w, http.StatusUnauthorized, "authentication required")
		return false
	}
	if strings.TrimSpace(principal.Method) == "" {
		s.writeStableError(w, http.StatusForbidden, "principal not authorized")
		return false
	}
	return true
}

func partnerPrincipalFromRequest(r *http.Request) (*PartnerPrincipal, bool) {
	if r == nil {
		return nil, false
	}
	partner, ok := PartnerFromContext(r.Context())
	if !ok || partner == nil {
		return nil, false
	}
	if strings.TrimSpace(partner.ID) == "" {
		return nil, false
	}
	return partner, true
}

// setReservationOwner records which partner created a reservation, so a
// later cash-out attempt can be checked against it.
func (s *Server) setReservationOwner(reservationID, partnerID string) {
	if s == nil {
		return
	}
	s.reservationOwnersMu.Lock()
	defer s.reservationOwnersMu.Unlock()
	if s.reservationOwners == nil {
		s.reservationOwners = make(map[string]string)
	}
	s.reservationOwners[reservationID] = partnerID
}

// reservationOwner returns the partner ID that created the reservation, if
// still tracked.
func (s *Server) reservationOwner(reservationID string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.reservationOwnersMu.Lock()
	defer s.reservationOwnersMu.Unlock()
	owner, ok := s.reservationOwners[reservationID]
	return owner, ok
}

func (s *Server) stableEngineEnabled() bool {
	return s != nil && s.stable.enabled && s.stable.engine != nil
}

func (s *Server) enforcePartnerQuota(ctx context.Context, partner *PartnerPrincipal, amount int64) (bool, int64, error) {
	if s == nil || s.storage == nil || partner == nil {
		return true, 0, nil
	}
	if amount <= 0 {
		return true, partner.DailyQuota, nil
	}
	partnerID := strings.TrimSpace(partner.ID)
	if partnerID == "" {
		return true, partner.DailyQuota, nil
	}
	now := s.stableNow()
	if now.IsZero() {
		now = time.Now()
	}
	return s.storage.ConsumePartnerQuota(ctx, partnerID, now, amount, partner.DailyQuota)
}

// recordAudit persists a durable audit trail entry for a stable-swap
// lifecycle step. It is best-effort: a storage failure is logged but never
// fails the HTTP request, since the audit trail is a reconstruction aid, not
// part of the transactional path.
func (s *Server) recordAudit(ctx context.Context, eventType, partnerID, subjectID, outcome string, detail map[string]any) {
	if s == nil || s.storage == nil {
		return
	}
	partnerID = strings.TrimSpace(partnerID)
	if partnerID == "" {
		partnerID = "anonymous"
	}
	payload, err := json.Marshal(detail)
	if err != nil {
		payload = []byte("{}")
	}
	event := storage.AuditEvent{
		EventType:  eventType,
		PartnerID:  partnerID,
		SubjectID:  subjectID,
		Outcome:    outcome,
		Detail:     string(payload),
		TraceID:    traceIDFromContext(ctx),
		OccurredAt: s.stableNow(),
	}
	if err := s.storage.RecordAuditEvent(ctx, event); err != nil && s.logger != nil {
		s.logger.Printf("swapd: record audit event: %v", err)
	}
}

func traceIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return ""
	}
	return spanCtx.TraceID().String()
}

func stableErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, stable.ErrNotSupported):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, stable.ErrQuoteNotFound):
		return http.StatusNotFound, err.Error()
	case errors.Is(err, stable.ErrQuoteExpired):
		return http.StatusConflict, err.Error()
	case errors.Is(err, stable.ErrReservationNotFound):
		return http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, stable.ErrPriceUnavailable):
		return http.StatusServiceUnavailable, err.Error()
	case errors.Is(err, stable.ErrSlippageExceeded):
		return http.StatusConflict, err.Error()
	case errors.Is(err, stable.ErrInsufficientReserve):
		return http.StatusConflict, err.Error()
	case errors.Is(err, stable.ErrDailyCapExceeded):
		return http.StatusTooManyRequests, err.Error()
	case errors.Is(err, stable.ErrQuoteAmountMismatch):
		return http.StatusUnprocessableEntity, err.Error()
	case errors.Is(err, stable.ErrReservationExpired):
		return http.StatusConflict, err.Error()
	case errors.Is(err, stable.ErrReservationConsumed):
		return http.StatusConflict, err.Error()
	default:
		return http.StatusInternalServerError, err.Error()
	}
}
