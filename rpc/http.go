package rpc

import (
	"bytes"
	"context"
	"crypto/subtle"
	"crypto/tls"
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
	"nhbchain/observability"
	"nhbchain/p2p"
	"nhbchain/rpc/modules"
)

const (
	jsonRPCVersion          = "2.0"
	maxRequestBytes         = 1 << 20 // 1 MiB
	rateLimitWindow         = time.Minute
	maxTxPerWindow          = 5
	txSeenTTL               = 15 * time.Minute
	rateLimiterMaxEntries   = 512
	rateLimiterStaleAfter   = 10 * rateLimitWindow
	rateLimiterSweepBackoff = rateLimitWindow
	maxForwardedForAddrs    = 5
	maxTrustedProxyEntries  = 32
)

const (
	codeParseError              = -32700
	codeInvalidRequest          = -32600
	codeMethodNotFound          = -32601
	codeInvalidParams           = -32602
	codeUnauthorized            = -32001
	codeServerError             = -32000
	codeDuplicateTx             = -32010
	codeRateLimited             = -32020
	codeMempoolFull             = -32030
	codeInvalidPolicyInvariants = -32040
)

type rateLimiter struct {
	count       int
	windowStart time.Time
	lastSeen    time.Time
}

type txSeenEntry struct {
	hash   string
	seenAt time.Time
}

// ServerConfig controls optional behaviours of the RPC server.
type ServerConfig struct {
	// TrustProxyHeaders, when set, will cause the server to honour proxy
	// forwarding headers such as X-Forwarded-For regardless of the caller's
	// remote address. Use with caution when the server is guaranteed to be
	// behind a trusted reverse proxy.
	TrustProxyHeaders bool
	// TrustedProxies enumerates remote addresses that are authorised to relay
	// client requests. When a request originates from one of these proxies the
	// server will honour X-Forwarded-For headers.
	TrustedProxies []string
	// ReadHeaderTimeout specifies how long the server waits for headers.
	ReadHeaderTimeout time.Duration
	// ReadTimeout bounds the duration permitted to read the full request.
	ReadTimeout time.Duration
	// WriteTimeout bounds how long a handler may take to write a response.
	WriteTimeout time.Duration
	// IdleTimeout defines how long to keep idle connections open.
	IdleTimeout time.Duration
	// TLSCertFile is the path to a PEM-encoded certificate chain.
	TLSCertFile string
	// TLSKeyFile is the path to the PEM-encoded private key for TLSCertFile.
	TLSKeyFile string
}

// NetworkService abstracts the network control plane used by RPC handlers to
// interrogate the peer-to-peer daemon.
type NetworkService interface {
	NetworkView(ctx context.Context) (p2p.NetworkView, []string, error)
	NetworkPeers(ctx context.Context) ([]p2p.PeerNetInfo, error)
	Dial(ctx context.Context, target string) error
	Ban(ctx context.Context, nodeID string, duration time.Duration) error
}

type Server struct {
	node *core.Node
	net  NetworkService

	mu                sync.Mutex
	txSeen            map[string]time.Time
	txSeenQueue       []txSeenEntry
	rateLimiters      map[string]*rateLimiter
	rateLimiterSweep  time.Time
	authToken         string
	potsoEvidence     *modules.PotsoEvidenceModule
	transactions      *modules.TransactionsModule
	escrow            *modules.EscrowModule
	lending           *modules.LendingModule
	trustProxyHeaders bool
	trustedProxies    map[string]struct{}
	readHeaderTimeout time.Duration
	readTimeout       time.Duration
	writeTimeout      time.Duration
	idleTimeout       time.Duration
	tlsCertFile       string
	tlsKeyFile        string

	serverMu   sync.Mutex
	httpServer *http.Server
}

