package rpc

import (
	"encoding/json"
	"errors"
	"net/http"

	"nhbchain/core"
)

type engagementRegisterDeviceParams struct {
	Address  string `json:"address"`
	DeviceID string `json:"deviceId"`
}

type engagementRegisterDeviceResult struct {
	Token string `json:"token"`
}

type engagementSubmitHeartbeatParams struct {
	DeviceID  string `json:"deviceId"`
	Token     string `json:"token"`
	Timestamp int64  `json:"timestamp,omitempty"`
}

type engagementSubmitHeartbeatResult struct {
	Queued    bool  `json:"queued"`
	Timestamp int64 `json:"timestamp"`
}

func (s *Server) handleEngagementRegisterDevice(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "register requires parameter object", nil)
		return
	}
	var params engagementRegisterDeviceParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid request parameters", err.Error())
		return
	}
	if params.Address == "" || params.DeviceID == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address and deviceId are required", nil)
		return
	}
	addr, err := decodeBech32(params.Address)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	token, err := s.node.EngagementRegisterDevice(addr, params.DeviceID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	writeResult(w, req.ID, engagementRegisterDeviceResult{Token: token})
}

func (s *Server) handleEngagementSubmitHeartbeat(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "submit requires parameter object", nil)
		return
	}
	var params engagementSubmitHeartbeatParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid request parameters", err.Error())
		return
	}
	if params.DeviceID == "" || params.Token == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "deviceId and token are required", nil)
		return
	}
	ts, err := s.node.EngagementSubmitHeartbeat(params.DeviceID, params.Token, params.Timestamp)
	if err != nil {
		if errors.Is(err, core.ErrMempoolFull) {
			writeError(w, http.StatusServiceUnavailable, req.ID, codeMempoolFull, "mempool full", nil)
			return
		}
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	writeResult(w, req.ID, engagementSubmitHeartbeatResult{Queued: true, Timestamp: ts})
}
