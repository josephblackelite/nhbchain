package rpc

import (
	"bytes"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"nhbchain/core"
	"nhbchain/core/epoch"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/rpc/modules"
)

const (
	jsonRPCVersion  = "2.0"
	maxRequestBytes = 1 << 20 // 1 MiB
	rateLimitWindow = time.Minute
	maxTxPerWindow  = 5
	txSeenTTL       = 15 * time.Minute
)

const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeUnauthorized   = -32001
	codeServerError    = -32000
	codeDuplicateTx    = -32010
	codeRateLimited    = -32020
)

type rateLimiter struct {
	count       int
	windowStart time.Time
}

type Server struct {
	node *core.Node

	mu            sync.Mutex
	txSeen        map[string]time.Time
	rateLimiters  map[string]*rateLimiter
	authToken     string
	potsoEvidence *modules.PotsoEvidenceModule
	transactions  *modules.TransactionsModule
	escrow        *modules.EscrowModule
}

func NewServer(node *core.Node) *Server {
	token := strings.TrimSpace(os.Getenv("NHB_RPC_TOKEN"))
	return &Server{
		node:          node,
		txSeen:        make(map[string]time.Time),
		rateLimiters:  make(map[string]*rateLimiter),
		authToken:     token,
		potsoEvidence: modules.NewPotsoEvidenceModule(node),
		transactions:  modules.NewTransactionsModule(node),
		escrow:        modules.NewEscrowModule(node),
	}
}

func (s *Server) Start(addr string) error {
	fmt.Printf("Starting JSON-RPC server on %s\n", addr)
	http.HandleFunc("/", s.handle)
	return http.ListenAndServe(addr, nil)
}

type RPCRequest struct {
	JSONRPC string            `json:"jsonrpc"`
	Method  string            `json:"method"`
	Params  []json.RawMessage `json:"params"`
	ID      int               `json:"id"`
}

type RPCResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

type RPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

func writeError(w http.ResponseWriter, status int, id interface{}, code int, message string, data interface{}) {
	if status <= 0 {
		status = http.StatusBadRequest
	}
	if status != http.StatusOK {
		w.WriteHeader(status)
	}
	errObj := &RPCError{Code: code, Message: message}
	if data != nil {
		errObj.Data = data
	}
	resp := RPCResponse{JSONRPC: jsonRPCVersion, ID: id, Error: errObj}
	_ = json.NewEncoder(w).Encode(resp)
}

func writeResult(w http.ResponseWriter, id interface{}, result interface{}) {
	resp := RPCResponse{JSONRPC: jsonRPCVersion, ID: id, Result: result}
	_ = json.NewEncoder(w).Encode(resp)
}

type BalanceResponse struct {
	Address            string                `json:"address"`
	BalanceNHB         *big.Int              `json:"balanceNHB"`
	BalanceZNHB        *big.Int              `json:"balanceZNHB"`
	Stake              *big.Int              `json:"stake"`
	LockedZNHB         *big.Int              `json:"lockedZNHB"`
	DelegatedValidator string                `json:"delegatedValidator,omitempty"`
	PendingUnbonds     []StakeUnbondResponse `json:"pendingUnbonds,omitempty"`
	Username           string                `json:"username"`
	Nonce              uint64                `json:"nonce"`
	EngagementScore    uint64                `json:"engagementScore"`
}

type StakeUnbondResponse struct {
	ID          uint64   `json:"id"`
	Validator   string   `json:"validator"`
	Amount      *big.Int `json:"amount"`
	ReleaseTime uint64   `json:"releaseTime"`
}

type EpochSummaryResult struct {
	Epoch                  uint64   `json:"epoch"`
	Height                 uint64   `json:"height"`
	FinalizedAt            int64    `json:"finalizedAt"`
	TotalWeight            string   `json:"totalWeight"`
	ActiveValidators       []string `json:"activeValidators"`
	EligibleValidatorCount int      `json:"eligibleValidatorCount"`
}