func NewServer(node *core.Node, netClient NetworkService, cfg ServerConfig) *Server {
	token := strings.TrimSpace(os.Getenv("NHB_RPC_TOKEN"))
	trusted := make(map[string]struct{}, len(cfg.TrustedProxies))
	count := 0
	for _, entry := range cfg.TrustedProxies {
		if count >= maxTrustedProxyEntries {
			break
		}
		trimmed := canonicalHost(entry)
		if trimmed == "" {
			continue
		}
		trusted[trimmed] = struct{}{}
		count++
	}
	return &Server{
		node:              node,
		net:               netClient,
		txSeen:            make(map[string]time.Time),
		rateLimiters:      make(map[string]*rateLimiter),
		authToken:         token,
		potsoEvidence:     modules.NewPotsoEvidenceModule(node),
		transactions:      modules.NewTransactionsModule(node),
		escrow:            modules.NewEscrowModule(node),
		lending:           modules.NewLendingModule(node),
		trustProxyHeaders: cfg.TrustProxyHeaders,
		trustedProxies:    trusted,
		readHeaderTimeout: cfg.ReadHeaderTimeout,
		readTimeout:       cfg.ReadTimeout,
		writeTimeout:      cfg.WriteTimeout,
		idleTimeout:       cfg.IdleTimeout,
		tlsCertFile:       strings.TrimSpace(cfg.TLSCertFile),
		tlsKeyFile:        strings.TrimSpace(cfg.TLSKeyFile),
	}
}

func (s *Server) Start(addr string) error {
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	fmt.Printf("Starting JSON-RPC server on %s\n", listener.Addr())
	return s.Serve(listener)
}

// Serve runs the RPC server using the provided listener. The listener is
// closed when Serve returns.
func (s *Server) Serve(listener net.Listener) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)

	srv := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           mux,
		ReadHeaderTimeout: s.readHeaderTimeout,
		ReadTimeout:       s.readTimeout,
		WriteTimeout:      s.writeTimeout,
		IdleTimeout:       s.idleTimeout,
	}

	var tlsConfig *tls.Config
	if s.tlsCertFile != "" || s.tlsKeyFile != "" {
		if s.tlsCertFile == "" || s.tlsKeyFile == "" {
			_ = listener.Close()
			return fmt.Errorf("both TLS certificate and key paths must be provided")
		}
		cert, err := tls.LoadX509KeyPair(s.tlsCertFile, s.tlsKeyFile)
		if err != nil {
			_ = listener.Close()
			return fmt.Errorf("load TLS key pair: %w", err)
		}
		tlsConfig = &tls.Config{Certificates: []tls.Certificate{cert}}
		srv.TLSConfig = tlsConfig
	}

	s.serverMu.Lock()
	s.httpServer = srv
	s.serverMu.Unlock()

	defer func() {
		s.serverMu.Lock()
		s.httpServer = nil
		s.serverMu.Unlock()
	}()

	if tlsConfig != nil {
		return srv.Serve(tls.NewListener(listener, tlsConfig))
	}
	return srv.Serve(listener)
}

