package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"nhbchain/core/claimable"
	"nhbchain/crypto"
)

const (
	codeClaimableInvalidParams = -32041
	codeClaimableNotFound      = -32042
	codeClaimableForbidden     = -32043
	codeClaimableConflict      = -32044
	codeClaimableInternal      = -32045
)

type claimableCreateParams struct {
	Payer    string `json:"payer"`
	Token    string `json:"token"`
	Amount   string `json:"amount"`
	Deadline int64  `json:"deadline"`
	HashLock string `json:"hashLock"`
	callerMetadataParams
}

type claimableIDParams struct {
	ID string `json:"id"`
}

type claimableClaimParams struct {
	ID       string `json:"id"`
	Preimage string `json:"preimage"`
	Payee    string `json:"payee"`
	callerMetadataParams
}

type claimableCancelParams struct {
	ID     string `json:"id"`
	Caller string `json:"caller"`
	callerMetadataParams
}

type claimableCreateResult struct {
	ID string `json:"id"`
}

type claimableOKResult struct {
	OK bool `json:"ok"`
}

type claimableJSON struct {
	ID        string `json:"id"`
	Payer     string `json:"payer"`
	Token     string `json:"token"`
	Amount    string `json:"amount"`
	HashLock  string `json:"hashLock"`
	Deadline  int64  `json:"deadline"`
	CreatedAt int64  `json:"createdAt"`
	Status    string `json:"status"`
}

func (s *Server) handleClaimableCreate(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params claimableCreateParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	payer, err := parseBech32Address(params.Payer)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	if err := s.validateCallerMetadata(callerKeyFromAddress(payer), params.callerMetadataParams); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	token := strings.ToUpper(strings.TrimSpace(params.Token))
	if token != "NHB" && token != "ZNHB" {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", "token must be NHB or ZNHB")
		return
	}
	amount, err := parsePositiveBigInt(params.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	now := time.Now().Unix()
	if params.Deadline < now-deadlineSkewSeconds {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", "deadline must be in the future")
		return
	}
	hashLock, err := parseHashLock(params.HashLock)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := s.node.ClaimableCreate(payer, token, amount, hashLock, params.Deadline)
	if err != nil {
		writeClaimableError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, claimableCreateResult{ID: formatClaimableID(id)})
}

func (s *Server) handleClaimableClaim(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params claimableClaimParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseClaimableID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	payee, err := parseBech32Address(params.Payee)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	preimage, err := parseHexBytes(params.Preimage)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	if err := s.validateCallerMetadata(callerKeyFromAddress(payee), params.callerMetadataParams); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	if err := s.node.ClaimableClaim(id, preimage, payee); err != nil {
		writeClaimableError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, claimableOKResult{OK: true})
}

func (s *Server) handleClaimableCancel(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params claimableCancelParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseClaimableID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	caller, err := parseBech32Address(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	if err := s.validateCallerMetadata(callerKeyFromAddress(caller), params.callerMetadataParams); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	if err := s.node.ClaimableCancel(id, caller); err != nil {
		writeClaimableError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, claimableOKResult{OK: true})
}

func (s *Server) handleClaimableGet(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params claimableIDParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseClaimableID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeClaimableInvalidParams, "invalid_params", err.Error())
		return
	}
	record, err := s.node.ClaimableGet(id)
	if err != nil {
		writeClaimableError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, formatClaimableJSON(record))
}

func parseHashLock(value string) ([32]byte, error) {
	var out [32]byte
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return out, fmt.Errorf("hashLock required")
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if len(cleaned) != 64 {
		return out, fmt.Errorf("hashLock must be 32 bytes")
	}
	decoded, err := hex.DecodeString(cleaned)
	if err != nil {
		return out, err
	}
	copy(out[:], decoded)
	return out, nil
}

func parseHexBytes(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if cleaned == "" {
		return []byte{}, nil
	}
	return hex.DecodeString(cleaned)
}

func parseClaimableID(id string) ([32]byte, error) {
	return parseEscrowID(id)
}

func formatClaimableID(id [32]byte) string {
	return "0x" + hex.EncodeToString(id[:])
}

func formatClaimableJSON(record *claimable.Claimable) claimableJSON {
	amount := "0"
	if record.Amount != nil {
		amount = record.Amount.String()
	}
	return claimableJSON{
		ID:        formatClaimableID(record.ID),
		Payer:     crypto.MustNewAddress(crypto.NHBPrefix, record.Payer[:]).String(),
		Token:     record.Token,
		Amount:    amount,
		HashLock:  "0x" + hex.EncodeToString(record.HashLock[:]),
		Deadline:  record.Deadline,
		CreatedAt: record.CreatedAt,
		Status:    record.Status.String(),
	}
}

func writeClaimableError(w http.ResponseWriter, id interface{}, err error) {
	if err == nil {
		return
	}
	status := http.StatusInternalServerError
	code := codeClaimableInternal
	message := "internal_error"
	data := err.Error()
	switch {
	case errors.Is(err, claimable.ErrNotFound):
		status = http.StatusNotFound
		code = codeClaimableNotFound
		message = "not_found"
	case errors.Is(err, claimable.ErrUnauthorized):
		status = http.StatusForbidden
		code = codeClaimableForbidden
		message = "forbidden"
	case errors.Is(err, claimable.ErrInvalidToken) || errors.Is(err, claimable.ErrInvalidAmount):
		status = http.StatusBadRequest
		code = codeClaimableInvalidParams
		message = "invalid_params"
	case errors.Is(err, claimable.ErrInvalidPreimage) || errors.Is(err, claimable.ErrInvalidState) ||
		errors.Is(err, claimable.ErrDeadlineExceeded) || errors.Is(err, claimable.ErrNotExpired) ||
		errors.Is(err, claimable.ErrInsufficientFunds):
		status = http.StatusConflict
		code = codeClaimableConflict
		message = "conflict"
	}
	writeError(w, status, id, code, message, data)
}
