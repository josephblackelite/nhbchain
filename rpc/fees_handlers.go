package rpc

import (
	"encoding/json"
	"net/http"

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
