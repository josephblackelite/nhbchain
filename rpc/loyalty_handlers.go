package rpc

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"sort"
	"strconv"
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
	ID                 string  `json:"id"`
	Owner              string  `json:"owner"`
	Pool               string  `json:"pool"`
	TokenSymbol        string  `json:"tokenSymbol"`
	RewardMode         string  `json:"rewardMode,omitempty"`
	AccrualBps         uint32  `json:"accrualBps"`
	FixedRewardWei     *string `json:"fixedRewardWei,omitempty"`
	MinSpendWei        *string `json:"minSpendWei,omitempty"`
	CapPerTx           *string `json:"capPerTx,omitempty"`
	DailyCapUser       *string `json:"dailyCapUser,omitempty"`
	DailyCapProgram    *string `json:"dailyCapProgram,omitempty"`
	EpochCapProgram    *string `json:"epochCapProgram,omitempty"`
	EpochLengthSeconds *uint64 `json:"epochLengthSeconds,omitempty"`
	IssuanceCapUser    *string `json:"issuanceCapUser,omitempty"`
	StartTime          *uint64 `json:"startTime,omitempty"`
	EndTime            *uint64 `json:"endTime,omitempty"`
	Active             *bool   `json:"active,omitempty"`
}

type programResult struct {
	ID                 string `json:"id"`
	Owner              string `json:"owner"`
	Pool               string `json:"pool"`
	TokenSymbol        string `json:"tokenSymbol"`
	RewardMode         string `json:"rewardMode"`
	AccrualBps         uint32 `json:"accrualBps"`
	FixedRewardWei     string `json:"fixedRewardWei"`
	MinSpendWei        string `json:"minSpendWei"`
	CapPerTx           string `json:"capPerTx"`
	DailyCapUser       string `json:"dailyCapUser"`
	DailyCapProgram    string `json:"dailyCapProgram"`
	EpochCapProgram    string `json:"epochCapProgram"`
	EpochLengthSeconds uint64 `json:"epochLengthSeconds"`
	IssuanceCapUser    string `json:"issuanceCapUser"`
	StartTime          uint64 `json:"startTime"`
	EndTime            uint64 `json:"endTime"`
	Active             bool   `json:"active"`
}

// programStatsResult is the response shape for loyalty_programStats. CapUsage
// is a pointer so it can be omitted (JSON null) when the program has no
// configured DailyCapProgram -- there is no cap denominator to compute a
// ratio against, and reporting "0" in that case would be indistinguishable
// from a capped program that simply had zero usage today. RewardsPaid/TxCount
// are scoped to the requested `day`; LifetimeRewardsPaid is a separate,
// never-reset cumulative total across all days, independent of the `day`
// parameter.
type programStatsResult struct {
	RewardsPaid         string  `json:"rewardsPaid"`
	TxCount             string  `json:"txCount"`
	CapUsage            *string `json:"capUsage"`
	LifetimeRewardsPaid string  `json:"lifetimeRewardsPaid"`
}

// accrualRecordResult is the response shape for a single entry returned by
// loyalty_listAccruals -- the decoded, JSON-friendly form of
// loyalty.AccrualRecord. Address/programId/txHash are hex strings and amount
// is a decimal wei string, matching how every other loyalty RPC in this file
// already formats its output (see formatProgram/formatBusiness above).
type accrualRecordResult struct {
	ProgramID string `json:"programId"`
	Address   string `json:"address"`
	Amount    string `json:"amount"`
	Kind      string `json:"kind"`
	TxHash    string `json:"txHash"`
	Timestamp uint64 `json:"timestamp"`
}

type businessResult struct {
	ID        string   `json:"id"`
	Owner     string   `json:"owner"`
	Name      string   `json:"name"`
	Paymaster string   `json:"paymaster"`
	Merchants []string `json:"merchants"`
}

