package server

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

// registerPriceProofHandlers wires the optional POST /v1/price-proof
// endpoint into mux when a price-proof service has been configured via
// SetPriceProofRuntime. A no-op otherwise, so a deployment that never calls
// SetPriceProofRuntime (the default) simply never exposes the route.
func (s *Server) registerPriceProofHandlers(mux *http.ServeMux) {
	if mux == nil || s.priceProofService == nil {
		return
	}
	mux.Handle("/v1/price-proof", otelhttp.NewHandler(s.requirePriceProofPartner(http.HandlerFunc(s.handlePriceProof)), "swapd.priceproof"))
}

// handlePriceProof signs a fresh swap.PriceProof for the requested pair
// (default "ZNHB/USD") and returns it in the exact JSON shape
// rpc/swap_handlers.go's handleSwapSubmitVoucher expects for its
// "priceProof" submission field, so callers (e.g. otc-gateway) can embed the
// response directly.
func (s *Server) handlePriceProof(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.priceProofService == nil {
		http.Error(w, "price proof signing disabled", http.StatusServiceUnavailable)
		return
	}
	partner, ok := partnerPrincipalFromRequest(r)
	if !ok {
		http.Error(w, "partner not authorized", http.StatusForbidden)
		return
	}

	var payload struct {
		Pair string `json:"pair"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil && !errors.Is(err, io.EOF) {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
	}
	pair := strings.TrimSpace(payload.Pair)
	if pair == "" {
		pair = "ZNHB/USD"
	}

	proof, err := s.priceProofService.Sign(r.Context(), pair)
	if err != nil {
		if s.logger != nil {
			s.logger.Printf("swapd: price proof sign error (partner=%s pair=%s): %v", partner.ID, pair, err)
		}
		http.Error(w, "failed to sign price proof", http.StatusBadGateway)
		return
	}

	rate := ""
	if proof.Rate != nil {
		rate = proof.Rate.FloatString(18)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"domain":    proof.Domain,
		"provider":  proof.Provider,
		"pair":      proof.Base + "/" + proof.Quote,
		"rate":      rate,
		"timestamp": proof.Timestamp.UTC().Unix(),
		"signature": "0x" + hex.EncodeToString(proof.Signature),
	})
}
