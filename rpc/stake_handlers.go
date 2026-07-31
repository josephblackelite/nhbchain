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
	stakeerrors "nhbchain/core/errors"

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

type stakeClaimRewardsResponse struct {
	Minted       string `json:"minted"`
	Periods      int    `json:"periods"`
	AprBps       uint64 `json:"aprBps"`
	NextEligible uint64 `json:"nextEligibleTs"`
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

// handleStakeDelegate, handleStakeUndelegate, and handleStakeClaim are
// deliberately disabled -- see docs/issue30.md item 3. They trusted a
// client-supplied "caller" address string with no signature proving the
// caller actually controls it, gated only by the shared admin JWT -- so
// anyone holding that JWT could lock, unlock, or move another address's
// stake without their authorization. nhbportal never used these (it signs
// real TxTypeStake/TxTypeUnstake/TxTypeStakeClaim transactions instead,
// which is safe), so nothing legitimate depends on them. Fail loudly rather
// than silently accept an unauthenticated instruction to move someone
// else's funds.
const stakeRPCDisabledMessage = "this method is disabled; sign a transaction (TxTypeStake/TxTypeUnstake/TxTypeStakeClaim) via nhb_sendTransaction instead, so the caller's own signature authorizes the action"

func (s *Server) handleStakeDelegate(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, stakeRPCDisabledMessage, nil)
}

func (s *Server) handleStakeUndelegate(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, stakeRPCDisabledMessage, nil)
}

func (s *Server) handleStakeClaim(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, stakeRPCDisabledMessage, nil)
}

func (s *Server) handleStakeClaimRewards(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if _, ok := s.guardStakeRequest(w, r, req); !ok {
		return
	}
	_, addrBytes, err := parseStakeAddressParam(req.Params)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	addr := common.BytesToAddress(addrBytes[:])
	paid, periods, nextEligible, aprBps, err := s.node.StakeClaimRewards(addr)
	if err != nil {
		if errors.Is(err, core.ErrStakePaused) || errors.Is(err, stakeerrors.ErrStakingPaused) {
			writeError(w, http.StatusServiceUnavailable, req.ID, codeModulePaused, stakingModulePausedMessage, nil)
			return
		}

		if errors.Is(err, stakeerrors.ErrNotDue) {
			data := map[string]interface{}{}
			if nextEligible > 0 {
				data["nextEligibleTs"] = uint64(nextEligible)
			}
			writeError(w, http.StatusConflict, req.ID, codeInvalidParams, err.Error(), data)
			return
		}
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to claim staking rewards", err.Error())
		return
	}
	paidStr := "0"
	if paid != nil {
		paidStr = paid.String()
	}
	nextPayout := uint64(0)
	if nextEligible > 0 {
		nextPayout = uint64(nextEligible)
	}
	result := stakeClaimRewardsResponse{
		Minted:       paidStr,
		Periods:      periods,
		AprBps:       aprBps,
		NextEligible: nextPayout,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleStakeGetPosition(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
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
	if authErr := s.requireAuthInto(&r); authErr != nil {
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
	identity, _ := r.Context().Value(clientIdentityContextKey).(string)
	if !s.allowSource(source, identity, "", req.Method, now) {
		writeError(w, http.StatusTooManyRequests, req.ID, codeRateLimited, "staking rate limit exceeded", source)
		return time.Time{}, false
	}
	return now, true
}
