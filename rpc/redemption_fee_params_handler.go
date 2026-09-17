package rpc

import "net/http"

// handleSwapGetRedemptionFeeParams returns the currently-effective
// redemption fee policy (ad-valorem rate plus its floor/cap) for the swap
// redeem direction (swap-out burn, TxTypeRedeemNHB). Each value is the
// governance param store's value if a policy.redemptionFeeParams proposal
// has ever executed, otherwise the built-in default (see
// native/swap/redemption_fee.go's Default* constants). Mirrors
// handleSwapGetRiskParams exactly, including being deliberately public: this
// exposes no account-specific data, only network-wide policy -- exactly what
// a wallet needs to preview a withdrawal fee, or a governance UI needs to
// display before drafting a policy.redemptionFeeParams proposal.
func (s *Server) handleSwapGetRedemptionFeeParams(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	params, err := s.node.RedemptionFeeParams()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load redemption fee params", err.Error())
		return
	}
	result := map[string]interface{}{
		"feeBps":      params.FeeBps,
		"feeFloorWei": weiOrZero(params.FeeFloorWei),
		"feeCapWei":   weiOrZero(params.FeeCapWei),
	}
	writeResult(w, req.ID, result)
}