// Shutdown gracefully terminates the RPC server if it is running.
func (s *Server) Shutdown(ctx context.Context) error {
	s.serverMu.Lock()
	srv := s.httpServer
	s.serverMu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
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

type rpcResponseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *rpcResponseRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
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

func moduleAndMethod(method string) (string, string) {
	trimmed := strings.TrimSpace(method)
	if trimmed == "" {
		return "", ""
	}
	if idx := strings.Index(trimmed, "_"); idx > 0 {
		module := trimmed[:idx]
		action := trimmed[idx+1:]
		if action == "" {
			action = "call"
		}
		return module, action
	}
	return trimmed, "call"
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

	moduleName, methodName := moduleAndMethod(req.Method)
	recorder := &rpcResponseRecorder{ResponseWriter: w, status: http.StatusOK}
	start := time.Now()
	defer func() {
		if moduleName == "" {
			return
		}
		metrics := observability.ModuleMetrics()
		metrics.Observe(moduleName, methodName, recorder.status, time.Since(start))
		if recorder.status == http.StatusTooManyRequests {
			metrics.RecordThrottle(moduleName, "rate_limit")
		}
	}()

	switch req.Method {
	case "nhb_sendTransaction":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSendTransaction(recorder, r, req)
	case "tx_previewSponsorship":
		s.handleTxPreviewSponsorship(recorder, r, req)
	case "tx_getSponsorshipConfig":
		s.handleTxGetSponsorshipConfig(recorder, r, req)
	case "tx_setSponsorshipEnabled":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleTxSetSponsorshipEnabled(recorder, r, req)
	case "nhb_getBalance":
		s.handleGetBalance(recorder, r, req)
	case "nhb_getLatestBlocks":
		s.handleGetLatestBlocks(recorder, r, req)
	case "nhb_getLatestTransactions":
		s.handleGetLatestTransactions(recorder, r, req)
	case "nhb_getEpochSummary":
		s.handleGetEpochSummary(recorder, r, req)
	case "nhb_getEpochSnapshot":
		s.handleGetEpochSnapshot(recorder, r, req)
	case "nhb_getRewardEpoch":
		s.handleGetRewardEpoch(recorder, r, req)
	case "nhb_getRewardPayout":
		s.handleGetRewardPayout(recorder, r, req)
	case "mint_with_sig":
		s.handleMintWithSig(recorder, r, req)
	case "swap_submitVoucher":
		s.handleSwapSubmitVoucher(recorder, r, req)
	case "swap_voucher_get":
		s.handleSwapVoucherGet(recorder, r, req)
	case "swap_voucher_list":
		s.handleSwapVoucherList(recorder, r, req)
	case "swap_voucher_export":
		s.handleSwapVoucherExport(recorder, r, req)
	case "swap_limits":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapLimits(recorder, r, req)
	case "swap_provider_status":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapProviderStatus(recorder, r, req)
	case "swap_burn_list":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapBurnList(recorder, r, req)
	case "swap_voucher_reverse":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleSwapVoucherReverse(recorder, r, req)
	case "lending_getMarket":
		s.handleLendingGetMarket(recorder, r, req)
	case "lend_getPools":
		s.handleLendGetPools(recorder, r, req)
	case "lend_createPool":
		s.handleLendCreatePool(recorder, r, req)
	case "lending_getUserAccount":
		s.handleLendingGetUserAccount(recorder, r, req)
	case "lending_supplyNHB":
		s.handleLendingSupplyNHB(recorder, r, req)
	case "lending_withdrawNHB":
		s.handleLendingWithdrawNHB(recorder, r, req)
	case "lending_depositZNHB":
		s.handleLendingDepositZNHB(recorder, r, req)
	case "lending_withdrawZNHB":
		s.handleLendingWithdrawZNHB(recorder, r, req)
	case "lending_borrowNHB":
		s.handleLendingBorrowNHB(recorder, r, req)
	case "lending_borrowNHBWithFee":
		s.handleLendingBorrowNHBWithFee(recorder, r, req)
	case "lending_repayNHB":
		s.handleLendingRepayNHB(recorder, r, req)
	case "lending_liquidate":
		s.handleLendingLiquidate(recorder, r, req)
	case "stake_delegate":
		s.handleStakeDelegate(recorder, r, req)
	case "stake_undelegate":
		s.handleStakeUndelegate(recorder, r, req)
	case "stake_claim":
		s.handleStakeClaim(recorder, r, req)
	case "loyalty_createBusiness":
		s.handleLoyaltyCreateBusiness(recorder, r, req)
	case "loyalty_setPaymaster":
		s.handleLoyaltySetPaymaster(recorder, r, req)
	case "loyalty_addMerchant":
		s.handleLoyaltyAddMerchant(recorder, r, req)
	case "loyalty_removeMerchant":
		s.handleLoyaltyRemoveMerchant(recorder, r, req)
	case "loyalty_createProgram":
		s.handleLoyaltyCreateProgram(recorder, r, req)
	case "loyalty_updateProgram":
		s.handleLoyaltyUpdateProgram(recorder, r, req)
	case "loyalty_pauseProgram":
		s.handleLoyaltyPauseProgram(recorder, r, req)
	case "loyalty_resumeProgram":
		s.handleLoyaltyResumeProgram(recorder, r, req)
	case "loyalty_getBusiness":
		s.handleLoyaltyGetBusiness(recorder, r, req)
	case "loyalty_listPrograms":
		s.handleLoyaltyListPrograms(recorder, r, req)
	case "loyalty_programStats":
		s.handleLoyaltyProgramStats(recorder, r, req)
	case "loyalty_userDaily":
		s.handleLoyaltyUserDaily(recorder, r, req)
	case "loyalty_paymasterBalance":
		s.handleLoyaltyPaymasterBalance(recorder, r, req)
	case "loyalty_resolveUsername":
		s.handleLoyaltyResolveUsername(recorder, r, req)
	case "loyalty_userQR":
		s.handleLoyaltyUserQR(recorder, r, req)
	case "creator_publish":
		s.handleCreatorPublish(recorder, r, req)
	case "creator_tip":
		s.handleCreatorTip(recorder, r, req)
	case "creator_stake":
		s.handleCreatorStake(recorder, r, req)
	case "creator_unstake":
		s.handleCreatorUnstake(recorder, r, req)
	case "creator_payouts":
		s.handleCreatorPayouts(recorder, r, req)
	case "identity_setAlias":
		s.handleIdentitySetAlias(recorder, r, req)
	case "identity_setAvatar":
		s.handleIdentitySetAvatar(recorder, r, req)
	case "identity_resolve":
		s.handleIdentityResolve(recorder, r, req)
	case "identity_reverse":
		s.handleIdentityReverse(recorder, r, req)
	case "identity_createClaimable":
		s.handleIdentityCreateClaimable(recorder, r, req)
	case "identity_claim":
		s.handleIdentityClaim(recorder, r, req)
	case "claimable_create":
		s.handleClaimableCreate(recorder, r, req)
	case "claimable_claim":
		s.handleClaimableClaim(recorder, r, req)
	case "claimable_cancel":
		s.handleClaimableCancel(recorder, r, req)
	case "claimable_get":
		s.handleClaimableGet(recorder, r, req)
	case "escrow_create":
		s.handleEscrowCreate(recorder, r, req)
	case "escrow_get":
		s.handleEscrowGet(recorder, r, req)
	case "escrow_getRealm":
		s.handleEscrowGetRealm(recorder, r, req)
	case "escrow_getSnapshot":
		s.handleEscrowGetSnapshot(recorder, r, req)
	case "escrow_listEvents":
		s.handleEscrowListEvents(recorder, r, req)
	case "escrow_fund":
		s.handleEscrowFund(recorder, r, req)
	case "escrow_release":
		s.handleEscrowRelease(recorder, r, req)
	case "escrow_refund":
		s.handleEscrowRefund(recorder, r, req)
	case "escrow_expire":
		s.handleEscrowExpire(recorder, r, req)
	case "escrow_dispute":
		s.handleEscrowDispute(recorder, r, req)
	case "escrow_resolve":
		s.handleEscrowResolve(recorder, r, req)
	case "escrow_milestoneCreate":
		s.handleEscrowMilestoneCreate(recorder, r, req)
	case "escrow_milestoneGet":
		s.handleEscrowMilestoneGet(recorder, r, req)
	case "escrow_milestoneFund":
		s.handleEscrowMilestoneFund(recorder, r, req)
	case "escrow_milestoneRelease":
		s.handleEscrowMilestoneRelease(recorder, r, req)
	case "escrow_milestoneCancel":
		s.handleEscrowMilestoneCancel(recorder, r, req)
	case "escrow_milestoneSubscriptionUpdate":
		s.handleEscrowMilestoneSubscriptionUpdate(recorder, r, req)
	case "net_info":
		s.handleNetInfo(recorder, r, req)
	case "net_peers":
		s.handleNetPeers(recorder, r, req)
	case "net_dial":
		s.handleNetDial(recorder, r, req)
	case "net_ban":
		s.handleNetBan(recorder, r, req)
	case "sync_snapshot_export":
		s.handleSyncSnapshotExport(recorder, r, req)
	case "sync_snapshot_import":
		s.handleSyncSnapshotImport(recorder, r, req)
	case "sync_status":
		s.handleSyncStatus(recorder, r, req)
	case "p2p_info":
		s.handleP2PInfo(recorder, r, req)
	case "p2p_peers":
		s.handleP2PPeers(recorder, r, req)
	case "p2p_createTrade":
		s.handleP2PCreateTrade(recorder, r, req)
	case "p2p_getTrade":
		s.handleP2PGetTrade(recorder, r, req)
	case "p2p_settle":
		s.handleP2PSettle(recorder, r, req)
	case "p2p_dispute":
		s.handleP2PDispute(recorder, r, req)
	case "p2p_resolve":
		s.handleP2PResolve(recorder, r, req)
	case "engagement_register_device":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleEngagementRegisterDevice(recorder, r, req)
	case "engagement_submit_heartbeat":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handleEngagementSubmitHeartbeat(recorder, r, req)
	case "potso_heartbeat":
		s.handlePotsoHeartbeat(recorder, r, req)
	case "potso_userMeters":
		s.handlePotsoUserMeters(recorder, r, req)
	case "potso_top":
		s.handlePotsoTop(recorder, r, req)
	case "potso_leaderboard":
		s.handlePotsoLeaderboard(recorder, r, req)
	case "potso_params":
		s.handlePotsoParams(recorder, r, req)
	case "potso_stake_lock":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeLock(recorder, r, req)
	case "potso_stake_unbond":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeUnbond(recorder, r, req)
	case "potso_stake_withdraw":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeWithdraw(recorder, r, req)
	case "potso_stake_info":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoStakeInfo(recorder, r, req)
	case "potso_epoch_info":
		s.handlePotsoEpochInfo(recorder, r, req)
	case "potso_epoch_payouts":
		s.handlePotsoEpochPayouts(recorder, r, req)
	case "potso_reward_claim":
		if authErr := s.requireAuth(r); authErr != nil {
			writeError(recorder, http.StatusUnauthorized, req.ID, authErr.Code, authErr.Message, authErr.Data)
			return
		}
		s.handlePotsoRewardClaim(recorder, r, req)
	case "potso_rewards_history":
		s.handlePotsoRewardsHistory(recorder, r, req)
	case "potso_export_epoch":
		s.handlePotsoExportEpoch(recorder, r, req)
	case "potso_submitEvidence":
		s.handlePotsoSubmitEvidence(recorder, r, req)
	case "potso_getEvidence":
		s.handlePotsoGetEvidence(recorder, r, req)
	case "potso_listEvidence":
		s.handlePotsoListEvidence(recorder, r, req)
	case "gov_propose":
		s.handleGovernancePropose(recorder, r, req)
	case "gov_vote":
		s.handleGovernanceVote(recorder, r, req)
	case "gov_proposal":
		s.handleGovernanceProposal(recorder, r, req)
	case "gov_list":
		s.handleGovernanceList(recorder, r, req)
	case "gov_finalize":
		s.handleGovernanceFinalize(recorder, r, req)
	case "gov_queue":
		s.handleGovernanceQueue(recorder, r, req)
	case "gov_execute":
		s.handleGovernanceExecute(recorder, r, req)
	case "reputation_verifySkill":
		s.handleReputationVerifySkill(recorder, r, req)
	default:
		writeError(recorder, http.StatusNotFound, req.ID, codeMethodNotFound, fmt.Sprintf("unknown method %s", req.Method), nil)
	}
}

// ServeHTTP allows the RPC server to satisfy the http.Handler interface for testing.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.handle(w, r)
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
	normalized := canonicalHost(source)
	if normalized == "" {
		normalized = "unknown"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.evictRateLimitersLocked(now)

	limiter, ok := s.rateLimiters[normalized]
	if !ok {
		if len(s.rateLimiters) >= rateLimiterMaxEntries {
			s.evictOldestLimiterLocked()
		}
		limiter = &rateLimiter{windowStart: now, lastSeen: now}
		s.rateLimiters[normalized] = limiter
	}

	if now.Sub(limiter.windowStart) >= rateLimitWindow {
		limiter.windowStart = now
		limiter.count = 0
	}
	if limiter.count >= maxTxPerWindow {
		limiter.lastSeen = now
		return false
	}
	limiter.count++
	limiter.lastSeen = now
	return true
}

func (s *Server) evictRateLimitersLocked(now time.Time) {
	if len(s.rateLimiters) == 0 {
		return
	}
	if !s.rateLimiterSweep.IsZero() && now.Sub(s.rateLimiterSweep) < rateLimiterSweepBackoff && len(s.rateLimiters) < rateLimiterMaxEntries {
		return
	}
	for key, limiter := range s.rateLimiters {
		if limiter.lastSeen.IsZero() {
			continue
		}
		if now.Sub(limiter.lastSeen) > rateLimiterStaleAfter {
			delete(s.rateLimiters, key)
		}
	}
	s.rateLimiterSweep = now
}

func (s *Server) evictOldestLimiterLocked() {
	if len(s.rateLimiters) == 0 {
		return
	}
	var oldestKey string
	var oldestTime time.Time
	hasOldest := false
	for key, limiter := range s.rateLimiters {
		switch {
		case !hasOldest:
			oldestKey = key
			oldestTime = limiter.lastSeen
			hasOldest = true
		case limiter.lastSeen.IsZero() && !oldestTime.IsZero():
			oldestKey = key
			oldestTime = limiter.lastSeen
		case limiter.lastSeen.Before(oldestTime):
			oldestKey = key
			oldestTime = limiter.lastSeen
		}
	}
	if hasOldest {
		delete(s.rateLimiters, oldestKey)
	}
}

func (s *Server) rememberTx(hash string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.evictExpiredTxLocked(now)

	if _, exists := s.txSeen[hash]; exists {
		return false
	}
	s.txSeen[hash] = now
	s.txSeenQueue = append(s.txSeenQueue, txSeenEntry{hash: hash, seenAt: now})
	return true
}

func (s *Server) evictExpiredTxLocked(now time.Time) {
	if len(s.txSeenQueue) == 0 {
		return
	}

	cutoff := now.Add(-txSeenTTL)
	idx := 0
	for idx < len(s.txSeenQueue) {
		entry := s.txSeenQueue[idx]
		if !entry.seenAt.Before(cutoff) {
			break
		}
		delete(s.txSeen, entry.hash)
		idx++
	}

	if idx == 0 {
		return
	}

	for i := 0; i < idx; i++ {
		s.txSeenQueue[i] = txSeenEntry{}
	}
	s.txSeenQueue = s.txSeenQueue[idx:]
	if len(s.txSeenQueue) == 0 {
		s.txSeenQueue = nil
	}
}

func (s *Server) clientSource(r *http.Request) string {
	host := r.RemoteAddr
	if splitHost, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		host = splitHost
	}
	host = canonicalHost(host)
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		if s.trustProxyHeaders || s.isTrustedProxy(host) {
			parts := strings.Split(forwarded, ",")
			for i, part := range parts {
				if i >= maxForwardedForAddrs {
					break
				}
				candidate := canonicalHost(part)
				if candidate != "" {
					return candidate
				}
			}
		}
	}
	return host
}

func (s *Server) isTrustedProxy(host string) bool {
	if len(s.trustedProxies) == 0 {
		return false
	}
	normalized := canonicalHost(host)
	if normalized == "" {
		return false
	}
	_, ok := s.trustedProxies[normalized]
	return ok
}

func canonicalHost(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if host, _, err := net.SplitHostPort(trimmed); err == nil {
		trimmed = host
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return ip.String()
	}
	return strings.ToLower(trimmed)
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
	source := s.clientSource(r)
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

	if err := s.node.AddTransaction(&tx); err != nil {
		switch {
		case errors.Is(err, core.ErrInvalidTransaction):
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid transaction", err.Error())
			return
		case errors.Is(err, core.ErrMempoolFull):
			writeError(w, http.StatusServiceUnavailable, req.ID, codeMempoolFull, "mempool full", nil)
			return
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to add transaction", err.Error())
			return
		}
	}
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
