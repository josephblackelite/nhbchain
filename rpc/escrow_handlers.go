package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"nhbchain/core"
	"nhbchain/core/genesis"
	"nhbchain/crypto"
	"nhbchain/native/escrow"
)

const (
	codeEscrowInvalidParams = -32021
	codeEscrowNotFound      = -32022
	codeEscrowForbidden     = -32023
	codeEscrowConflict      = -32024
	codeEscrowInternal      = -32025
)

const deadlineSkewSeconds int64 = 5

type escrowCreateParams struct {
	Payer    string `json:"payer"`
	Payee    string `json:"payee"`
	Token    string `json:"token"`
	Amount   string `json:"amount"`
	FeeBps   uint32 `json:"feeBps"`
	Deadline int64  `json:"deadline"`
	Nonce    uint64 `json:"nonce"`
	Mediator string `json:"mediator,omitempty"`
	MetaHex  string `json:"meta,omitempty"`
	Realm    string `json:"realm,omitempty"`
}

type escrowIDParams struct {
	ID string `json:"id"`
}

type escrowActorParams struct {
	ID     string `json:"id"`
	Caller string `json:"caller"`
}

type escrowFundParams struct {
	ID   string `json:"id"`
	From string `json:"from"`
}

type escrowResolveParams struct {
	ID      string `json:"id"`
	Caller  string `json:"caller"`
	Outcome string `json:"outcome"`
}

type escrowCreateResult struct {
	ID string `json:"id"`
}

type escrowJSON struct {
	ID           string   `json:"id"`
	Payer        string   `json:"payer"`
	Payee        string   `json:"payee"`
	Mediator     *string  `json:"mediator,omitempty"`
	Token        string   `json:"token"`
	Amount       string   `json:"amount"`
	FeeBps       uint32   `json:"feeBps"`
	Deadline     int64    `json:"deadline"`
	CreatedAt    int64    `json:"createdAt"`
	Nonce        uint64   `json:"nonce"`
	Status       string   `json:"status"`
	Meta         string   `json:"meta"`
	Realm        *string  `json:"realm,omitempty"`
	RealmVersion *uint64  `json:"realmVersion,omitempty"`
	PolicyNonce  *uint64  `json:"policyNonce,omitempty"`
	ArbScheme    *uint8   `json:"arbScheme,omitempty"`
	ArbThreshold *uint32  `json:"arbThreshold,omitempty"`
	FrozenAt     *int64   `json:"frozenAt,omitempty"`
	Arbitrators  []string `json:"arbitrators,omitempty"`
}

func (s *Server) handleEscrowCreate(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params escrowCreateParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	payer, err := parseBech32Address(params.Payer)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	payee, err := parseBech32Address(params.Payee)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	token := strings.ToUpper(strings.TrimSpace(params.Token))
	if token != "NHB" && token != "ZNHB" {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "token must be NHB or ZNHB")
		return
	}
	amount, err := parsePositiveBigInt(params.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	if params.FeeBps > 10_000 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "feeBps must be <= 10000")
		return
	}
	now := time.Now().Unix()
	if params.Deadline < now-deadlineSkewSeconds {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "deadline must be in the future")
		return
	}
	if params.Nonce == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "nonce must be > 0")
		return
	}
	meta, err := parseMetaHex(params.MetaHex)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	var mediatorPtr *[20]byte
	if strings.TrimSpace(params.Mediator) != "" {
		mediator, parseErr := parseBech32Address(params.Mediator)
		if parseErr != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", parseErr.Error())
			return
		}
		mediatorCopy := mediator
		mediatorPtr = &mediatorCopy
	}
	id, err := s.node.EscrowCreate(payer, payee, token, amount, params.FeeBps, params.Deadline, params.Nonce, mediatorPtr, meta, strings.TrimSpace(params.Realm))
	if err != nil {
		writeEscrowError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, escrowCreateResult{ID: formatEscrowID(id)})
}

func (s *Server) handleEscrowGet(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params escrowIDParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	esc, err := s.node.EscrowGet(id)
	if err != nil {
		writeEscrowError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, formatEscrowJSON(esc))
}

