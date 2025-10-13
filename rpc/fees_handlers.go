package rpc

import (
	"net/http"
)

type feesMonthlyStatusResult struct {
	Window       string `json:"window_yyyymm"`
	Used         uint64 `json:"used"`
	Remaining    uint64 `json:"remaining"`
	LastRollover string `json:"last_rollover_yyyymm"`
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