type EpochWeightResult struct {
	Address    string `json:"address"`
	Stake      string `json:"stake"`
	Engagement uint64 `json:"engagement"`
	Composite  string `json:"compositeWeight"`
}

type EpochSnapshotResult struct {
	Epoch       uint64              `json:"epoch"`
	Height      uint64              `json:"height"`
	FinalizedAt int64               `json:"finalizedAt"`
	TotalWeight string              `json:"totalWeight"`
	Weights     []EpochWeightResult `json:"weights"`
	Selected    []string            `json:"selectedValidators"`
}

func parseEpochParam(raw json.RawMessage) (uint64, bool, error) {
	if raw == nil {
		return 0, false, nil
	}

	var direct uint64
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, true, nil
	}

	var wrapper struct {
		Epoch *uint64 `json:"epoch"`
	}
	if err := json.Unmarshal(raw, &wrapper); err == nil && wrapper.Epoch != nil {
		return *wrapper.Epoch, true, nil
	}

	return 0, false, fmt.Errorf("invalid epoch parameter")
}

// handle is the main request handler that routes to specific handlers.
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	reader := http.MaxBytesReader(w, r.Body, maxRequestBytes)
	defer func() {
		_ = reader.Close()
	}()

	w.Header().Set("Content-Type", "application/json")

	body, err := io.ReadAll(reader)
	if err != nil {
		status := http.StatusBadRequest
		message := "failed to read request body"
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			status = http.StatusRequestEntityTooLarge
			message = fmt.Sprintf("request body exceeds %d bytes", maxRequestBytes)
		}
		writeError(w, status, nil, codeInvalidRequest, message, err.Error())
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeError(w, http.StatusBadRequest, nil, codeInvalidRequest, "request body required", nil)
		return
	}

	req := &RPCRequest{}
	if err := json.Unmarshal(body, req); err != nil {
		writeError(w, http.StatusBadRequest, nil, codeParseError, "invalid JSON payload", err.Error())
		return
	}
	if req.JSONRPC != "" && req.JSONRPC != jsonRPCVersion {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidRequest, "unsupported jsonrpc version", req.JSONRPC)
		return
	}
	if req.Method == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidRequest, "method required", nil)
		return
	}

	switch req.Method {
	case "nhb_sendTransaction":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSendTransaction(w, r, req)
	case "tx_previewSponsorship":
		s.handleTxPreviewSponsorship(w, r, req)
	case "tx_getSponsorshipConfig":
		s.handleTxGetSponsorshipConfig(w, r, req)
	case "tx_setSponsorshipEnabled":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleTxSetSponsorshipEnabled(w, r, req)
	case "nhb_getBalance":
		s.handleGetBalance(w, r, req)
	case "nhb_getLatestBlocks":
		s.handleGetLatestBlocks(w, r, req)
	case "nhb_getLatestTransactions":
		s.handleGetLatestTransactions(w, r, req)
	case "nhb_getEpochSummary":
		s.handleGetEpochSummary(w, r, req)
	case "nhb_getEpochSnapshot":
		s.handleGetEpochSnapshot(w, r, req)
	case "nhb_getRewardEpoch":
		s.handleGetRewardEpoch(w, r, req)
	case "nhb_getRewardPayout":
		s.handleGetRewardPayout(w, r, req)
	case "mint_with_sig":
		s.handleMintWithSig(w, r, req)
	case "swap_submitVoucher":
		s.handleSwapSubmitVoucher(w, r, req)
	case "swap_voucher_get":
		s.handleSwapVoucherGet(w, r, req)
	case "swap_voucher_list":
		s.handleSwapVoucherList(w, r, req)
	case "swap_voucher_export":
		s.handleSwapVoucherExport(w, r, req)
	case "swap_limits":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapLimits(w, r, req)
	case "swap_provider_status":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapProviderStatus(w, r, req)
	case "swap_burn_list":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapBurnList(w, r, req)
	case "swap_voucher_reverse":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapVoucherReverse(w, r, req)
	case "stake_delegate":
		s.handleStakeDelegate(w, r, req)
	case "stake_undelegate":
		s.handleStakeUndelegate(w, r, req)
	case "stake_claim":
		s.handleStakeClaim(w, r, req)
	case "loyalty_createBusiness":
		s.handleLoyaltyCreateBusiness(w, r, req)
	case "loyalty_setPaymaster":
		s.handleLoyaltySetPaymaster(w, r, req)
	case "loyalty_addMerchant":
		s.handleLoyaltyAddMerchant(w, r, req)
	case "loyalty_removeMerchant":
		s.handleLoyaltyRemoveMerchant(w, r, req)
	case "loyalty_createProgram":
		s.handleLoyaltyCreateProgram(w, r, req)
	case "loyalty_updateProgram":
		s.handleLoyaltyUpdateProgram(w, r, req)
	case "loyalty_pauseProgram":
		s.handleLoyaltyPauseProgram(w, r, req)
	case "loyalty_resumeProgram":
		s.handleLoyaltyResumeProgram(w, r, req)
	case "loyalty_getBusiness":
		s.handleLoyaltyGetBusiness(w, r, req)
	case "loyalty_listPrograms":
		s.handleLoyaltyListPrograms(w, r, req)
	case "loyalty_programStats":
		s.handleLoyaltyProgramStats(w, r, req)
	case "loyalty_userDaily":
		s.handleLoyaltyUserDaily(w, r, req)
	case "loyalty_paymasterBalance":
		s.handleLoyaltyPaymasterBalance(w, r, req)
	case "loyalty_resolveUsername":
		s.handleLoyaltyResolveUsername(w, r, req)
	case "loyalty_userQR":
		s.handleLoyaltyUserQR(w, r, req)
	case "creator_publish":
		s.handleCreatorPublish(w, r, req)
	case "creator_tip":
		s.handleCreatorTip(w, r, req)
	case "creator_stake":
		s.handleCreatorStake(w, r, req)
	case "creator_unstake":
		s.handleCreatorUnstake(w, r, req)
	case "creator_payouts":
		s.handleCreatorPayouts(w, r, req)
	case "identity_setAlias":
		s.handleIdentitySetAlias(w, r, req)
	case "identity_setAvatar":
		s.handleIdentitySetAvatar(w, r, req)
	case "identity_resolve":
		s.handleIdentityResolve(w, r, req)
	case "identity_reverse":
		s.handleIdentityReverse(w, r, req)
	case "identity_createClaimable":
		s.handleIdentityCreateClaimable(w, r, req)
	case "identity_claim":
		s.handleIdentityClaim(w, r, req)
	case "claimable_create":
		s.handleClaimableCreate(w, r, req)
	case "claimable_claim":
		s.handleClaimableClaim(w, r, req)
	case "claimable_cancel":
		s.handleClaimableCancel(w, r, req)
	case "claimable_get":
		s.handleClaimableGet(w, r, req)
	case "escrow_create":
		s.handleEscrowCreate(w, r, req)
	case "escrow_get":
		s.handleEscrowGet(w, r, req)
	case "escrow_getRealm":
		s.handleEscrowGetRealm(w, r, req)
	case "escrow_getSnapshot":
		s.handleEscrowGetSnapshot(w, r, req)
	case "escrow_listEvents":
		s.handleEscrowListEvents(w, r, req)
	case "escrow_fund":
		s.handleEscrowFund(w, r, req)
	case "escrow_release":
		s.handleEscrowRelease(w, r, req)
	case "escrow_refund":
		s.handleEscrowRefund(w, r, req)
	case "escrow_expire":
		s.handleEscrowExpire(w, r, req)
        case "escrow_dispute":
                s.handleEscrowDispute(w, r, req)
        case "escrow_resolve":
                s.handleEscrowResolve(w, r, req)
        case "escrow_milestoneCreate":
                s.handleEscrowMilestoneCreate(w, r, req)
        case "escrow_milestoneGet":
                s.handleEscrowMilestoneGet(w, r, req)
        case "escrow_milestoneFund":
                s.handleEscrowMilestoneFund(w, r, req)
        case "escrow_milestoneRelease":
                s.handleEscrowMilestoneRelease(w, r, req)
        case "escrow_milestoneCancel":
                s.handleEscrowMilestoneCancel(w, r, req)
        case "escrow_milestoneSubscriptionUpdate":
                s.handleEscrowMilestoneSubscriptionUpdate(w, r, req)
	case "net_info":
		s.handleNetInfo(w, r, req)
	case "net_peers":
		s.handleNetPeers(w, r, req)
	case "net_dial":
		s.handleNetDial(w, r, req)
	case "net_ban":
		s.handleNetBan(w, r, req)
	case "sync_snapshot_export":
		s.handleSyncSnapshotExport(w, r, req)
	case "sync_snapshot_import":
		s.handleSyncSnapshotImport(w, r, req)
	case "sync_status":
		s.handleSyncStatus(w, r, req)
	case "p2p_info":
		s.handleP2PInfo(w, r, req)
	case "p2p_peers":
		s.handleP2PPeers(w, r, req)
	case "p2p_createTrade":
		s.handleP2PCreateTrade(w, r, req)
	case "p2p_getTrade":
		s.handleP2PGetTrade(w, r, req)
	case "p2p_settle":
		s.handleP2PSettle(w, r, req)
	case "p2p_dispute":
		s.handleP2PDispute(w, r, req)
	case "p2p_resolve":
		s.handleP2PResolve(w, r, req)
	case "engagement_register_device":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleEngagementRegisterDevice(w, r, req)
	case "engagement_submit_heartbeat":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleEngagementSubmitHeartbeat(w, r, req)
	case "potso_heartbeat":
		s.handlePotsoHeartbeat(w, r, req)
	case "potso_userMeters":
		s.handlePotsoUserMeters(w, r, req)
	case "potso_top":
		s.handlePotsoTop(w, r, req)
	case "potso_leaderboard":
		s.handlePotsoLeaderboard(w, r, req)
	case "potso_params":
		s.handlePotsoParams(w, r, req)
	case "potso_stake_lock":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeLock(w, r, req)
	case "potso_stake_unbond":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeUnbond(w, r, req)
	case "potso_stake_withdraw":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeWithdraw(w, r, req)
	case "potso_stake_info":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeInfo(w, r, req)
	case "potso_epoch_info":
		s.handlePotsoEpochInfo(w, r, req)
	case "potso_epoch_payouts":
		s.handlePotsoEpochPayouts(w, r, req)
	case "potso_reward_claim":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(w, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoRewardClaim(w, r, req)
	case "potso_rewards_history":
		s.handlePotsoRewardsHistory(w, r, req)
	case "potso_export_epoch":
		s.handlePotsoExportEpoch(w, r, req)
	case "potso_submitEvidence":
		s.handlePotsoSubmitEvidence(w, r, req)
	case "potso_getEvidence":
		s.handlePotsoGetEvidence(w, r, req)
	case "potso_listEvidence":
		s.handlePotsoListEvidence(w, r, req)
	case "gov_propose":
		s.handleGovernancePropose(w, r, req)
	case "gov_vote":
		s.handleGovernanceVote(w, r, req)
	case "gov_proposal":
		s.handleGovernanceProposal(w, r, req)
	case "gov_list":
		s.handleGovernanceList(w, r, req)
	case "gov_finalize":
		s.handleGovernanceFinalize(w, r, req)
        case "gov_queue":
                s.handleGovernanceQueue(w, r, req)
        case "gov_execute":
                s.handleGovernanceExecute(w, r, req)
        case "reputation_verifySkill":
                s.handleReputationVerifySkill(w, r, req)
        default:
                writeError(w, http.StatusNotFound, req.ID, codeMethodNotFound, fmt.Sprintf("unknown method %s", req.Method), nil)
        }
}

