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
	"nhbchain/core/types"
	"nhbchain/crypto"
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

	mu           sync.Mutex
	txSeen       map[string]time.Time
	rateLimiters map[string]*rateLimiter
	authToken    string
}

func NewServer(node *core.Node) *Server {
	token := strings.TrimSpace(os.Getenv("NHB_RPC_TOKEN"))
	return &Server{
		node:         node,
		txSeen:       make(map[string]time.Time),
		rateLimiters: make(map[string]*rateLimiter),
		authToken:    token,
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
	Address         string   `json:"address"`
	BalanceNHB      *big.Int `json:"balanceNHB"`
	BalanceZNHB     *big.Int `json:"balanceZNHB"`
	Stake           *big.Int `json:"stake"`
	Username        string   `json:"username"`
	Nonce           uint64   `json:"nonce"`
	EngagementScore uint64   `json:"engagementScore"`
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
	case "nhb_getBalance":
		s.handleGetBalance(w, r, req)
	case "nhb_getLatestBlocks":
		s.handleGetLatestBlocks(w, r, req)
	case "nhb_getLatestTransactions":
		s.handleGetLatestTransactions(w, r, req)
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
	resp := BalanceResponse{
		Address:         addrStr,
		BalanceNHB:      account.BalanceNHB,
		BalanceZNHB:     account.BalanceZNHB,
		Stake:           account.Stake,
		Username:        account.Username,
		Nonce:           account.Nonce,
		EngagementScore: account.EngagementScore,
	}
	writeResult(w, req.ID, resp)
}
