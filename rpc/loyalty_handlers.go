package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strings"

	"nhbchain/crypto"
	"nhbchain/native/loyalty"
)

type createBusinessParams struct {
	Caller string `json:"caller"`
	Owner  string `json:"owner"`
	Name   string `json:"name"`
}

type setPaymasterParams struct {
	Caller     string `json:"caller"`
	BusinessID string `json:"businessId"`
	Paymaster  string `json:"paymaster"`
}

type merchantParams struct {
	Caller     string `json:"caller"`
	BusinessID string `json:"businessId"`
	Merchant   string `json:"merchant"`
}

type programSpecEnvelope struct {
	Caller     string          `json:"caller"`
	BusinessID string          `json:"businessId,omitempty"`
	Spec       json.RawMessage `json:"spec"`
}

type programLifecycleParams struct {
	Caller    string `json:"caller"`
	ProgramID string `json:"programId"`
}

type businessQueryParams struct {
	BusinessID string `json:"businessId"`
}

type programStatsParams struct {
	ProgramID string `json:"programId"`
	Day       string `json:"day"`
}

type userDailyParams struct {
	User      string `json:"user"`
	ProgramID string `json:"programId"`
	Day       string `json:"day"`
}

type usernameParams struct {
	Username string `json:"username"`
}

type userQRParams struct {
	Username string `json:"username,omitempty"`
	Address  string `json:"address,omitempty"`
}

type programSpec struct {
	ID           string  `json:"id"`
	Owner        string  `json:"owner"`
	Pool         string  `json:"pool"`
	TokenSymbol  string  `json:"tokenSymbol"`
	AccrualBps   uint32  `json:"accrualBps"`
	MinSpendWei  *string `json:"minSpendWei,omitempty"`
	CapPerTx     *string `json:"capPerTx,omitempty"`
	DailyCapUser *string `json:"dailyCapUser,omitempty"`
	StartTime    *uint64 `json:"startTime,omitempty"`
	EndTime      *uint64 `json:"endTime,omitempty"`
	Active       *bool   `json:"active,omitempty"`
}

type programResult struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	Pool         string `json:"pool"`
	TokenSymbol  string `json:"tokenSymbol"`
	AccrualBps   uint32 `json:"accrualBps"`
	MinSpendWei  string `json:"minSpendWei"`
	CapPerTx     string `json:"capPerTx"`
	DailyCapUser string `json:"dailyCapUser"`
	StartTime    uint64 `json:"startTime"`
	EndTime      uint64 `json:"endTime"`
	Active       bool   `json:"active"`
}

type businessResult struct {
	ID        string   `json:"id"`
	Owner     string   `json:"owner"`
	Name      string   `json:"name"`
	Paymaster string   `json:"paymaster"`
	Merchants []string `json:"merchants"`
}

func (s *Server) handleLoyaltyCreateBusiness(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params createBusinessParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	ownerStr := params.Owner
	if strings.TrimSpace(ownerStr) == "" {
		ownerStr = params.Caller
	}
	ownerAddr, err := decodeBech32(ownerStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid owner address", err.Error())
		return
	}
	if !addressesEqual(callerAddr, ownerAddr) && !s.node.HasRole("ROLE_LOYALTY_ADMIN", callerAddr[:]) {
		writeError(w, http.StatusForbidden, req.ID, codeUnauthorized, "caller not authorized", nil)
		return
	}
	trimmedName := strings.TrimSpace(params.Name)
	if trimmedName == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "name is required", nil)
		return
	}
	registry := s.node.LoyaltyRegistry()
	businessID, err := registry.RegisterBusiness(ownerAddr, trimmedName)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to create business", err.Error())
		return
	}
	writeResult(w, req.ID, formatBusinessID(businessID))
}