// loyaltyRPCDisabledMessage: loyalty_createBusiness/setPaymaster/addMerchant/
// removeMerchant/createProgram/updateProgram/pauseProgram/resumeProgram used
// to call s.node.LoyaltyRegistry() directly -- a bare
// nhbstate.NewManager(n.state.Trie) mutation of the live state trie OUTSIDE
// the transaction/block pipeline entirely (no tx created, signed, or added
// to any block). CreateBlock/ValidateBlock both derive their working state
// from that SAME n.state via n.state.Copy() before applying only the
// block's tx list and requiring the result to match the proposer's
// header.StateRoot -- so any validator-local RPC mutation here is invisible
// to every OTHER validator's independently-derived state, and the very next
// block either validator proposes is guaranteed to be rejected by the other
// on a state-root mismatch. With exactly 2 validators and zero quorum
// slack, that is not a risk of a fork -- it is a guaranteed, permanent halt
// at that height, on the very next legitimate authenticated call to ANY of
// these methods (same incident class already fixed this session for
// escrow/claimable/p2p/identity/creator, and previously for
// lending_supply/withdraw/borrow/... and stake_delegate/undelegate/claim/
// claimRewards). Disabled immediately as an emergency stopgap pending a
// real signed-transaction conversion that applies through
// StateProcessor.ApplyTransaction inside the normal consensus path instead.
// That conversion now exists: TxTypeCreateLoyaltyBusiness/
// LoyaltySetPaymaster/LoyaltyAddMerchant/LoyaltyRemoveMerchant/
// CreateLoyaltyProgram/UpdateLoyaltyProgram/PauseLoyaltyProgram/
// ResumeLoyaltyProgram (0x42-0x49, core/types/transaction.go) replace each
// of these 8 RPC methods respectively -- a caller signs and submits one of
// those transaction types with their own key instead of calling this RPC
// method, exactly as escrow/lending/swap already work. These RPC methods
// stay disabled rather than re-enabled as thin transaction-builders,
// because that would silently reintroduce a trusted-RPC-signs-for-you model
// when the actual security model requires the caller to hold and use their
// own private key. loyalty_getBusiness/listBusinesses/listPrograms/
// programStats/listAccruals/userDaily/paymasterBalance/resolveUsername/
// userQR (all read-only) are deliberately left live.
const loyaltyRPCDisabledMessage = "this method is disabled -- it mutated validator-local state outside the block pipeline, guaranteeing a consensus fork/halt on a 2-validator zero-quorum-slack chain; submit the equivalent TxTypeCreateLoyaltyBusiness/LoyaltySetPaymaster/LoyaltyAddMerchant/LoyaltyRemoveMerchant/CreateLoyaltyProgram/UpdateLoyaltyProgram/PauseLoyaltyProgram/ResumeLoyaltyProgram transaction instead, signed by the caller's own key"

func (s *Server) handleLoyaltyCreateBusiness(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, loyaltyRPCDisabledMessage, nil)
}

func (s *Server) handleLoyaltySetPaymaster(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, loyaltyRPCDisabledMessage, nil)
}

func (s *Server) handleLoyaltyAddMerchant(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, loyaltyRPCDisabledMessage, nil)
}

func (s *Server) handleLoyaltyRemoveMerchant(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, loyaltyRPCDisabledMessage, nil)
}

func (s *Server) handleLoyaltyCreateProgram(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, loyaltyRPCDisabledMessage, nil)
}

func (s *Server) handleLoyaltyUpdateProgram(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, loyaltyRPCDisabledMessage, nil)
}

func (s *Server) handleLoyaltyPauseProgram(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, loyaltyRPCDisabledMessage, nil)
}

