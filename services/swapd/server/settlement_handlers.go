package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nhbchain/services/swapd/settlement"
	"nhbchain/services/swapd/stable"
	"nhbchain/services/swapd/storage"
)

// handleListSettlements exposes durable settlement records for operator
// visibility -- which cash-out intents have real money moving, on which
// rail, and what state each is in.
func (s *Server) handleListSettlements(w http.ResponseWriter, r *http.Request) {
	if s.settlement == nil {
		s.writeStableJSON(w, http.StatusNotImplemented, map[string]string{"error": "settlement not enabled"})
		return
	}
	query := r.URL.Query()
	partnerID := strings.TrimSpace(query.Get("partner_id"))
	status := strings.TrimSpace(query.Get("status"))
	limit := 100
	if raw := strings.TrimSpace(query.Get("limit")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	records, err := s.settlement.List(r.Context(), partnerID, status, limit)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("swapd: list settlements: %v", err)
		}
		http.Error(w, "failed to load settlements", http.StatusInternalServerError)
		return
	}
	response := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		response = append(response, settlementJSON(rec))
	}
	s.writeStableJSON(w, http.StatusOK, map[string]any{"settlements": response})
}

// handleConfirmSettlement lets an operator attach real-world evidence
// (a bank wire reference, or a verified NOWPayments payout confirmation)
// that a settlement actually completed.
func (s *Server) handleConfirmSettlement(w http.ResponseWriter, r *http.Request) {
	if s.settlement == nil {
		s.writeStableJSON(w, http.StatusNotImplemented, map[string]string{"error": "settlement not enabled"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var payload struct {
		Reference string `json:"reference"`
		Note      string `json:"note"`
		Operator  string `json:"operator"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.writeStableError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	record, err := s.settlement.ConfirmSettled(r.Context(), id, settlement.Receipt{
		Reference: payload.Reference, Note: payload.Note, Operator: payload.Operator,
	})
	if err != nil {
		status, message := settlementErrorStatus(err)
		s.recordAudit(r.Context(), "settlement_confirm", record.PartnerID, id, "error", map[string]any{"error": err.Error()})
		s.writeStableError(w, status, message)
		return
	}
	s.recordAudit(r.Context(), "settlement_confirm", record.PartnerID, record.ID, "success", map[string]any{
		"operator": payload.Operator, "reference": payload.Reference,
	})
	s.writeStableJSON(w, http.StatusOK, settlementJSON(record))
}

// handleRetrySettlement re-attempts a failed nowpayments payout submission.
func (s *Server) handleRetrySettlement(w http.ResponseWriter, r *http.Request) {
	if s.settlement == nil {
		s.writeStableJSON(w, http.StatusNotImplemented, map[string]string{"error": "settlement not enabled"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	record, err := s.settlement.RetryNowPayments(r.Context(), id)
	if err != nil {
		status, message := settlementErrorStatus(err)
		s.recordAudit(r.Context(), "settlement_retry", record.PartnerID, id, "error", map[string]any{"error": err.Error()})
		s.writeStableError(w, status, message)
		return
	}
	s.recordAudit(r.Context(), "settlement_retry", record.PartnerID, record.ID, "success", map[string]any{"status": record.Status})
	s.writeStableJSON(w, http.StatusOK, settlementJSON(record))
}

// handleFailSettlement lets an operator explicitly close out a stuck
// pending/submitted settlement (partner cancelled, wire bounced, payout
// rejected) rather than leaving it in limbo forever.
func (s *Server) handleFailSettlement(w http.ResponseWriter, r *http.Request) {
	if s.settlement == nil {
		s.writeStableJSON(w, http.StatusNotImplemented, map[string]string{"error": "settlement not enabled"})
		return
	}
	id := strings.TrimSpace(r.PathValue("id"))
	var payload struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.writeStableError(w, http.StatusBadRequest, "invalid payload")
		return
	}
	record, err := s.settlement.MarkFailed(r.Context(), id, payload.Reason)
	if err != nil {
		status, message := settlementErrorStatus(err)
		s.recordAudit(r.Context(), "settlement_fail", record.PartnerID, id, "error", map[string]any{"error": err.Error()})
		s.writeStableError(w, status, message)
		return
	}
	s.recordAudit(r.Context(), "settlement_fail", record.PartnerID, record.ID, "success", map[string]any{"reason": payload.Reason})
	s.writeStableJSON(w, http.StatusOK, settlementJSON(record))
}

func settlementJSON(rec storage.SettlementRecord) map[string]any {
	out := map[string]any{
		"id":             rec.ID,
		"intent_id":      rec.IntentID,
		"reservation_id": rec.ReservationID,
		"partner_id":     rec.PartnerID,
		"asset":          rec.Asset,
		"amount":         stable.FromAmountUnits(rec.AmountUnits),
		"account":        rec.Account,
		"rail":           rec.Rail,
		"status":         rec.Status,
		"external_ref":   rec.ExternalRef,
		"created_at":     rec.CreatedAt.UTC().Format(time.RFC3339),
		"updated_at":     rec.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if rec.Detail != "" {
		out["detail"] = json.RawMessage(rec.Detail)
	}
	if !rec.SettledAt.IsZero() {
		out["settled_at"] = rec.SettledAt.UTC().Format(time.RFC3339)
	}
	return out
}

func settlementErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, settlement.ErrReceiptRequired), errors.Is(err, settlement.ErrUnknownRail):
		return http.StatusBadRequest, err.Error()
	case errors.Is(err, settlement.ErrNotConfirmable), errors.Is(err, settlement.ErrNotRetryable), errors.Is(err, settlement.ErrNotNowPayments):
		return http.StatusConflict, err.Error()
	case errors.Is(err, settlement.ErrRailNotConfigured):
		return http.StatusInternalServerError, err.Error()
	case errors.Is(err, settlement.ErrManagerUnconfigured):
		return http.StatusNotImplemented, err.Error()
	case strings.Contains(err.Error(), "not found"):
		return http.StatusNotFound, err.Error()
	default:
		return http.StatusInternalServerError, err.Error()
	}
}