func (s *Server) handleLoyaltySetPaymaster(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params setPaymasterParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	businessID, err := parseBusinessID(params.BusinessID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid businessId", err.Error())
		return
	}
	business, ok, err := s.node.LoyaltyBusinessByID(businessID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load business", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "business not found", params.BusinessID)
		return
	}
	if !addressesEqual(callerAddr, business.Owner) && !s.node.HasRole("ROLE_LOYALTY_ADMIN", callerAddr[:]) {
		writeError(w, http.StatusForbidden, req.ID, codeUnauthorized, "caller not authorized", nil)
		return
	}
	paymasterAddr, err := decodeOptionalBech32(params.Paymaster)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid paymaster", err.Error())
		return
	}
	registry := s.node.LoyaltyRegistry()
	if err := registry.SetPaymaster(businessID, callerAddr, paymasterAddr); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to set paymaster", err.Error())
		return
	}
	writeResult(w, req.ID, "ok")
}

func (s *Server) handleLoyaltyAddMerchant(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params merchantParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	businessID, err := parseBusinessID(params.BusinessID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid businessId", err.Error())
		return
	}
	merchantAddr, err := decodeBech32(params.Merchant)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid merchant address", err.Error())
		return
	}
	business, ok, err := s.node.LoyaltyBusinessByID(businessID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load business", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "business not found", params.BusinessID)
		return
	}
	if !addressesEqual(callerAddr, business.Owner) && !s.node.HasRole("ROLE_LOYALTY_ADMIN", callerAddr[:]) {
		writeError(w, http.StatusForbidden, req.ID, codeUnauthorized, "caller not authorized", nil)
		return
	}
	registry := s.node.LoyaltyRegistry()
	if err := registry.AddMerchantAddress(businessID, merchantAddr); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to add merchant", err.Error())
		return
	}
	writeResult(w, req.ID, "ok")
}

func (s *Server) handleLoyaltyRemoveMerchant(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params merchantParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	businessID, err := parseBusinessID(params.BusinessID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid businessId", err.Error())
		return
	}
	merchantAddr, err := decodeBech32(params.Merchant)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid merchant address", err.Error())
		return
	}
	business, ok, err := s.node.LoyaltyBusinessByID(businessID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load business", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "business not found", params.BusinessID)
		return
	}
	if !addressesEqual(callerAddr, business.Owner) && !s.node.HasRole("ROLE_LOYALTY_ADMIN", callerAddr[:]) {
		writeError(w, http.StatusForbidden, req.ID, codeUnauthorized, "caller not authorized", nil)
		return
	}
	registry := s.node.LoyaltyRegistry()
	if err := registry.RemoveMerchantAddress(businessID, merchantAddr); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to remove merchant", err.Error())
		return
	}
	writeResult(w, req.ID, "ok")
}

func (s *Server) handleLoyaltyCreateProgram(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var envelope programSpecEnvelope
	if err := json.Unmarshal(req.Params[0], &envelope); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(envelope.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	businessID, err := parseBusinessID(envelope.BusinessID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid businessId", err.Error())
		return
	}
	var spec programSpec
	if err := json.Unmarshal(envelope.Spec, &spec); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid program spec", err.Error())
		return
	}
	program, err := buildProgramFromSpec(&spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid program spec", err.Error())
		return
	}
	if !addressesEqual(callerAddr, program.Owner) && !s.node.HasRole("ROLE_LOYALTY_ADMIN", callerAddr[:]) {
		writeError(w, http.StatusForbidden, req.ID, codeUnauthorized, "caller not authorized", nil)
		return
	}
	business, ok, err := s.node.LoyaltyBusinessByID(businessID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load business", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "business not found", envelope.BusinessID)
		return
	}
	if !isMerchantOf(business, program.Owner) {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "owner is not a registered merchant", nil)
		return
	}
	registry := s.node.LoyaltyRegistry()
	if err := registry.CreateProgram(callerAddr, program); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to create program", err.Error())
		return
	}
	writeResult(w, req.ID, formatProgramID(program.ID))
}

