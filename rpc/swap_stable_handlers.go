package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"go.opentelemetry.io/otel/trace"

	"nhbchain/services/swapd/stable"
)

func (s *Server) handleStableRequestSwapApproval(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	engine, _, _, _ := s.stableEngineConfig()
	if engine == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "stable engine not enabled", nil)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected payload with asset and amount", nil)
		return
	}
	var payload struct {
		Asset   string  `json:"asset"`
		Amount  float64 `json:"amount"`
		Account string  `json:"account"`
	}
	if err := json.Unmarshal(req.Params[0], &payload); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid payload", err.Error())
		return
	}
	asset := strings.ToUpper(strings.TrimSpace(payload.Asset))
	if asset == "" || payload.Amount <= 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "asset and positive amount required", nil)
		return
	}
	quote, err := engine.Price(r.Context(), stable.QuoteRequest{Asset: asset, Amount: payload.Amount})
	if err != nil {
		s.writeStableRPCError(w, req.ID, err)
		return
	}
	traceID := traceIDFromRPCContext(r.Context())
	response := map[string]any{
		"quoteId":   quote.Quote.ID,
		"asset":     quote.Quote.Asset,
		"price":     quote.Quote.Price,
		"expiresAt": quote.Quote.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if traceID != "" {
		response["traceId"] = traceID
	}
	writeResult(w, req.ID, response)
}

func (s *Server) handleStableSwapMint(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	engine, _, _, _ := s.stableEngineConfig()
	if engine == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "stable engine not enabled", nil)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected reservation payload", nil)
		return
	}
	var payload struct {
		QuoteID  string  `json:"quoteId"`
		AmountIn float64 `json:"amountIn"`
		Account  string  `json:"account"`
	}
	if err := json.Unmarshal(req.Params[0], &payload); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid payload", err.Error())
		return
	}
	quoteID := strings.TrimSpace(payload.QuoteID)
	account := strings.TrimSpace(payload.Account)
	if quoteID == "" || account == "" || payload.AmountIn <= 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "quoteId, account, and positive amountIn required", nil)
		return
	}
	reservation, err := engine.Reserve(r.Context(), stable.ReserveRequest{QuoteID: quoteID, Account: account, AmountIn: payload.AmountIn})
	if err != nil {
		s.writeStableRPCError(w, req.ID, err)
		return
	}
	traceID := traceIDFromRPCContext(r.Context())
	response := map[string]any{
		"reservationId": reservation.Reservation.QuoteID,
		"quoteId":       reservation.Reservation.QuoteID,
		"amountIn":      reservation.Reservation.AmountIn,
		"amountOut":     reservation.Reservation.AmountOut,
		"expiresAt":     reservation.Reservation.ExpiresAt.UTC().Format(time.RFC3339),
	}
	if traceID != "" {
		response["traceId"] = traceID
	}
	writeResult(w, req.ID, response)
}

func (s *Server) handleStableSwapBurn(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	engine, _, _, _ := s.stableEngineConfig()
	if engine == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "stable engine not enabled", nil)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected reservation payload", nil)
		return
	}
	var payload struct {
		ReservationID string `json:"reservationId"`
	}
	if err := json.Unmarshal(req.Params[0], &payload); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid payload", err.Error())
		return
	}
	reservationID := strings.TrimSpace(payload.ReservationID)
	if reservationID == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "reservationId required", nil)
		return
	}
	intent, err := engine.CashOut(r.Context(), stable.CashOutRequest{ReservationID: reservationID})
	if err != nil {
		s.writeStableRPCError(w, req.ID, err)
		return
	}
	traceID := traceIDFromRPCContext(r.Context())
	response := map[string]any{
		"intentId":      intent.Intent.ID,
		"reservationId": intent.Intent.ReservationID,
		"amount":        intent.Intent.Amount,
		"createdAt":     intent.Intent.CreatedAt.UTC().Format(time.RFC3339),
	}
	if traceID != "" {
		response["traceId"] = traceID
	}
	writeResult(w, req.ID, response)
}

func (s *Server) handleStableGetSwapStatus(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	engine, _, _, nowFn := s.stableEngineConfig()
	if engine == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "stable engine not enabled", nil)
		return
	}
	status := engine.Status(r.Context())
	response := map[string]any{
		"quotes":       status.Quotes,
		"reservations": status.Reservations,
		"assets":       status.Assets,
		"updatedAt":    nowFn().UTC().Format(time.RFC3339),
	}
	writeResult(w, req.ID, response)
}

func (s *Server) stableEngineConfig() (*stable.Engine, map[string]stable.Asset, stable.Limits, func() time.Time) {
	s.swapStableMu.RLock()
	defer s.swapStableMu.RUnlock()
	assets := s.swapStable.assets
	if assets == nil {
		assets = make(map[string]stable.Asset)
	}
	now := s.swapStable.now
	if now == nil {
		now = time.Now
	}
	return s.swapStable.engine, assets, s.swapStable.limits, now
}

func (s *Server) writeStableRPCError(w http.ResponseWriter, id interface{}, err error) {
	switch {
	case errors.Is(err, stable.ErrNotSupported):
		writeError(w, http.StatusNotFound, id, codeInvalidParams, err.Error(), nil)
	case errors.Is(err, stable.ErrQuoteNotFound):
		writeError(w, http.StatusNotFound, id, codeInvalidParams, err.Error(), nil)
	case errors.Is(err, stable.ErrQuoteExpired):
		writeError(w, http.StatusConflict, id, codeInvalidParams, err.Error(), nil)
	case errors.Is(err, stable.ErrReservationNotFound):
		writeError(w, http.StatusUnprocessableEntity, id, codeInvalidParams, err.Error(), nil)
	default:
		writeError(w, http.StatusInternalServerError, id, codeServerError, "stable engine error", err.Error())
	}
}

func traceIDFromRPCContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return ""
	}
	return spanCtx.TraceID().String()
}
