package rpc

import (
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/native/governance"
)

type govProposeParams struct {
	Kind    string `json:"kind"`
	Payload string `json:"payload"`
	From    string `json:"from"`
	Deposit string `json:"deposit,omitempty"`
}

type govVoteParams struct {
	ID     uint64 `json:"id"`
	From   string `json:"from"`
	Choice string `json:"choice"`
}

type govIDParams struct {
	ID uint64 `json:"id"`
}

type govListParams struct {
	Cursor *uint64 `json:"cursor,omitempty"`
	Limit  *int    `json:"limit,omitempty"`
}

type govProposeResponse struct {
	ProposalID uint64 `json:"proposalId"`
}

type govAckResponse struct {
	OK       bool                 `json:"ok"`
	Proposal *governance.Proposal `json:"proposal,omitempty"`
}

type govFinalizeResponse struct {
	Proposal *governance.Proposal `json:"proposal"`
	Tally    *governance.Tally    `json:"tally"`
}

type govListResponse struct {
	Proposals  []*governance.Proposal `json:"proposals"`
	NextCursor *uint64                `json:"nextCursor,omitempty"`
}

func parseNonNegativeAmount(value string) (*big.Int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return big.NewInt(0), nil
	}
	normalized := strings.TrimPrefix(trimmed, "+")
	if strings.HasPrefix(normalized, "-") {
		return nil, fmt.Errorf("amount must not be negative")
	}
	amount, ok := new(big.Int).SetString(normalized, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount")
	}
	if amount.Sign() < 0 {
		return nil, fmt.Errorf("amount must not be negative")
	}
	return amount, nil
}

func (s *Server) handleGovernancePropose(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params govProposeParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	kind := strings.TrimSpace(params.Kind)
	if kind == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "kind is required", nil)
		return
	}
	payload := strings.TrimSpace(params.Payload)
	if payload == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "payload is required", nil)
		return
	}
	if strings.TrimSpace(params.From) == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "from is required", nil)
		return
	}
	proposer, err := decodeBech32(params.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid from address", err.Error())
		return
	}
	deposit, err := parseNonNegativeAmount(params.Deposit)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	proposalID, err := s.node.GovernancePropose(proposer, kind, payload, deposit)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	writeResult(w, req.ID, govProposeResponse{ProposalID: proposalID})
}

func (s *Server) handleGovernanceVote(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params govVoteParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	if params.ID == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "id is required", nil)
		return
	}
	if strings.TrimSpace(params.From) == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "from is required", nil)
		return
	}
	voter, err := decodeBech32(params.From)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid from address", err.Error())
		return
	}
	choice := strings.TrimSpace(params.Choice)
	if choice == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "choice is required", nil)
		return
	}
	if err := s.node.GovernanceVote(params.ID, voter, choice); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	writeResult(w, req.ID, govAckResponse{OK: true})
}

func (s *Server) handleGovernanceProposal(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
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

func (s *Server) handleGovernanceList(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
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

func (s *Server) handleGovernanceFinalize(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
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
	proposal, tally, err := s.node.GovernanceFinalize(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	writeResult(w, req.ID, govFinalizeResponse{Proposal: proposal, Tally: tally})
}

func (s *Server) handleGovernanceQueue(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
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
	proposal, err := s.node.GovernanceQueue(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	writeResult(w, req.ID, govAckResponse{OK: true, Proposal: proposal})
}

func (s *Server) handleGovernanceExecute(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuth(r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
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
	proposal, err := s.node.GovernanceExecute(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		return
	}
	writeResult(w, req.ID, govAckResponse{OK: true, Proposal: proposal})
}
