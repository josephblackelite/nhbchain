package rpc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"nhbchain/crypto"
	"nhbchain/native/potso"
)

type potsoHeartbeatParams struct {
	User          string `json:"user"`
	LastBlock     uint64 `json:"lastBlock"`
	LastBlockHash string `json:"lastBlockHash"`
	Timestamp     int64  `json:"timestamp"`
	Signature     string `json:"signature"`
}

type potsoHeartbeatResult struct {
	Accepted    bool         `json:"accepted"`
	UptimeDelta uint64       `json:"uptimeDelta"`
	Meter       *potso.Meter `json:"meter"`
}

type potsoUserMetersParams struct {
	User string `json:"user"`
	Day  string `json:"day,omitempty"`
}

type potsoTopParams struct {
	Day   string `json:"day,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type potsoTopEntry struct {
	User  string       `json:"user"`
	Meter *potso.Meter `json:"meter"`
}

func heartbeatDigest(user string, block uint64, hash []byte, ts int64) []byte {
	payload := fmt.Sprintf("potso_heartbeat|%s|%d|%s|%d", strings.ToLower(strings.TrimSpace(user)), block, strings.ToLower(hex.EncodeToString(hash)), ts)
	digest := sha256.Sum256([]byte(payload))
	return digest[:]
}

func decodeHexBytes(value string) ([]byte, error) {
	cleaned := strings.TrimPrefix(strings.TrimSpace(value), "0x")
	if len(cleaned)%2 == 1 {
		cleaned = "0" + cleaned
	}
	if cleaned == "" {
		return nil, fmt.Errorf("hex value required")
	}
	return hex.DecodeString(cleaned)
}

func (s *Server) handlePotsoHeartbeat(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	// Heartbeats used to mutate consensus state directly over JSON-RPC, which
	// let wallet background polling diverge validator trie roots outside the
	// canonical block path. Until heartbeats are mediated through committed
	// transactions / epoch processing, keep this endpoint read-only-disabled.
	writeError(
		w,
		http.StatusServiceUnavailable,
		req.ID,
		codeServerError,
		"potso heartbeat rpc is temporarily disabled; submit engagement through the canonical transaction pipeline",
		nil,
	)
}

func (s *Server) handlePotsoUserMeters(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "userMeters requires parameter object", nil)
		return
	}
	var params potsoUserMetersParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid request parameters", err.Error())
		return
	}
	if params.User == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "user is required", nil)
		return
	}
	addr, err := decodeBech32(params.User)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid user", err.Error())
		return
	}
	meter, err := s.node.PotsoUserMeters(addr, params.Day)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	writeResult(w, req.ID, meter)
}

func (s *Server) handlePotsoTop(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "top requires parameter object", nil)
		return
	}
	var params potsoTopParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid request parameters", err.Error())
		return
	}
	entries, err := s.node.PotsoTop(params.Day, params.Limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	result := make([]potsoTopEntry, len(entries))
	for i, entry := range entries {
		user := crypto.MustNewAddress(crypto.NHBPrefix, entry.Address[:]).String()
		result[i] = potsoTopEntry{User: user, Meter: entry.Meter}
	}
	writeResult(w, req.ID, result)
}
