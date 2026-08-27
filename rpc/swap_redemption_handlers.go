package rpc

import (
	"net/http"
	"strings"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
)

// handleSwapListPendingRedemptions returns every currently pending
// TxTypeRedeemNHB swap-out request. This is how payments-gateway (the
// off-chain NOWPayments payout watcher) discovers burns awaiting payout --
// see core/state/redemption.go's pending-request index and
// core/node.go's ListPendingRedemptions.
//
// Deliberately NOT registered in isPublicSwapMethod: unlike the
// partner-facing swap_submitVoucher/nhb_swapMint family, this exposes
// addresses, NHB burn amounts, and destination crypto addresses for every
// pending redemption across all accounts, so it is gated purely by
// requireAuthInto (the same JWT/mTLS bearer auth used by the other
// sensitive swap_* admin methods -- swap_limits, swap_burn_list,
// swap_provider_status, swap_voucher_reverse, swap_setManualQuote; see
// rpc/http.go's dispatch switch).
func (s *Server) handleSwapListPendingRedemptions(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	requests, err := s.node.ListPendingRedemptions()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to list pending redemptions", err.Error())
		return
	}
	formatted := make([]map[string]interface{}, 0, len(requests))
	for _, request := range requests {
		formatted = append(formatted, formatRedemptionRequest(request))
	}
	writeResult(w, req.ID, map[string]interface{}{"requests": formatted})
}

func formatRedemptionRequest(request *nhbstate.StoredRedemptionRequest) map[string]interface{} {
	if request == nil {
		return nil
	}
	payload := map[string]interface{}{
		"requestId":          request.RequestID,
		"nhbAmountWei":       request.NHBAmountWei,
		"destinationAsset":   request.DestinationAsset,
		"destinationAddress": request.DestinationAddress,
		"status":             request.Status,
		"createdAt":          request.CreatedAt,
	}
	if len(request.Account) == 20 {
		var addr [20]byte
		copy(addr[:], request.Account)
		payload["account"] = crypto.MustNewAddress(crypto.NHBPrefix, addr[:]).String()
	}
	if request.SettledAt > 0 {
		payload["settledAt"] = request.SettledAt
	}
	if strings.TrimSpace(request.PayoutReference) != "" {
		payload["payoutReference"] = strings.TrimSpace(request.PayoutReference)
	}
	if strings.TrimSpace(request.FailureReason) != "" {
		payload["failureReason"] = strings.TrimSpace(request.FailureReason)
	}
	return payload
}