func (s *Server) handleLoyaltyUpdateProgram(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var envelope programSpecEnvelope
	if err := json.Unmarshal(req.Params[0], &envelope); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(envelope.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	var spec programSpec
	if err := json.Unmarshal(envelope.Spec, &spec); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid program spec", err.Error())
		return
	}
	program, err := buildProgramFromSpec(&spec)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid program spec", err.Error())
		return
	}
	registry := s.node.LoyaltyRegistry()
	if err := registry.UpdateProgram(callerAddr, program); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to update program", err.Error())
		return
	}
	writeResult(w, req.ID, "ok")
}

func (s *Server) handleLoyaltyPauseProgram(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params programLifecycleParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	programID, err := parseProgramID(params.ProgramID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid programId", err.Error())
		return
	}
	registry := s.node.LoyaltyRegistry()
	if err := registry.PauseProgram(callerAddr, programID); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to pause program", err.Error())
		return
	}
	writeResult(w, req.ID, "ok")
}

func (s *Server) handleLoyaltyResumeProgram(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if authErr := s.requireAuthInto(&r); authErr != nil {
		writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params programLifecycleParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	callerAddr, err := decodeBech32(params.Caller)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid caller address", err.Error())
		return
	}
	programID, err := parseProgramID(params.ProgramID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid programId", err.Error())
		return
	}
	registry := s.node.LoyaltyRegistry()
	if err := registry.ResumeProgram(callerAddr, programID); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to resume program", err.Error())
		return
	}
	writeResult(w, req.ID, "ok")
}

func (s *Server) handleLoyaltyGetBusiness(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params businessQueryParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	businessID, err := parseBusinessID(params.BusinessID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid businessId", err.Error())
		return
	}
	business, ok, err := s.node.LoyaltyBusinessByID(businessID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load business", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "business not found", params.BusinessID)
		return
	}
	writeResult(w, req.ID, formatBusiness(business))
}

func (s *Server) handleLoyaltyListPrograms(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params businessQueryParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	businessID, err := parseBusinessID(params.BusinessID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid businessId", err.Error())
		return
	}
	business, ok, err := s.node.LoyaltyBusinessByID(businessID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load business", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "business not found", params.BusinessID)
		return
	}
	programs := make([]programResult, 0)
	seen := make(map[[32]byte]struct{})
	for _, merchant := range business.Merchants {
		ids, err := s.node.LoyaltyProgramsByOwner(merchant)
		if err != nil {
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to list programs", err.Error())
			return
		}
		for _, id := range ids {
			if _, exists := seen[id]; exists {
				continue
			}
			program, ok, err := s.node.LoyaltyProgramByID(id)
			if err != nil {
				writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load program", err.Error())
				return
			}
			if !ok {
				continue
			}
			programs = append(programs, formatProgram(program))
			seen[id] = struct{}{}
		}
	}
	sort.Slice(programs, func(i, j int) bool { return programs[i].ID < programs[j].ID })
	writeResult(w, req.ID, programs)
}

func (s *Server) handleLoyaltyProgramStats(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params programStatsParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	if strings.TrimSpace(params.Day) == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "day is required", nil)
		return
	}
	if _, err := parseProgramID(params.ProgramID); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid programId", err.Error())
		return
	}
	writeResult(w, req.ID, map[string]string{
		"rewardsPaid": "0",
		"txCount":     "0",
		"capUsage":    "0",
	})
}

func (s *Server) handleLoyaltyUserDaily(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params userDailyParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	addr, err := decodeBech32(params.User)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid user address", err.Error())
		return
	}
	if strings.TrimSpace(params.Day) == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "day is required", nil)
		return
	}
	programID, err := parseProgramID(params.ProgramID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid programId", err.Error())
		return
	}
	manager := s.node.LoyaltyManager()
	accrued, err := manager.LoyaltyProgramDailyAccrued(programID, addr[:], params.Day)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load meters", err.Error())
		return
	}
	writeResult(w, req.ID, accrued.String())
}

