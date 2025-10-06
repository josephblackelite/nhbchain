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

	"nhbchain/services/swapd/stable"
)

func (s *Server) registerStableHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/v1/stable/quote", s.handleStableQuote)
	mux.HandleFunc("/v1/stable/reserve", s.handleStableReserve)
	mux.HandleFunc("/v1/stable/cashout", s.handleStableCashOut)
	mux.HandleFunc("/v1/stable/status", s.handleStableStatus)
	mux.HandleFunc("/v1/stable/limits", s.handleStableLimits)
}

func (s *Server) handleStableQuote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
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
		s.writeStableError(w, status, message)
		return
	}
	traceID := traceIDFromContext(r.Context())
	response := map[string]any{
		"quote_id":   quote.Quote.ID,
		"asset":      quote.Quote.Asset,
		"price":      quote.Quote.Price,
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
		s.writeStableError(w, status, message)
		return
	}
	traceID := traceIDFromContext(r.Context())
	response := map[string]any{
		"reservation_id": reservation.Reservation.QuoteID,
		"quote_id":       reservation.Reservation.QuoteID,
		"amount_in":      reservation.Reservation.AmountIn,
		"amount_out":     reservation.Reservation.AmountOut,
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
	intent, err := s.stable.engine.CashOut(r.Context(), stable.CashOutRequest{ReservationID: reservationID})
	if err != nil {
		status, message := stableErrorStatus(err)
		if status >= http.StatusInternalServerError && s.logger != nil {
			s.logger.Printf("swapd: stable cashout error: %v", err)
		}
		s.writeStableError(w, status, message)
		return
	}
	traceID := traceIDFromContext(r.Context())
	response := map[string]any{
		"intent_id":      intent.Intent.ID,
		"reservation_id": intent.Intent.ReservationID,
		"amount":         intent.Intent.Amount,
		"created_at":     intent.Intent.CreatedAt.UTC().Format(time.RFC3339),
	}
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

func (s *Server) stableEngineEnabled() bool {
	return s != nil && s.stable.enabled && s.stable.engine != nil
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
	default:
		return http.StatusInternalServerError, err.Error()
	}
}
