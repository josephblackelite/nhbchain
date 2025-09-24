package rpc

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/crypto"
)

type stakeDelegateParams struct {
	Caller    string `json:"caller"`
	Amount    string `json:"amount"`
	Validator string `json:"validator,omitempty"`
}

type stakeUndelegateParams struct {
	Caller string `json:"caller"`
	Amount string `json:"amount"`
}

type stakeClaimParams struct {
	Caller      string `json:"caller"`
	UnbondingID uint64 `json:"unbondingId"`
}

func parseAmount(amount string) (*big.Int, error) {
	trimmed := strings.TrimSpace(amount)
	if trimmed == "" {
		return nil, fmt.Errorf("amount is required")
	}
	value, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount")
	}
	if value.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	return value, nil
}

func (s *Server) handleStakeDelegate(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params stakeDelegateParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	amount, err := parseAmount(params.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	var validatorPtr *[20]byte
	if strings.TrimSpace(params.Validator) != "" {
		validator, err := decodeBech32(params.Validator)
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid validator", err.Error())
			return
		}
		validatorPtr = &validator
	}
	account, err := s.node.StakeDelegate(callerAddr, amount, validatorPtr)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to delegate stake", err.Error())
		return
	}
	resp := balanceResponseFromAccount(params.Caller, account)
	writeResult(w, req.ID, resp)
}

func (s *Server) handleStakeUndelegate(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params stakeUndelegateParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	amount, err := parseAmount(params.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	unbond, err := s.node.StakeUndelegate(callerAddr, amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to undelegate", err.Error())
		return
	}
	validator := ""
	if len(unbond.Validator) > 0 {
		validator = crypto.NewAddress(crypto.NHBPrefix, unbond.Validator).String()
	}
	amountCopy := big.NewInt(0)
	if unbond.Amount != nil {
		amountCopy = new(big.Int).Set(unbond.Amount)
	}
	resp := StakeUnbondResponse{ID: unbond.ID, Validator: validator, Amount: amountCopy, ReleaseTime: unbond.ReleaseTime}
	writeResult(w, req.ID, resp)
}

func (s *Server) handleStakeClaim(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params stakeClaimParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	claimed, err := s.node.StakeClaim(callerAddr, params.UnbondingID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to claim stake", err.Error())
		return
	}
	account, err := s.node.GetAccount(callerAddr[:])
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load account", err.Error())
		return
	}
	validator := ""
	if len(claimed.Validator) > 0 {
		validator = crypto.NewAddress(crypto.NHBPrefix, claimed.Validator).String()
	}
	amountCopy := big.NewInt(0)
	if claimed.Amount != nil {
		amountCopy = new(big.Int).Set(claimed.Amount)
	}
	result := map[string]interface{}{
		"claimed": StakeUnbondResponse{ID: claimed.ID, Validator: validator, Amount: amountCopy, ReleaseTime: claimed.ReleaseTime},
		"balance": balanceResponseFromAccount(params.Caller, account),
	}
	writeResult(w, req.ID, result)
}
