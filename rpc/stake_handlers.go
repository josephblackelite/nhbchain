package rpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"nhbchain/core"
	"nhbchain/crypto"

	"github.com/ethereum/go-ethereum/common"
)

const stakingModulePausedMessage = "staking module paused"

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

type stakeClaimRewardsResult struct {
	Minted       string          `json:"minted"`
	Balance      BalanceResponse `json:"balance"`
	NextPayoutTs uint64          `json:"nextPayoutTs"`
}

type stakePositionResult struct {
	Shares       string `json:"shares"`
	LastIndex    string `json:"lastIndex"`
	LastPayoutTs uint64 `json:"lastPayoutTs"`
}

type stakePreviewClaimResult struct {
	Payable      string `json:"payable"`
	NextPayoutTs uint64 `json:"nextPayoutTs"`
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
		if errors.Is(err, core.ErrStakePaused) {
			writeError(w, http.StatusServiceUnavailable, req.ID, codeModulePaused, stakingModulePausedMessage, nil)
			return
		}
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
		if errors.Is(err, core.ErrStakePaused) {
			writeError(w, http.StatusServiceUnavailable, req.ID, codeModulePaused, stakingModulePausedMessage, nil)
			return
		}
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to undelegate", err.Error())
		return
	}
	validator := ""
	if len(unbond.Validator) > 0 {
		validator = crypto.MustNewAddress(crypto.NHBPrefix, unbond.Validator).String()
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
		if errors.Is(err, core.ErrStakePaused) {
			writeError(w, http.StatusServiceUnavailable, req.ID, codeModulePaused, stakingModulePausedMessage, nil)
			return
		}
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
		validator = crypto.MustNewAddress(crypto.NHBPrefix, claimed.Validator).String()
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

func (s *Server) handleStakeClaimRewards(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if _, ok := s.guardStakeRequest(w, r, req); !ok {
		return
	}
	addrStr, addrBytes, err := parseStakeAddressParam(req.Params)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	addr := common.BytesToAddress(addrBytes[:])
	minted, _, nextEligible, err := s.node.StakeClaimRewards(addr)
	if err != nil {
		if errors.Is(err, core.ErrStakePaused) {
			writeError(w, http.StatusServiceUnavailable, req.ID, codeModulePaused, stakingModulePausedMessage, nil)
			return
		}
		if errors.Is(err, core.ErrStakingNotReady) {
			writeError(w, http.StatusNotImplemented, req.ID, codeServerError, "staking not ready", nil)
			return
		}
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to claim staking rewards", err.Error())
		return
	}
	account, err := s.node.GetAccount(addrBytes[:])
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load account", err.Error())
		return
	}
	mintedStr := "0"
	if minted != nil {
		mintedStr = minted.String()
	}
	nextPayout := uint64(0)
	if nextEligible > 0 {
		nextPayout = uint64(nextEligible)
	}
	result := stakeClaimRewardsResult{
		Minted:       mintedStr,
		Balance:      balanceResponseFromAccount(addrStr, account),
		NextPayoutTs: nextPayout,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleStakeGetPosition(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if _, ok := s.guardStakeRequest(w, r, req); !ok {
		return
	}
	_, addr, err := parseStakeAddressParam(req.Params)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	account, err := s.node.GetAccount(addr[:])
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load account", err.Error())
		return
	}
	shares := "0"
	lastIndex := "0"
	if account.StakeShares != nil {
		shares = account.StakeShares.String()
	}
	if account.StakeLastIndex != nil {
		lastIndex = account.StakeLastIndex.String()
	}
	result := stakePositionResult{
		Shares:       shares,
		LastIndex:    lastIndex,
		LastPayoutTs: account.StakeLastPayoutTs,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleStakePreviewClaim(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	now, ok := s.guardStakeRequest(w, r, req)
	if !ok {
		return
	}
	_, addr, err := parseStakeAddressParam(req.Params)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	payable, nextPayout, err := s.node.StakePreviewClaim(addr, now)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to preview staking rewards", err.Error())
		return
	}
	payableStr := "0"
	if payable != nil {
		payableStr = payable.String()
	}
	result := stakePreviewClaimResult{
		Payable:      payableStr,
		NextPayoutTs: nextPayout,
	}
	writeResult(w, req.ID, result)
}

func parseStakeAddressParam(params []json.RawMessage) (string, [20]byte, error) {
	if len(params) != 1 {
		return "", [20]byte{}, fmt.Errorf("address parameter required")
	}
	var addrStr string
	if err := json.Unmarshal(params[0], &addrStr); err != nil {
		return "", [20]byte{}, fmt.Errorf("invalid address parameter")
	}
	addr, err := decodeBech32(addrStr)
	if err != nil {
		return "", [20]byte{}, fmt.Errorf("invalid address: %w", err)
	}
	return addrStr, addr, nil
}

func (s *Server) guardStakeRequest(w http.ResponseWriter, r *http.Request, req *RPCRequest) (time.Time, bool) {
	now := time.Now().UTC()
	if s.node == nil {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "node unavailable", nil)
		return time.Time{}, false
	}
	if s.node.IsPaused("staking") {
		writeError(w, http.StatusServiceUnavailable, req.ID, codeModulePaused, stakingModulePausedMessage, nil)
		return time.Time{}, false
	}
	source := s.clientSource(r)
	if !s.allowSource(source, now) {
		writeError(w, http.StatusTooManyRequests, req.ID, codeRateLimited, "staking rate limit exceeded", source)
		return time.Time{}, false
	}
	return now, true
}