// --- NEW HANDLER: Get Latest Blocks ---
func (s *Server) handleGetLatestBlocks(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	count := 10
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params[0], &count); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "count must be an integer", err.Error())
			return
		}
	}
	if count <= 0 {
		count = 10
	} else if count > 20 {
		count = 20
	}

	latestHeight := s.node.Chain().GetHeight()
	blocks := make([]*types.Block, 0, count)

	for i := 0; i < count && uint64(i) <= latestHeight; i++ {
		height := latestHeight - uint64(i)
		block, err := s.node.Chain().GetBlockByHeight(height)
		if err != nil {
			break // Stop if we go past the genesis block
		}
		blocks = append(blocks, block)
	}
	writeResult(w, req.ID, blocks)
}

// --- NEW HANDLER: Get Latest Transactions ---
func (s *Server) handleGetLatestTransactions(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	count := 20
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params[0], &count); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "count must be an integer", err.Error())
			return
		}
	}
	if count <= 0 {
		count = 20
	} else if count > 50 {
		count = 50
	}

	latestHeight := s.node.Chain().GetHeight()
	var txs []*types.Transaction

	// Iterate backwards from the latest block until we have enough transactions
	for i := uint64(0); i <= latestHeight && len(txs) < count; i++ {
		height := latestHeight - i
		block, err := s.node.Chain().GetBlockByHeight(height)
		if err != nil {
			break
		}
		txs = append(txs, block.Transactions...)
	}

	// Ensure we only return the requested number of transactions
	if len(txs) > count {
		txs = txs[:count]
	}
	writeResult(w, req.ID, txs)
}

