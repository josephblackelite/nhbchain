package server

import (
	"encoding/json"
	"errors"
	"net/http"

	"nhbchain/services/otc-gateway/funding"
)

// HandleFundingWebhook ingests custodian notifications and marks invoices as FIAT_CONFIRMED.
func (s *Server) HandleFundingWebhook(w http.ResponseWriter, r *http.Request) {
	if s.Funding == nil {
		http.Error(w, "funding processor unavailable", http.StatusServiceUnavailable)
		return
	}
	var payload funding.Notification
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	invoice, err := s.Funding.Process(r.Context(), payload)
	if err != nil {
		switch {
		case errors.Is(err, funding.ErrInvoiceNotFound):
			http.Error(w, "invoice not found", http.StatusNotFound)
		case errors.Is(err, funding.ErrInvoiceFinalised):
			http.Error(w, "invoice already finalised", http.StatusConflict)
		case errors.Is(err, funding.ErrInvalidState):
			http.Error(w, "invoice not eligible for confirmation", http.StatusConflict)
		case errors.Is(err, funding.ErrPartnerNotApproved):
			http.Error(w, "partner not approved", http.StatusPreconditionFailed)
		case errors.Is(err, funding.ErrMissingDossier):
			http.Error(w, "partner dossier missing", http.StatusPreconditionFailed)
		case errors.Is(err, funding.ErrDossierMismatch):
			http.Error(w, "dossier mismatch", http.StatusForbidden)
		default:
			http.Error(w, err.Error(), http.StatusBadRequest)
		}
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"invoice_id":        invoice.ID,
		"state":             invoice.State,
		"funding_status":    invoice.FundingStatus,
		"funding_reference": invoice.FundingReference,
	})
}
