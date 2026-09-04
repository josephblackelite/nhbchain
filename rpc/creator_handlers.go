package rpc

import (
	"encoding/json"
	"math/big"
	"net/http"

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

// creatorRPCDisabledMessage: same guaranteed-fork defect as escrow's RPC
// handlers (see escrowRPCDisabledMessage) -- creator_publish/tip/stake/
// unstake and creator_payouts' Claim:true path mutated n.state.Trie directly
// outside the block pipeline. Disabled as an emergency stopgap.
const creatorRPCDisabledMessage = "this method is disabled -- it mutated validator-local state outside the block pipeline, guaranteeing a consensus fork/halt on a 2-validator zero-quorum-slack chain; a signed-transaction replacement is pending"

func (s *Server) handleCreatorPublish(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, creatorRPCDisabledMessage, nil)
}

func (s *Server) handleCreatorTip(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, creatorRPCDisabledMessage, nil)
}

func (s *Server) handleCreatorStake(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, creatorRPCDisabledMessage, nil)
}

func (s *Server) handleCreatorUnstake(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, creatorRPCDisabledMessage, nil)
}

// handleCreatorPayouts keeps its READ path (Claim: false, just loading the
// payout ledger) live -- only Claim: true actually mutates state
// (CreatorClaimPayouts), so only that path is disabled.
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
	if params.Claim {
		writeError(w, http.StatusGone, req.ID, codeMethodDisabled, creatorRPCDisabledMessage, nil)
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	ledger, err := s.node.CreatorPayouts(callerAddr)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to load payouts", err.Error())
		return
	}
	pending, totalTips, totalYield, last := formatLedger(ledger)
	result := creatorPayoutsResult{
		Creator:    params.Caller,
		Pending:    pending,
		TotalTips:  totalTips,
		TotalYield: totalYield,
		LastPayout: last,
		Claimed:    bigString(big.NewInt(0)),
	}
	writeResult(w, req.ID, result)
}
