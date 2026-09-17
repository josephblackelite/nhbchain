package rpc

import (
	"encoding/json"
	"net/http"
	"strings"

	"nhbchain/crypto"
)

type feesMonthlyStatusResult struct {
	Window       string `json:"window_yyyymm"`
	Used         uint64 `json:"used"`
	Remaining    uint64 `json:"remaining"`
	LastRollover string `json:"last_rollover_yyyymm"`
}

type feesTransferStatusParams struct {
	Address string `json:"address"`
}

type feesTransferStatusResult struct {
	Window        string `json:"window"`
	WindowKey     string `json:"window_key"`
	SpentWei      string `json:"spentWei"`
	FreeLimitWei  string `json:"freeLimitWei"`
	RemainingWei  string `json:"remainingWei"`
	Eligible      bool   `json:"eligible"`
	NextResetUnix int64  `json:"nextResetUnix,omitempty"`
}

type feesTransferQuoteParams struct {
	Address   string `json:"address"`
	Asset     string `json:"asset"`
	AmountWei string `json:"amountWei"`
}

type feesTransferQuoteResult struct {
	Eligible bool   `json:"eligible"`
	FeeWei   string `json:"feeWei"`
	FeeBps   uint32 `json:"feeBps"`
}

func (s *Server) handleFeesGetMonthlyStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	status, err := s.node.FeesMonthlyStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load monthly status", err.Error())
		return
	}
	result := feesMonthlyStatusResult{
		Window:       status.Window,
		Used:         status.Used,
		Remaining:    status.Remaining,
		LastRollover: status.LastRollover,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleFeesGetTransferStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address parameter required", nil)
		return
	}
	var params feesTransferStatusParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid params", err.Error())
		return
	}
	addr, err := crypto.DecodeAddress(params.Address)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	status, err := s.node.TransferGasStatus(addr.Bytes())
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load transfer status", err.Error())
		return
	}
	result := feesTransferStatusResult{
		Window:       status.Window,
		WindowKey:    status.WindowKey,
		SpentWei:     status.Spent.String(),
		FreeLimitWei: status.FreeLimit.String(),
		RemainingWei: status.Remaining.String(),
		Eligible:     status.Eligible,
	}
	if !status.NextReset.IsZero() {
		result.NextResetUnix = status.NextReset.UTC().Unix()
	}
	writeResult(w, req.ID, result)
}

// handleFeesGetTransferQuote returns the fee that would actually be charged
// for a transfer of amountWei in the given asset from address, reusing the
// same free-tier eligibility check as fees_getTransferStatus. When the
// wallet is within its free tier the quoted fee is zero; otherwise the fee
// is computed from the live protocol-enforced rate for the requested asset
// (TransferGasPolicy.FeeBps for NHB, TransferGasPolicy.FeeBpsZNHB for ZNHB --
// see TransferGasPolicy.FeeBpsForAsset). The response's FeeBps field always
// echoes back whichever rate actually applied to this request's asset.
func (s *Server) handleFeesGetTransferQuote(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address, asset, and amountWei parameters required", nil)
		return
	}
	var params feesTransferQuoteParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid params", err.Error())
		return
	}
	addr, err := crypto.DecodeAddress(params.Address)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	asset := strings.ToUpper(strings.TrimSpace(params.Asset))
	if asset != "NHB" && asset != "ZNHB" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid asset", "asset must be NHB or ZNHB")
		return
	}
	amount, err := parsePositiveBigInt(params.AmountWei)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid amountWei", err.Error())
		return
	}
	status, err := s.node.TransferGasStatusForAsset(addr.Bytes(), asset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load transfer status", err.Error())
		return
	}
	policy := s.node.TransferGasPolicy()
	result := feesTransferQuoteResult{
		Eligible: status.Eligible,
		FeeWei:   "0",
		FeeBps:   policy.FeeBpsForAsset(asset),
	}
	if !status.Eligible {
		result.FeeWei = policy.ComputeFee(asset, amount).String()
	}
	writeResult(w, req.ID, result)
}
