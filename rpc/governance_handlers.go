package rpc

import (
	"encoding/json"
	"net/http"

	"nhbchain/native/governance"
)

// Governance write actions (propose/vote/finalize/queue/execute) no longer
// have bespoke RPC methods here -- they were removed along with
// Node.GovernancePropose/Vote/Finalize/Queue/Execute (core/node.go) for
// bypassing consensus/gossip entirely and trusting a client-supplied `from`
// field with no cryptographic proof of caller identity. They're real signed
// transactions now (TxTypeGovPropose/Vote/Finalize/Queue/Execute,
// core/governance_tx.go), submitted via nhb_sendTransaction like every
// other signed native transaction type. Only the two read-only queries
// remain here.

type govIDParams struct {
	ID uint64 `json:"id"`
}

type govListParams struct {
	Cursor *uint64 `json:"cursor,omitempty"`
	Limit  *int    `json:"limit,omitempty"`
}

type govListResponse struct {
	Proposals  []*governance.Proposal `json:"proposals"`
	NextCursor *uint64                `json:"nextCursor,omitempty"`
}

type govEngagementResult struct {
	Available       bool   `json:"available"`
	Epoch           uint64 `json:"epoch"`
	TotalEngagement uint64 `json:"totalEngagement"`
	TotalStakeWei   string `json:"totalStakeWei"`
	EligibleVoters  uint64 `json:"eligibleVoters"`
}

type govParamsResult struct {
	VotingPeriodSeconds uint64              `json:"votingPeriodSeconds"`
	TimelockSeconds     uint64              `json:"timelockSeconds"`
	MinDepositWei       string              `json:"minDepositWei"`
	QuorumBps           uint64              `json:"quorumBps"`
	PassThresholdBps    uint64              `json:"passThresholdBps"`
	AllowedParams       []string            `json:"allowedParams"`
	AllowedRoles        []string            `json:"allowedRoles"`
	Engagement          govEngagementResult `json:"engagement"`
}

// handleGovernanceParams answers gov_params: the voting rules this node applies
// to a new governance proposal (voting period, timelock, minimum deposit,
// quorum, pass threshold, and the parameter and role allow-lists) and the
// engagement total of the weight snapshot that voting power is drawn from.
//
// Anonymous and read-only, like gov_proposal and gov_list. The rules are the
// node's own [governance] configuration, not chain state, so a different
// validator can report different ones; the response is what THIS node would
// enforce on a proposal submitted to it. engagement.available is false until a
// POTSO reward epoch has been processed.
func (s *Server) handleGovernanceParams(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	params, err := s.node.GovernanceParams()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load governance parameters", err.Error())
		return
	}
	result := govParamsResult{
		VotingPeriodSeconds: params.VotingPeriodSeconds,
		TimelockSeconds:     params.TimelockSeconds,
		MinDepositWei:       weiOrZero(params.MinDepositWei),
		QuorumBps:           params.QuorumBps,
		PassThresholdBps:    params.PassThresholdBps,
		AllowedParams:       params.AllowedParams,
		AllowedRoles:        params.AllowedRoles,
		Engagement: govEngagementResult{
			Available:       params.Engagement.Available,
			Epoch:           params.Engagement.Epoch,
			TotalEngagement: params.Engagement.TotalEngagement,
			TotalStakeWei:   weiOrZero(params.Engagement.TotalStake),
			EligibleVoters:  params.Engagement.EligibleVoters,
		},
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleGovernanceProposal(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params govIDParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	if params.ID == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "id is required", nil)
		return
	}
	proposal, ok, err := s.node.GovernanceProposal(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "proposal not found", nil)
		return
	}
	writeResult(w, req.ID, proposal)
}

func (s *Server) handleGovernanceList(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	var params govListParams
	if len(req.Params) > 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "at most one parameter object expected", nil)
		return
	}
	if len(req.Params) == 1 {
		if err := json.Unmarshal(req.Params[0], &params); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
			return
		}
	}
	var cursor uint64
	if params.Cursor != nil {
		cursor = *params.Cursor
	}
	limit := 0
	if params.Limit != nil {
		limit = *params.Limit
	}
	proposals, nextCursor, err := s.node.GovernanceListProposals(cursor, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	resp := govListResponse{Proposals: proposals}
	if nextCursor > 0 {
		resp.NextCursor = &nextCursor
	}
	writeResult(w, req.ID, resp)
}
