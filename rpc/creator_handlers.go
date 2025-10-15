package rpc

import (
	"encoding/json"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/crypto"
	"nhbchain/native/creator"
)

type creatorPublishParams struct {
	Caller    string `json:"caller"`
	ContentID string `json:"contentId"`
	URI       string `json:"uri"`
	Metadata  string `json:"metadata"`
}

type creatorTipParams struct {
	Caller    string `json:"caller"`
	ContentID string `json:"contentId"`
	Amount    string `json:"amount"`
}

type creatorStakeParams struct {
	Caller  string `json:"caller"`
	Creator string `json:"creator"`
	Amount  string `json:"amount"`
}

type creatorUnstakeParams struct {
	Caller  string `json:"caller"`
	Creator string `json:"creator"`
	Amount  string `json:"amount"`
}

type creatorPayoutsParams struct {
	Caller string `json:"caller"`
	Claim  bool   `json:"claim,omitempty"`
}

type creatorContentResult struct {
	ID          string `json:"id"`
	Creator     string `json:"creator"`
	URI         string `json:"uri"`
	Metadata    string `json:"metadata"`
	PublishedAt int64  `json:"publishedAt"`
	TotalTips   string `json:"totalTips"`
	TotalStake  string `json:"totalStake"`
}

type creatorTipResult struct {
	ContentID  string `json:"contentId"`
	Creator    string `json:"creator"`
	Fan        string `json:"fan"`
	Amount     string `json:"amount"`
	TippedAt   int64  `json:"tippedAt"`
	Pending    string `json:"pending"`
	TotalTips  string `json:"totalTips"`
	TotalYield string `json:"totalYield"`
}

type creatorStakeResult struct {
	Creator    string `json:"creator"`
	Fan        string `json:"fan"`
	Amount     string `json:"amount"`
	Shares     string `json:"shares"`
	StakedAt   int64  `json:"stakedAt"`
	Reward     string `json:"reward"`
	Pending    string `json:"pending"`
	TotalTips  string `json:"totalTips"`
	TotalYield string `json:"totalYield"`
}

type creatorUnstakeResult struct {
	Creator   string `json:"creator"`
	Fan       string `json:"fan"`
	Amount    string `json:"amount"`
	Remaining string `json:"remaining"`
	Shares    string `json:"shares"`
}

type creatorPayoutsResult struct {
	Creator    string `json:"creator"`
	Pending    string `json:"pending"`
	TotalTips  string `json:"totalTips"`
	TotalYield string `json:"totalYield"`
	LastPayout int64  `json:"lastPayout"`
	Claimed    string `json:"claimed"`
}

func formatCreatorContent(addr string, content *creator.Content) creatorContentResult {
	totalTips := "0"
	if content.TotalTips != nil {
		totalTips = content.TotalTips.String()
	}
	totalStake := "0"
	if content.TotalStake != nil {
		totalStake = content.TotalStake.String()
	}
	return creatorContentResult{
		ID:          content.ID,
		Creator:     addr,
		URI:         content.URI,
		Metadata:    content.Metadata,
		PublishedAt: content.PublishedAt,
		TotalTips:   totalTips,
		TotalStake:  totalStake,
	}
}

func bigString(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}

func formatLedger(ledger *creator.PayoutLedger) (pending, totalTips, totalYield string, lastPayout int64) {
	if ledger == nil {
		return "0", "0", "0", 0
	}
	return bigString(ledger.PendingDistribution), bigString(ledger.TotalTips), bigString(ledger.TotalStakingYield), ledger.LastPayout
}

func formatAddress(addr [20]byte) string {
	return crypto.MustNewAddress(crypto.NHBPrefix, addr[:]).String()
}

func (s *Server) handleCreatorPublish(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params creatorPublishParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	trimmedID := strings.TrimSpace(params.ContentID)
	if trimmedID == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "contentId is required", nil)
		return
	}
	content, err := s.node.CreatorPublish(callerAddr, trimmedID, params.URI, params.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to publish content", err.Error())
		return
	}
	result := formatCreatorContent(params.Caller, content)
	writeResult(w, req.ID, result)
}

func (s *Server) handleCreatorTip(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params creatorTipParams
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
	tip, ledger, err := s.node.CreatorTip(callerAddr, params.ContentID, amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to apply tip", err.Error())
		return
	}
	creatorAddr := ""
	if tip != nil {
		creatorAddr = formatAddress(tip.Creator)
	}
	pending, totalTips, totalYield, _ := formatLedger(ledger)
	result := creatorTipResult{
		ContentID:  params.ContentID,
		Creator:    creatorAddr,
		Fan:        params.Caller,
		Amount:     amount.String(),
		TippedAt:   0,
		Pending:    pending,
		TotalTips:  totalTips,
		TotalYield: totalYield,
	}
	if tip != nil {
		result.TippedAt = tip.TippedAt
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleCreatorStake(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params creatorStakeParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	creatorAddr, err := decodeBech32(params.Creator)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid creator address", err.Error())
		return
	}
	amount, err := parseAmount(params.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	stake, reward, ledger, err := s.node.CreatorStake(callerAddr, creatorAddr, amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to stake", err.Error())
		return
	}
	pending, totalTips, totalYield, _ := formatLedger(ledger)
	result := creatorStakeResult{
		Creator:    params.Creator,
		Fan:        params.Caller,
		Amount:     amount.String(),
		Shares:     "0",
		StakedAt:   0,
		Reward:     bigString(reward),
		Pending:    pending,
		TotalTips:  totalTips,
		TotalYield: totalYield,
	}
	if stake != nil {
		result.Shares = bigString(stake.Shares)
		result.StakedAt = stake.StakedAt
		result.Amount = bigString(stake.Amount)
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleCreatorUnstake(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params creatorUnstakeParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	creatorAddr, err := decodeBech32(params.Creator)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid creator address", err.Error())
		return
	}
	amount, err := parseAmount(params.Amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	stake, err := s.node.CreatorUnstake(callerAddr, creatorAddr, amount)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to unstake", err.Error())
		return
	}
	result := creatorUnstakeResult{
		Creator:   params.Creator,
		Fan:       params.Caller,
		Amount:    amount.String(),
		Remaining: "0",
		Shares:    "0",
	}
	if stake != nil {
		result.Remaining = bigString(stake.Amount)
		result.Shares = bigString(stake.Shares)
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleCreatorPayouts(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params creatorPayoutsParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	claimed := big.NewInt(0)
	var ledger *creator.PayoutLedger
	if params.Claim {
		var amount *big.Int
		ledger, amount, err = s.node.CreatorClaimPayouts(callerAddr)
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to claim payouts", err.Error())
			return
		}
		claimed = amount
	} else {
		ledger, err = s.node.CreatorPayouts(callerAddr)
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to load payouts", err.Error())
			return
		}
	}
	pending, totalTips, totalYield, last := formatLedger(ledger)
	result := creatorPayoutsResult{
		Creator:    params.Caller,
		Pending:    pending,
		TotalTips:  totalTips,
		TotalYield: totalYield,
		LastPayout: last,
		Claimed:    bigString(claimed),
	}
	writeResult(w, req.ID, result)
}