func (s *Server) handleLoyaltyResumeProgram(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	writeError(w, http.StatusGone, req.ID, codeMethodDisabled, loyaltyRPCDisabledMessage, nil)
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

// handleLoyaltyListBusinesses lists every business the given address owns.
// This is the read path a client uses to discover the BusinessID assigned
// by a TxTypeCreateLoyaltyBusiness transaction -- RegisterBusiness itself
// emits no synchronous return value the way this RPC's old create handler
// once did, and BusinessIDs are sequentially minted so a caller cannot
// predict theirs in advance.
func (s *Server) handleLoyaltyListBusinesses(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params struct {
		Owner string `json:"owner"`
	}
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}
	ownerAddr, err := decodeBech32(params.Owner)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid owner address", err.Error())
		return
	}
	ids, err := s.node.LoyaltyBusinessesByOwner(ownerAddr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to list businesses", err.Error())
		return
	}
	businesses := make([]businessResult, 0, len(ids))
	for _, id := range ids {
		business, ok, err := s.node.LoyaltyBusinessByID(id)
		if err != nil {
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load business", err.Error())
			return
		}
		if !ok {
			continue
		}
		businesses = append(businesses, formatBusiness(business))
	}
	sort.Slice(businesses, func(i, j int) bool { return businesses[i].ID < businesses[j].ID })
	writeResult(w, req.ID, businesses)
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
	programID, err := parseProgramID(params.ProgramID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid programId", err.Error())
		return
	}
	program, ok, err := s.node.LoyaltyProgramByID(programID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load program", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "program not found", params.ProgramID)
		return
	}

	// rewardsPaid/txCount are real, always-on meters (native/loyalty's
	// ApplyProgramReward writes both unconditionally on every successful
	// accrual, regardless of whether the program has any cap configured --
	// see engine_program.go). Days before this instrumented write path was
	// deployed have no recorded meter and read back as zero, indistinguishable
	// from a genuine zero-activity day; there is no way to retroactively
	// backfill that from state alone.
	manager := s.node.LoyaltyManager()
	rewardsPaid, err := manager.LoyaltyProgramDailyTotalAccrued(programID, params.Day)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load meters", err.Error())
		return
	}
	txCount, err := manager.LoyaltyProgramDailyTxCount(programID, params.Day)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load meters", err.Error())
		return
	}
	// lifetimeRewardsPaid is a separate, never-reset cumulative meter -- it is
	// not derived from rewardsPaid/params.Day and is unaffected by which `day`
	// was requested. See LoyaltyProgramLifetimeAccrued's doc comment.
	lifetimeRewardsPaid, err := manager.LoyaltyProgramLifetimeAccrued(programID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load meters", err.Error())
		return
	}

	result := programStatsResult{
		RewardsPaid:         bigIntToString(rewardsPaid),
		TxCount:             strconv.FormatUint(txCount, 10),
		LifetimeRewardsPaid: bigIntToString(lifetimeRewardsPaid),
	}
	// capUsage is only a meaningful ratio when the program has a configured
	// DailyCapProgram -- without one there is no denominator to divide by, so
	// leave it nil (JSON null) rather than reporting a fabricated "0" that
	// would look identical to a capped program with genuinely zero usage.
	// Note this deliberately reflects DailyCapProgram only, not
	// EpochCapProgram: epoch windows are not guaranteed to align with UTC day
	// boundaries, so an epoch-based ratio would not correspond to the `day`
	// parameter this method is scoped to.
	if program.DailyCapProgram != nil && program.DailyCapProgram.Sign() > 0 {
		usage := new(big.Rat).SetFrac(rewardsPaid, program.DailyCapProgram)
		usageStr := usage.FloatString(4)
		result.CapUsage = &usageStr
	}
	writeResult(w, req.ID, result)
}

// handleLoyaltyListAccruals is a READ-ONLY handler: it only ever calls
// LoyaltyProgramDailyAccrualRecords (a KVGetList-backed read accessor), never
// any Set*/Append* method -- see the direct-state-write RPC bug class this
// codebase has fixed elsewhere (CreatePool, governance, POTSO stake,
// pause-clear) for why that distinction matters. It returns every individual
// accrual line item (both base and program-kind, though in practice only
// program-kind records are ever appended under a real programId -- base
// rewards have no program context, see
// BaseRewardState.AppendLoyaltyBaseAccrualRecord's doc comment) recorded for
// the given program on the given UTC day, powering the "Rewards Accrual
// History" business dashboard feature.
func (s *Server) handleLoyaltyListAccruals(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
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
	programID, err := parseProgramID(params.ProgramID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid programId", err.Error())
		return
	}
	_, ok, err := s.node.LoyaltyProgramByID(programID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load program", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "program not found", params.ProgramID)
		return
	}

	manager := s.node.LoyaltyManager()
	records, err := manager.LoyaltyProgramDailyAccrualRecords(programID, params.Day)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load accrual records", err.Error())
		return
	}
	results := make([]accrualRecordResult, 0, len(records))
	for _, record := range records {
		results = append(results, formatAccrualRecord(record))
	}
	writeResult(w, req.ID, results)
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

