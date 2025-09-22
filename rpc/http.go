package rpc

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"nhbchain/core"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

type Server struct {
	node *core.Node
}

func NewServer(node *core.Node) *Server {
	return &Server{node: node}
}

func (s *Server) Start(addr string) error {
	fmt.Printf("Starting JSON-RPC server on %s\n", addr)
	http.HandleFunc("/", s.handle)
	return http.ListenAndServe(addr, nil)
}

type RPCRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	ID     int               `json:"id"`
}

type RPCResponse struct {
	ID     int         `json:"id"`
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
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
	body, _ := io.ReadAll(r.Body)
	req := &RPCRequest{}
	json.Unmarshal(body, req)
	w.Header().Set("Content-Type", "application/json")

	switch req.Method {
	case "nhb_sendTransaction":
		s.handleSendTransaction(w, req)
	case "nhb_getBalance":
		s.handleGetBalance(w, req)
	// --- NEW ENDPOINTS FOR THE EXPLORER ---
	case "nhb_getLatestBlocks":
		s.handleGetLatestBlocks(w, req)
	case "nhb_getLatestTransactions":
		s.handleGetLatestTransactions(w, req)
	default:
		json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Error: fmt.Sprintf("unknown method %s", req.Method)})
	}
}

// --- NEW HANDLER: Get Latest Blocks ---
func (s *Server) handleGetLatestBlocks(w http.ResponseWriter, req *RPCRequest) {
	var count int
	if len(req.Params) > 0 {
		json.Unmarshal(req.Params[0], &count)
	}
	if count <= 0 || count > 20 {
		count = 10 // Default and max count
	}

	latestHeight := s.node.Chain().GetHeight()
	var blocks []*types.Block

	for i := 0; i < count; i++ {
		height := latestHeight - uint64(i)
		if height < 0 {
			break
		}
		block, err := s.node.Chain().GetBlockByHeight(height)
		if err != nil {
			break // Stop if we go past the genesis block
		}
		blocks = append(blocks, block)
	}
	json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Result: blocks})
}

// --- NEW HANDLER: Get Latest Transactions ---
func (s *Server) handleGetLatestTransactions(w http.ResponseWriter, req *RPCRequest) {
	var count int
	if len(req.Params) > 0 {
		json.Unmarshal(req.Params[0], &count)
	}
	if count <= 0 || count > 50 {
		count = 20 // Default and max
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
	json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Result: txs})
}

// --- Existing Handlers ---
func (s *Server) handleSendTransaction(w http.ResponseWriter, req *RPCRequest) {
	var tx types.Transaction
	if err := json.Unmarshal(req.Params[0], &tx); err != nil {
		json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Error: "invalid transaction format"})
		return
	}
	s.node.AddTransaction(&tx)
	json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Result: "Transaction received by node."})
}

func (s *Server) handleGetBalance(w http.ResponseWriter, req *RPCRequest) {
	var addrStr string
	if err := json.Unmarshal(req.Params[0], &addrStr); err != nil {
		json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Error: "invalid address parameter"})
		return
	}
	addr, err := crypto.DecodeAddress(addrStr)
	if err != nil {
		json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Error: "failed to decode address"})
		return
	}
	account, err := s.node.GetAccount(addr.Bytes())
	if err != nil {
		json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Error: err.Error()})
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
	json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Result: resp})
}
