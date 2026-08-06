package rpc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
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
		Asset     string          `json:"asset"`
		AmountRaw json.RawMessage `json:"amount"`
		Account   string          `json:"account"`
		PayAsset  string          `json:"payAsset"`
		GetAsset  string          `json:"getAsset"`
		AmountIn  string          `json:"amountIn"`
	}
	if err := json.Unmarshal(req.Params[0], &payload); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid payload", err.Error())
		return
	}

	traceID := traceIDFromRPCContext(r.Context())

	payAsset := strings.ToUpper(strings.TrimSpace(payload.PayAsset))
	getAsset := strings.ToUpper(strings.TrimSpace(payload.GetAsset))
	if payAsset != "" && getAsset != "" && strings.TrimSpace(payload.AmountIn) != "" {
		amountOutWei, err := quoteCrossAsset(engine, payAsset, getAsset, payload.AmountIn)
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to quote asset pair", err.Error())
			return
		}
		payRate, _, payOK := engine.CurrentPrice("USD", payAsset)
		getRate, _, getOK := engine.CurrentPrice("USD", getAsset)
		if !payOK || !getOK {
			writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "price unavailable", nil)
			return
		}
		response := map[string]any{
			"quoteId":     fmt.Sprintf("pair-%d", time.Now().UTC().UnixNano()),
			"payAsset":    payAsset,
			"getAsset":    getAsset,
			"amountIn":    payload.AmountIn,
			"amountOut":   amountOutWei,
			"payAssetUsd": payRate,
			"getAssetUsd": getRate,
			"price":       getRate,
			"expiresAt":   time.Now().UTC().Add(30 * time.Second).Format(time.RFC3339),
		}
		if traceID != "" {
			response["traceId"] = traceID
		}
		writeResult(w, req.ID, response)
		return
	}

	asset := strings.ToUpper(strings.TrimSpace(payload.Asset))
	amount, err := parseFlexibleAmount(payload.AmountRaw)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid amount", err.Error())
		return
	}
	if asset == "" || amount <= 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "asset and positive amount required", nil)
		return
	}
	quote, err := engine.Price(r.Context(), stable.QuoteRequest{Asset: asset, Amount: amount})
	if err != nil {
		s.writeStableRPCError(w, req.ID, err)
		return
	}
	response := map[string]any{
		"quoteId":   quote.Quote.ID,
		"asset":     quote.Quote.Asset,
		"price":     quote.Quote.Price,
		"amountOut": quote.Quote.Price,
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

func quoteCrossAsset(engine *stable.Engine, payAsset string, getAsset string, amountInWei string) (string, error) {
	if engine == nil {
		return "", stable.ErrPriceUnavailable
	}
	payRate, _, payOK := engine.CurrentPrice("USD", payAsset)
	getRate, _, getOK := engine.CurrentPrice("USD", getAsset)
	if !payOK || !getOK || payRate <= 0 || getRate <= 0 {
		return "", stable.ErrPriceUnavailable
	}
	amountIn := new(big.Int)
	if _, ok := amountIn.SetString(strings.TrimSpace(amountInWei), 10); !ok || amountIn.Sign() <= 0 {
		return "", errors.New("amountIn must be a positive integer string")
	}
	payRateFloat := new(big.Float).SetPrec(256).SetFloat64(payRate)
	getRateFloat := new(big.Float).SetPrec(256).SetFloat64(getRate)
	if getRateFloat.Sign() <= 0 {
		return "", stable.ErrPriceUnavailable
	}
	amountInFloat := new(big.Float).SetPrec(256).SetInt(amountIn)
	usdValueWei := new(big.Float).SetPrec(256).Mul(amountInFloat, payRateFloat)
	amountOutFloat := new(big.Float).SetPrec(256).Quo(usdValueWei, getRateFloat)
	amountOut, _ := amountOutFloat.Int(nil)
	if amountOut == nil || amountOut.Sign() <= 0 {
		return "", errors.New("quoted amount is zero")
	}
	return amountOut.String(), nil
}

func parseFlexibleAmount(raw json.RawMessage) (float64, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return 0, nil
	}
	var numeric float64
	if err := json.Unmarshal(raw, &numeric); err == nil {
		return numeric, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		text = strings.TrimSpace(text)
		if text == "" {
			return 0, nil
		}
		value, ok := new(big.Float).SetString(text)
		if !ok {
			return 0, errors.New("amount string must be numeric")
		}
		floatValue, _ := value.Float64()
		return floatValue, nil
	}
	return 0, errors.New("amount must be a number or numeric string")
}

func (s *Server) handleCheckSwapAllowance(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	// For native layer swaps, tokens are internally provisioned within the chain state.
	// As we don't use standard ERC-20 allowances, this always returns maximum allowance.
	writeResult(w, req.ID, map[string]any{
		"allowed":   true,
		"allowance": "0xffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff",
	})
}