func (s *Server) handleGetEpochSummary(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	var epochNumber uint64
	var haveEpoch bool
	if len(req.Params) > 0 {
		value, ok, err := parseEpochParam(req.Params[0])
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
			return
		}
		if ok {
			epochNumber = value
			haveEpoch = true
		}
	}

	var (
		summary *epoch.Summary
		exists  bool
	)
	if haveEpoch {
		summary, exists = s.node.EpochSummary(epochNumber)
	} else {
		summary, exists = s.node.LatestEpochSummary()
	}
	if !exists || summary == nil {
		writeError(w, http.StatusNotFound, req.ID, codeServerError, "epoch summary not found", nil)
		return
	}

	active := make([]string, len(summary.ActiveValidators))
	for i := range summary.ActiveValidators {
		active[i] = "0x" + hex.EncodeToString(summary.ActiveValidators[i])
	}
	total := big.NewInt(0)
	if summary.TotalWeight != nil {
		total = new(big.Int).Set(summary.TotalWeight)
	}
	result := EpochSummaryResult{
		Epoch:                  summary.Epoch,
		Height:                 summary.Height,
		FinalizedAt:            summary.FinalizedAt,
		TotalWeight:            total.String(),
		ActiveValidators:       active,
		EligibleValidatorCount: summary.EligibleCount,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleGetEpochSnapshot(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	var epochNumber uint64
	var haveEpoch bool
	if len(req.Params) > 0 {
		value, ok, err := parseEpochParam(req.Params[0])
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
			return
		}
		if ok {
			epochNumber = value
			haveEpoch = true
		}
	}

	var (
		snapshot *epoch.Snapshot
		exists   bool
	)
	if haveEpoch {
		snapshot, exists = s.node.EpochSnapshot(epochNumber)
	} else {
		snapshot, exists = s.node.LatestEpochSnapshot()
	}
	if !exists || snapshot == nil {
		writeError(w, http.StatusNotFound, req.ID, codeServerError, "epoch snapshot not found", nil)
		return
	}

	weights := make([]EpochWeightResult, len(snapshot.Weights))
	for i := range snapshot.Weights {
		stake := big.NewInt(0)
		if snapshot.Weights[i].Stake != nil {
			stake = new(big.Int).Set(snapshot.Weights[i].Stake)
		}
		composite := big.NewInt(0)
		if snapshot.Weights[i].Composite != nil {
			composite = new(big.Int).Set(snapshot.Weights[i].Composite)
		}
		weights[i] = EpochWeightResult{
			Address:    "0x" + hex.EncodeToString(snapshot.Weights[i].Address),
			Stake:      stake.String(),
			Engagement: snapshot.Weights[i].Engagement,
			Composite:  composite.String(),
		}
	}

	selected := make([]string, len(snapshot.Selected))
	for i := range snapshot.Selected {
		selected[i] = "0x" + hex.EncodeToString(snapshot.Selected[i])
	}

	total := big.NewInt(0)
	if snapshot.TotalWeight != nil {
		total = new(big.Int).Set(snapshot.TotalWeight)
	}

	result := EpochSnapshotResult{
		Epoch:       snapshot.Epoch,
		Height:      snapshot.Height,
		FinalizedAt: snapshot.FinalizedAt,
		TotalWeight: total.String(),
		Weights:     weights,
		Selected:    selected,
	}
	writeResult(w, req.ID, result)
}