func (s *Server) handleEscrowFund(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params escrowFundParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	from, err := parseBech32Address(params.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	if err := s.node.EscrowFund(id, from); err != nil {
		writeEscrowError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, "ok")
}

func (s *Server) handleEscrowRelease(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	s.handleEscrowTransition(w, r, req, s.node.EscrowRelease)
}

func (s *Server) handleEscrowRefund(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	s.handleEscrowTransition(w, r, req, s.node.EscrowRefund)
}

func (s *Server) handleEscrowDispute(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	s.handleEscrowTransition(w, r, req, s.node.EscrowDispute)
}

func (s *Server) handleEscrowTransition(w http.ResponseWriter, r *http.Request, req *RPCRequest, fn func([32]byte, [20]byte) error) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params escrowActorParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	caller, err := parseBech32Address(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	if err := fn(id, caller); err != nil {
		writeEscrowError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, "ok")
}

func (s *Server) handleEscrowExpire(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params escrowIDParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	if err := s.node.EscrowExpire(id); err != nil {
		writeEscrowError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, "ok")
}

func (s *Server) handleEscrowResolve(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "exactly one parameter object expected")
		return
	}
	var params escrowResolveParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	id, err := parseEscrowID(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	caller, err := parseBech32Address(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", err.Error())
		return
	}
	outcome := strings.ToLower(strings.TrimSpace(params.Outcome))
	if outcome != "release" && outcome != "refund" {
		writeError(w, http.StatusBadRequest, req.ID, codeEscrowInvalidParams, "invalid_params", "outcome must be release or refund")
		return
	}
	if err := s.node.EscrowResolve(id, caller, outcome); err != nil {
		writeEscrowError(w, req.ID, err)
		return
	}
	writeResult(w, req.ID, "ok")
}

func parseBech32Address(addr string) ([20]byte, error) {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return [20]byte{}, fmt.Errorf("address required")
	}
	return genesis.ParseBech32Account(trimmed)
}

func parsePositiveBigInt(value string) (*big.Int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, fmt.Errorf("amount required")
	}
	amount, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount")
	}
	if amount.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	return amount, nil
}

func parseMetaHex(value string) ([32]byte, error) {
	var out [32]byte
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return out, nil
	}
	if !strings.HasPrefix(strings.ToLower(trimmed), "0x") {
		return out, fmt.Errorf("meta must be 0x-prefixed")
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if len(cleaned)%2 != 0 {
		return out, fmt.Errorf("meta hex length must be even")
	}
	bytes, err := hex.DecodeString(cleaned)
	if err != nil {
		return out, err
	}
	if len(bytes) > len(out) {
		return out, fmt.Errorf("meta must be <= 32 bytes")
	}
	copy(out[:], bytes)
	return out, nil
}

