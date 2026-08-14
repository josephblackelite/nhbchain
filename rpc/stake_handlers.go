package rpc

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
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

// handleStakeDelegate, handleStakeUndelegate, handleStakeClaim, and
// handleStakeClaimRewards are deliberately disabled -- see docs/issue30.md
// item 3. The first three trusted a client-supplied "caller" address string
// with no signature proving the caller actually controls it, gated only by
// the shared admin JWT -- so anyone holding that JWT could lock, unlock, or
// move another address's stake without their authorization. nhbportal never
// used these (it signs real TxTypeStake/TxTypeUnstake/TxTypeStakeClaim
// transactions instead, which is safe), so nothing legitimate depends on
// them.
//
// handleStakeClaimRewards was missed by that original cleanup: it called
// s.node.StakeClaimRewards(addr) directly under n.stateMu.Lock(), mutating
// the live state trie completely outside CreateBlock/ApplyTransaction/
// ValidateBlock -- invisible to any other validator and guaranteed to
// eventually diverge state roots, plus (unlike the three above) it accepted
// the address as a plain client-supplied parameter with no signature at
// all. nhbportal's lendingClient.ts claimRewards() did depend on it (wired
// to the Finance/Lending "Earn" page's claim-rewards button), so it has been
// switched to sign and submit a real TxTypeStakeClaimRewards transaction via
// nhb_sendTransaction (see core/state_transition.go's applyStakeClaimRewards
// and walletManager.ts's claimStakingRewards()) instead of calling this
// method. Fail loudly rather than silently accept an unauthenticated
// instruction to move someone else's funds, or a live but consensus-bypassing
// write.
const stakeRPCDisabledMessage = "this method is disabled; sign a transaction (TxTypeStake/TxTypeUnstake/TxTypeStakeClaim/TxTypeStakeClaimRewards) via nhb_sendTransaction instead, so the caller's own signature authorizes the action"

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
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, stakeRPCDisabledMessage, nil)
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