func (s *Server) requireAuth(r *http.Request) *RPCError {
	if s.authToken == "" {
		return &RPCError{Code: codeUnauthorized, Message: "RPC authentication token not configured"}
	}
	header := r.Header.Get("Authorization")
	if header == "" {
		return &RPCError{Code: codeUnauthorized, Message: "missing Authorization header"}
	}
	if !strings.HasPrefix(header, "Bearer ") {
		return &RPCError{Code: codeUnauthorized, Message: "Authorization header must use Bearer scheme"}
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if token == "" {
		return &RPCError{Code: codeUnauthorized, Message: "missing bearer token"}
	}
	if subtle.ConstantTimeCompare([]byte(token), []byte(s.authToken)) != 1 {
		return &RPCError{Code: codeUnauthorized, Message: "invalid RPC credentials"}
	}
	return nil
}

func (s *Server) allowSource(source string, now time.Time) bool {
	if source == "" {
		source = "unknown"
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	limiter, ok := s.rateLimiters[source]
	if !ok {
		limiter = &rateLimiter{windowStart: now}
		s.rateLimiters[source] = limiter
	}
	if now.Sub(limiter.windowStart) >= rateLimitWindow {
		limiter.windowStart = now
		limiter.count = 0
	}
	if limiter.count >= maxTxPerWindow {
		return false
	}
	limiter.count++
	return true
}

func (s *Server) rememberTx(hash string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	for h, seenAt := range s.txSeen {
		if now.Sub(seenAt) > txSeenTTL {
			delete(s.txSeen, h)
		}
	}
	if _, exists := s.txSeen[hash]; exists {
		return false
	}
	s.txSeen[hash] = now
	return true
}

func clientSource(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		if len(parts) > 0 {
			candidate := strings.TrimSpace(parts[0])
			if candidate != "" {
				return candidate
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// --- Existing Handlers ---
func (s *Server) handleSendTransaction(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "transaction parameter required", nil)
		return
	}

	var tx types.Transaction
	if err := json.Unmarshal(req.Params[0], &tx); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid transaction format", err.Error())
		return
	}
	if !types.IsValidChainID(tx.ChainID) {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "transaction chainId does not match NHBCoin network", tx.ChainID)
		return
	}
	if tx.GasLimit == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "gasLimit must be greater than zero", nil)
		return
	}
	if tx.GasPrice == nil || tx.GasPrice.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "gasPrice must be greater than zero", nil)
		return
	}

	from, err := tx.From()
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid transaction signature", err.Error())
		return
	}

	account, err := s.node.GetAccount(from)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load sender account", err.Error())
		return
	}
	if tx.Nonce < account.Nonce {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, fmt.Sprintf("nonce %d has already been used; current account nonce is %d", tx.Nonce, account.Nonce), nil)
		return
	}

	now := time.Now()
	source := clientSource(r)
	if !s.allowSource(source, now) {
		writeError(w, http.StatusTooManyRequests, req.ID, codeRateLimited, "transaction rate limit exceeded", source)
		return
	}

	hashBytes, err := tx.Hash()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to hash transaction", err.Error())
		return
	}
	hash := hex.EncodeToString(hashBytes)
	if !s.rememberTx(hash, now) {
		writeError(w, http.StatusConflict, req.ID, codeDuplicateTx, "transaction has already been submitted", hash)
		return
	}

	s.node.AddTransaction(&tx)
	writeResult(w, req.ID, "Transaction received by node.")
}