func parseEscrowID(id string) ([32]byte, error) {
	var out [32]byte
	trimmed := strings.TrimSpace(id)
	if trimmed == "" {
		return out, fmt.Errorf("id required")
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if len(cleaned) != 64 {
		return out, fmt.Errorf("id must be 32 bytes")
	}
	bytes, err := hex.DecodeString(cleaned)
	if err != nil {
		return out, err
	}
	copy(out[:], bytes)
	return out, nil
}

func formatEscrowID(id [32]byte) string {
	return "0x" + hex.EncodeToString(id[:])
}

func formatEscrowJSON(esc *escrow.Escrow) escrowJSON {
	payer := crypto.MustNewAddress(crypto.NHBPrefix, esc.Payer[:]).String()
	payee := crypto.MustNewAddress(crypto.NHBPrefix, esc.Payee[:]).String()
	var mediatorPtr *string
	if esc.Mediator != ([20]byte{}) {
		mediator := crypto.MustNewAddress(crypto.NHBPrefix, esc.Mediator[:]).String()
		mediatorPtr = &mediator
	}
	amount := "0"
	if esc.Amount != nil {
		amount = esc.Amount.String()
	}
	meta := "0x" + hex.EncodeToString(esc.MetaHash[:])
	var realmPtr *string
	var realmVersionPtr *uint64
	var policyNoncePtr *uint64
	var schemePtr *uint8
	var thresholdPtr *uint32
	var frozenAtPtr *int64
	var arbitrators []string
	if trimmed := strings.TrimSpace(esc.RealmID); trimmed != "" {
		realm := trimmed
		realmPtr = &realm
		if esc.FrozenArb != nil {
			realmVersion := esc.FrozenArb.RealmVersion
			realmVersionPtr = &realmVersion
			policyNonce := esc.FrozenArb.PolicyNonce
			policyNoncePtr = &policyNonce
			scheme := uint8(esc.FrozenArb.Scheme)
			schemePtr = &scheme
			threshold := esc.FrozenArb.Threshold
			thresholdPtr = &threshold
			if esc.FrozenArb.FrozenAt != 0 {
				frozen := esc.FrozenArb.FrozenAt
				frozenAtPtr = &frozen
			}
			if len(esc.FrozenArb.Members) > 0 {
				arbitrators = make([]string, 0, len(esc.FrozenArb.Members))
				for _, member := range esc.FrozenArb.Members {
					arbitrators = append(arbitrators, crypto.MustNewAddress(crypto.NHBPrefix, member[:]).String())
				}
			}
		}
	}
	return escrowJSON{
		ID:           formatEscrowID(esc.ID),
		Payer:        payer,
		Payee:        payee,
		Mediator:     mediatorPtr,
		Token:        esc.Token,
		Amount:       amount,
		FeeBps:       esc.FeeBps,
		Deadline:     esc.Deadline,
		CreatedAt:    esc.CreatedAt,
		Nonce:        esc.Nonce,
		Status:       escrowStatusString(esc.Status),
		Meta:         meta,
		Realm:        realmPtr,
		RealmVersion: realmVersionPtr,
		PolicyNonce:  policyNoncePtr,
		ArbScheme:    schemePtr,
		ArbThreshold: thresholdPtr,
		FrozenAt:     frozenAtPtr,
		Arbitrators:  arbitrators,
	}
}

func escrowStatusString(status escrow.EscrowStatus) string {
	switch status {
	case escrow.EscrowInit:
		return "init"
	case escrow.EscrowFunded:
		return "funded"
	case escrow.EscrowReleased:
		return "released"
	case escrow.EscrowRefunded:
		return "refunded"
	case escrow.EscrowExpired:
		return "expired"
	case escrow.EscrowDisputed:
		return "disputed"
	default:
		return "unknown"
	}
}

func writeEscrowError(w http.ResponseWriter, id interface{}, err error) {
	if err == nil {
		return
	}
	status := http.StatusInternalServerError
	code := codeEscrowInternal
	message := "internal_error"
	data := err.Error()
	switch {
	case errors.Is(err, core.ErrEscrowNotFound) || strings.Contains(err.Error(), "escrow engine: escrow not found"):
		status = http.StatusNotFound
		code = codeEscrowNotFound
		message = "not_found"
	case strings.Contains(err.Error(), "unauthorized") || strings.Contains(err.Error(), "requires mediator"):
		status = http.StatusForbidden
		code = codeEscrowForbidden
		message = "forbidden"
	case strings.Contains(err.Error(), "cannot ") || strings.Contains(err.Error(), "deadline not reached") || strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "refund deadline passed"):
		status = http.StatusConflict
		code = codeEscrowConflict
		message = "conflict"
	case strings.Contains(err.Error(), "escrow engine: fee treasury not configured") || strings.Contains(err.Error(), "escrow: nil escrow") || strings.Contains(err.Error(), "escrow: unsupported token"):
		status = http.StatusInternalServerError
		code = codeEscrowInternal
		message = "internal_error"
	case strings.Contains(err.Error(), "escrow: amount must be positive"):
		status = http.StatusConflict
		code = codeEscrowConflict
		message = "conflict"
	}
	writeError(w, status, id, code, message, data)
}
