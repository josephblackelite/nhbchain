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