func (s *Server) handleEscrowGetRealm(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	if s.escrow == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "escrow module unavailable", nil)
		return
	}
	result, modErr := s.escrow.GetRealm(req.Params[0])
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleEscrowGetSnapshot(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	if s.escrow == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "escrow module unavailable", nil)
		return
	}
	result, modErr := s.escrow.GetSnapshot(req.Params[0])
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleEscrowListEvents(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) > 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "too many parameters", nil)
		return
	}
	if s.escrow == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "escrow module unavailable", nil)
		return
	}
	var raw json.RawMessage
	if len(req.Params) == 1 {
		raw = req.Params[0]
	}
	result, modErr := s.escrow.ListEvents(raw)
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleTxPreviewSponsorship(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "transaction parameter required", nil)
		return
	}
	if s.transactions == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "transactions module unavailable", nil)
		return
	}
	result, modErr := s.transactions.PreviewSponsorship(req.Params[0])
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleTxSetSponsorshipEnabled(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "parameter object required", nil)
		return
	}
	if s.transactions == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "transactions module unavailable", nil)
		return
	}
	result, modErr := s.transactions.SetSponsorshipEnabled(req.Params[0])
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleTxGetSponsorshipConfig(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	if s.transactions == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "transactions module unavailable", nil)
		return
	}
	result, modErr := s.transactions.SponsorshipConfig()
	if modErr != nil {
		writeModuleError(w, req.ID, modErr)
		return
	}
	writeResult(w, req.ID, result)
}

