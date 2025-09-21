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

// Server is the JSON-RPC server that exposes methods to interact with the node.
type Server struct {
	node *core.Node
}

// NewServer creates a new RPC server.
func NewServer(node *core.Node) *Server {
	return &Server{node: node}
}

// Start begins listening for HTTP requests on the given address.
func (s *Server) Start(addr string) error {
	fmt.Printf("Starting JSON-RPC server on %s\n", addr)
	http.HandleFunc("/", s.handle)
	return http.ListenAndServe(addr, nil)
}

// A generic structure for a JSON-RPC request.
type RPCRequest struct {
	Method string            `json:"method"`
	Params []json.RawMessage `json:"params"`
	ID     int               `json:"id"`
}

// A generic structure for a JSON-RPC response.
type RPCResponse struct {
	ID     int         `json:"id"`
	Result interface{} `json:"result,omitempty"`
	Error  string      `json:"error,omitempty"`
}

// UPDATED: The response structure now includes all on-chain account data.
type BalanceResponse struct {
	Address         string   `json:"address"`
	BalanceNHB      *big.Int `json:"balanceNHB"`
	BalanceZNHB     *big.Int `json:"balanceZNHB"`
	Stake           *big.Int `json:"stake"` // NEW: The user's staked balance
	Username        string   `json:"username"`
	Nonce           uint64   `json:"nonce"`
	EngagementScore uint64   `json:"engagementScore"` // NEW: The user's engagement score
}

// handle is the main request handler.
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req := &RPCRequest{}
	json.Unmarshal(body, req)
	w.Header().Set("Content-Type", "application/json")

	switch req.Method {
	case "nhb_sendTransaction":
		var tx types.Transaction
		if err := json.Unmarshal(req.Params[0], &tx); err != nil {
			json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Error: "invalid transaction format"})
			return
		}
		s.node.AddTransaction(&tx)
		json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Result: "Transaction received by node."})

	case "nhb_getBalance":
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

		// UPDATED: The response object is now populated with the complete account state.
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

	default:
		json.NewEncoder(w).Encode(RPCResponse{ID: req.ID, Error: fmt.Sprintf("unknown method %s", req.Method)})
	}
}