func (s *Server) handleLoyaltyPaymasterBalance(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params businessQueryParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	businessID, err := parseBusinessID(params.BusinessID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid businessId", err.Error())
		return
	}
	business, ok, err := s.node.LoyaltyBusinessByID(businessID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load business", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "business not found", params.BusinessID)
		return
	}
	if isZeroAddress(business.Paymaster) {
		writeResult(w, req.ID, "0")
		return
	}
	account, err := s.node.GetAccount(business.Paymaster[:])
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load account", err.Error())
		return
	}
	writeResult(w, req.ID, account.BalanceZNHB.String())
}

func (s *Server) handleLoyaltyResolveUsername(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params usernameParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	if strings.TrimSpace(params.Username) == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "username is required", nil)
		return
	}
	addr, ok := s.node.ResolveUsername(params.Username)
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "username not found", params.Username)
		return
	}
	writeResult(w, req.ID, crypto.MustNewAddress(crypto.NHBPrefix, addr).String())
}

func (s *Server) handleLoyaltyUserQR(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params userQRParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	var address string
	if strings.TrimSpace(params.Address) != "" {
		if _, err := decodeBech32(params.Address); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
			return
		}
		address = params.Address
	} else if strings.TrimSpace(params.Username) != "" {
		addr, ok := s.node.ResolveUsername(params.Username)
		if !ok {
			writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "username not found", params.Username)
			return
		}
		address = crypto.MustNewAddress(crypto.NHBPrefix, addr).String()
	} else {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "username or address required", nil)
		return
	}
	payload := fmt.Sprintf("nhb:%s", address)
	writeResult(w, req.ID, map[string]string{
		"address": address,
		"payload": payload,
	})
}

func decodeBech32(addr string) ([20]byte, error) {
	var zero [20]byte
	decoded, err := crypto.DecodeAddress(strings.TrimSpace(addr))
	if err != nil {
		return zero, err
	}
	copy(zero[:], decoded.Bytes())
	return zero, nil
}

func decodeOptionalBech32(addr string) ([20]byte, error) {
	if strings.TrimSpace(addr) == "" {
		return [20]byte{}, nil
	}
	return decodeBech32(addr)
}

func addressesEqual(a, b [20]byte) bool {
	return a == b
}

func isZeroAddress(addr [20]byte) bool {
	return addr == ([20]byte{})
}

func parseBusinessID(id string) (loyalty.BusinessID, error) {
	var out loyalty.BusinessID
	cleaned := strings.TrimPrefix(strings.TrimSpace(id), "0x")
	bytes, err := hex.DecodeString(cleaned)
	if err != nil {
		return out, err
	}
	if len(bytes) != len(out) {
		return out, fmt.Errorf("businessId must be %d bytes", len(out))
	}
	copy(out[:], bytes)
	return out, nil
}

func parseProgramID(id string) (loyalty.ProgramID, error) {
	var out loyalty.ProgramID
	cleaned := strings.TrimPrefix(strings.TrimSpace(id), "0x")
	bytes, err := hex.DecodeString(cleaned)
	if err != nil {
		return out, err
	}
	if len(bytes) != len(out) {
		return out, fmt.Errorf("programId must be %d bytes", len(out))
	}
	copy(out[:], bytes)
	return out, nil
}

func formatBusinessID(id loyalty.BusinessID) string {
	return "0x" + hex.EncodeToString(id[:])
}

func formatProgramID(id loyalty.ProgramID) string {
	return "0x" + hex.EncodeToString(id[:])
}