func balanceResponseFromAccount(addr string, account *types.Account) BalanceResponse {
	resp := BalanceResponse{
		Address:         addr,
		BalanceNHB:      account.BalanceNHB,
		BalanceZNHB:     account.BalanceZNHB,
		Stake:           account.Stake,
		Username:        account.Username,
		Nonce:           account.Nonce,
		EngagementScore: account.EngagementScore,
	}
	if account.LockedZNHB != nil {
		resp.LockedZNHB = account.LockedZNHB
	}
	if len(account.DelegatedValidator) > 0 {
		resp.DelegatedValidator = crypto.NewAddress(crypto.NHBPrefix, account.DelegatedValidator).String()
	}
	if len(account.PendingUnbonds) > 0 {
		resp.PendingUnbonds = make([]StakeUnbondResponse, len(account.PendingUnbonds))
		for i, entry := range account.PendingUnbonds {
			validator := ""
			if len(entry.Validator) > 0 {
				validator = crypto.NewAddress(crypto.NHBPrefix, entry.Validator).String()
			}
			amount := big.NewInt(0)
			if entry.Amount != nil {
				amount = new(big.Int).Set(entry.Amount)
			}
			resp.PendingUnbonds[i] = StakeUnbondResponse{
				ID:          entry.ID,
				Validator:   validator,
				Amount:      amount,
				ReleaseTime: entry.ReleaseTime,
			}
		}
	}
	return resp
}

func (s *Server) handleGetBalance(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address parameter required", nil)
		return
	}
	var addrStr string
	if err := json.Unmarshal(req.Params[0], &addrStr); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address parameter", err.Error())
		return
	}
	addr, err := crypto.DecodeAddress(addrStr)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to decode address", err.Error())
		return
	}
	account, err := s.node.GetAccount(addr.Bytes())
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load account", err.Error())
		return
	}
	resp := balanceResponseFromAccount(addrStr, account)
	writeResult(w, req.ID, resp)
}
