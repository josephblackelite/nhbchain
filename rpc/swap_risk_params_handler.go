package rpc

import (
	"math/big"
	"net/http"
)

// handleSwapGetRiskParams returns the currently-effective circuit-breaker
// caps for the swap redeem direction (swap-out burn, TxTypeRedeemNHB). Each
// value is the governance param store's value if a policy.swapRiskParams
// proposal has ever executed, otherwise the conservative built-in default
// (see native/swap/redeem_risk.go's Default* constants).
//
// There is no mint-side equivalent: ZNHB voucher mints (TxTypeSwapVoucherMint)
// draw from a fixed, pre-allocated genesis treasury Sale Pool rather than
// minting new supply, so they carry no external financial risk requiring a
// governance-adjustable circuit breaker -- see core/swap_voucher_tx.go's
// applySwapVoucherMintTransaction and ProposalKindSwapRiskParams's doc
// comment. The response is still nested under a "redeem" key (rather than
// flattening the four fields to the top level) to keep the response shape
// stable for existing/concurrent consumers built against the two-direction
// shape -- only the "mint" key has been removed.
//
// Deliberately public: registered in isPublicSwapMethod with no
// requireAuthInto gate, unlike the sensitive swap_* admin methods
// (swap_limits, swap_burn_list, swap_provider_status, swap_voucher_reverse,
// swap_setManualQuote, swap_listPendingRedemptions). This method exposes no
// account-specific data at all, only network-wide parameters -- exactly
// what a governance UI needs to display before drafting a
// policy.swapRiskParams proposal.
func (s *Server) handleSwapGetRiskParams(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	redeem, err := s.node.SwapRiskParams()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load swap risk params", err.Error())
		return
	}
	result := map[string]interface{}{
		"redeem": map[string]interface{}{
			"perTxMinWei":             weiOrZero(redeem.PerTxMinWei),
			"perTxMaxWei":             weiOrZero(redeem.PerTxMaxWei),
			"perAddressDailyCapWei":   weiOrZero(redeem.PerAddressDailyCapWei),
			"perAddressMonthlyCapWei": weiOrZero(redeem.PerAddressMonthlyCapWei),
		},
	}
	writeResult(w, req.ID, result)
}

// weiOrZero renders a possibly-nil wei amount as its canonical decimal
// string, defaulting to "0" -- PerTxMinWei in particular is legitimately nil
// (no floor configured) rather than an error condition.
func weiOrZero(amount *big.Int) string {
	if amount == nil {
		return "0"
	}
	return amount.String()
}