func formatBusiness(business *loyalty.Business) businessResult {
	merchants := make([]string, 0, len(business.Merchants))
	for _, merchant := range business.Merchants {
		merchants = append(merchants, crypto.MustNewAddress(crypto.NHBPrefix, merchant[:]).String())
	}
	sort.Strings(merchants)
	paymaster := ""
	if !isZeroAddress(business.Paymaster) {
		paymaster = crypto.MustNewAddress(crypto.NHBPrefix, business.Paymaster[:]).String()
	}
	return businessResult{
		ID:        formatBusinessID(business.ID),
		Owner:     crypto.MustNewAddress(crypto.NHBPrefix, business.Owner[:]).String(),
		Name:      business.Name,
		Paymaster: paymaster,
		Merchants: merchants,
	}
}

func formatProgram(program *loyalty.Program) programResult {
	return programResult{
		ID:           formatProgramID(program.ID),
		Owner:        crypto.MustNewAddress(crypto.NHBPrefix, program.Owner[:]).String(),
		Pool:         crypto.MustNewAddress(crypto.NHBPrefix, program.Pool[:]).String(),
		TokenSymbol:  program.TokenSymbol,
		AccrualBps:   program.AccrualBps,
		MinSpendWei:  bigIntToString(program.MinSpendWei),
		CapPerTx:     bigIntToString(program.CapPerTx),
		DailyCapUser: bigIntToString(program.DailyCapUser),
		StartTime:    program.StartTime,
		EndTime:      program.EndTime,
		Active:       program.Active,
	}
}

func bigIntToString(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}

func buildProgramFromSpec(spec *programSpec) (*loyalty.Program, error) {
	if spec == nil {
		return nil, fmt.Errorf("spec required")
	}
	id, err := parseProgramID(spec.ID)
	if err != nil {
		return nil, err
	}
	owner, err := decodeBech32(spec.Owner)
	if err != nil {
		return nil, err
	}
	pool, err := decodeBech32(spec.Pool)
	if err != nil {
		return nil, err
	}
	token := strings.ToUpper(strings.TrimSpace(spec.TokenSymbol))
	if token == "" {
		return nil, fmt.Errorf("tokenSymbol required")
	}
	minSpend, err := parseBigInt(spec.MinSpendWei)
	if err != nil {
		return nil, fmt.Errorf("invalid minSpendWei: %w", err)
	}
	capPerTx, err := parseBigInt(spec.CapPerTx)
	if err != nil {
		return nil, fmt.Errorf("invalid capPerTx: %w", err)
	}
	dailyCap, err := parseBigInt(spec.DailyCapUser)
	if err != nil {
		return nil, fmt.Errorf("invalid dailyCapUser: %w", err)
	}
	active := true
	if spec.Active != nil {
		active = *spec.Active
	}
	startTime := uint64(0)
	if spec.StartTime != nil {
		startTime = *spec.StartTime
	}
	endTime := uint64(0)
	if spec.EndTime != nil {
		endTime = *spec.EndTime
	}
	return &loyalty.Program{
		ID:           id,
		Owner:        owner,
		Pool:         pool,
		TokenSymbol:  token,
		AccrualBps:   spec.AccrualBps,
		MinSpendWei:  minSpend,
		CapPerTx:     capPerTx,
		DailyCapUser: dailyCap,
		StartTime:    startTime,
		EndTime:      endTime,
		Active:       active,
	}, nil
}

func parseBigInt(input *string) (*big.Int, error) {
	if input == nil || strings.TrimSpace(*input) == "" {
		return big.NewInt(0), nil
	}
	value, ok := new(big.Int).SetString(strings.TrimSpace(*input), 10)
	if !ok {
		return nil, fmt.Errorf("invalid integer")
	}
	if value.Sign() < 0 {
		return nil, fmt.Errorf("value must be non-negative")
	}
	return value, nil
}

func isMerchantOf(business *loyalty.Business, owner [20]byte) bool {
	if business == nil {
		return false
	}
	for _, merchant := range business.Merchants {
		if merchant == owner {
			return true
		}
	}
	return false
}