func formatRewardMode(mode loyalty.RewardMode) string {
	if mode == loyalty.RewardModeFixed {
		return "fixed"
	}
	return "bps"
}

func formatProgram(program *loyalty.Program) programResult {
	return programResult{
		ID:                 formatProgramID(program.ID),
		Owner:              crypto.MustNewAddress(crypto.NHBPrefix, program.Owner[:]).String(),
		Pool:               crypto.MustNewAddress(crypto.NHBPrefix, program.Pool[:]).String(),
		TokenSymbol:        program.TokenSymbol,
		RewardMode:         formatRewardMode(program.RewardMode),
		AccrualBps:         program.AccrualBps,
		FixedRewardWei:     bigIntToString(program.FixedRewardWei),
		MinSpendWei:        bigIntToString(program.MinSpendWei),
		CapPerTx:           bigIntToString(program.CapPerTx),
		DailyCapUser:       bigIntToString(program.DailyCapUser),
		DailyCapProgram:    bigIntToString(program.DailyCapProgram),
		EpochCapProgram:    bigIntToString(program.EpochCapProgram),
		EpochLengthSeconds: program.EpochLengthSeconds,
		IssuanceCapUser:    bigIntToString(program.IssuanceCapUser),
		StartTime:          program.StartTime,
		EndTime:            program.EndTime,
		Active:             program.Active,
	}
}

func formatAccrualRecord(record loyalty.AccrualRecord) accrualRecordResult {
	return accrualRecordResult{
		ProgramID: formatProgramID(record.ProgramID),
		Address:   crypto.MustNewAddress(crypto.NHBPrefix, record.Address[:]).String(),
		Amount:    bigIntToString(record.Amount),
		Kind:      record.Kind,
		TxHash:    "0x" + hex.EncodeToString(record.TxHash[:]),
		Timestamp: record.Timestamp,
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
	rewardMode, err := parseRewardMode(spec.RewardMode)
	if err != nil {
		return nil, err
	}
	fixedReward, err := parseBigInt(spec.FixedRewardWei)
	if err != nil {
		return nil, fmt.Errorf("invalid fixedRewardWei: %w", err)
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
	dailyCapProgram, err := parseBigInt(spec.DailyCapProgram)
	if err != nil {
		return nil, fmt.Errorf("invalid dailyCapProgram: %w", err)
	}
	epochCapProgram, err := parseBigInt(spec.EpochCapProgram)
	if err != nil {
		return nil, fmt.Errorf("invalid epochCapProgram: %w", err)
	}
	issuanceCap, err := parseBigInt(spec.IssuanceCapUser)
	if err != nil {
		return nil, fmt.Errorf("invalid issuanceCapUser: %w", err)
	}
	epochLengthSeconds := uint64(0)
	if spec.EpochLengthSeconds != nil {
		epochLengthSeconds = *spec.EpochLengthSeconds
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
		ID:                 id,
		Owner:              owner,
		Pool:               pool,
		TokenSymbol:        token,
		RewardMode:         rewardMode,
		AccrualBps:         spec.AccrualBps,
		FixedRewardWei:     fixedReward,
		MinSpendWei:        minSpend,
		CapPerTx:           capPerTx,
		DailyCapUser:       dailyCap,
		DailyCapProgram:    dailyCapProgram,
		EpochCapProgram:    epochCapProgram,
		EpochLengthSeconds: epochLengthSeconds,
		IssuanceCapUser:    issuanceCap,
		StartTime:          startTime,
		EndTime:            endTime,
		Active:             active,
	}, nil
}

// parseRewardMode accepts "bps" or "" (both -> RewardModeBps, preserving
// backward compatibility with any caller that predates this field) and
// "fixed" (-> RewardModeFixed). Anything else is rejected outright rather
// than silently defaulting, since a typo here would otherwise silently
// configure the wrong reward mechanism.
func parseRewardMode(input string) (loyalty.RewardMode, error) {
	switch strings.ToLower(strings.TrimSpace(input)) {
	case "", "bps":
		return loyalty.RewardModeBps, nil
	case "fixed":
		return loyalty.RewardModeFixed, nil
	default:
		return 0, fmt.Errorf("invalid rewardMode %q: must be \"bps\" or \"fixed\"", input)
	}
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
