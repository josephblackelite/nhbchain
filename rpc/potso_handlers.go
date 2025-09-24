package rpc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

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
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "heartbeat requires parameter object", nil)
		return
	}
	var params potsoHeartbeatParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid request parameters", err.Error())
		return
	}
	if params.User == "" || params.Signature == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "user and signature are required", nil)
		return
	}
	addr, err := decodeBech32(params.User)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid user", err.Error())
		return
	}
	blockHash, err := decodeHexBytes(params.LastBlockHash)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid block hash", err.Error())
		return
	}
	sig, err := decodeHexBytes(params.Signature)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid signature", err.Error())
		return
	}
	if len(sig) != 65 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "signature must be 65 bytes", nil)
		return
	}
	digest := heartbeatDigest(params.User, params.LastBlock, blockHash, params.Timestamp)
	pubKey, err := ethcrypto.SigToPub(digest, sig)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid signature", err.Error())
		return
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	if !strings.EqualFold(recovered.Hex()[2:], hex.EncodeToString(addr[:])) {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "signature does not match user", nil)
		return
	}
	meter, delta, err := s.node.PotsoHeartbeat(addr, params.LastBlock, blockHash, params.Timestamp)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	result := potsoHeartbeatResult{Accepted: delta > 0, UptimeDelta: delta, Meter: meter}
	writeResult(w, req.ID, result)
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
		user := crypto.NewAddress(crypto.NHBPrefix, entry.Address[:]).String()
		result[i] = potsoTopEntry{User: user, Meter: entry.Meter}
	}
	writeResult(w, req.ID, result)
}
