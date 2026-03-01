package rpc

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

func (s *Server) handleGetValidatorSet(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	validators := s.node.GetValidatorSet()
	type vSet struct {
		Address string `json:"address"`
		Stake   string `json:"stake"`
	}
	var res []vSet
	for addr, stake := range validators {
		// Attempting to normalize the key depending on how string(addr) is stored.
		// If it's pure bytes, hex encode it.
		encoded := addr
		if !strings.HasPrefix(encoded, "0x") {
			encoded = common.BytesToAddress([]byte(addr)).Hex()
		}
		res = append(res, vSet{
			Address: encoded,
			Stake:   stake.String(),
		})
	}
	writeResult(w, req.ID, map[string]any{
		"validators": res,
		"totalCount": len(res),
		"timestamp":  time.Now().Unix(),
	})
}

func (s *Server) handleGetValidatorInfo(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected address", nil)
		return
	}
	var addrStr string
	if err := json.Unmarshal(req.Params[0], &addrStr); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address parameter", nil)
		return
	}
	addr := common.HexToAddress(addrStr)
	acc, err := s.node.GetAccount(addr.Bytes())
	if err != nil || acc == nil {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "validator not found", nil)
		return
	}
	writeResult(w, req.ID, map[string]any{
		"address":         addr.Hex(),
		"stake":           acc.Stake.String(),
		"engagementScore": acc.EngagementScore,
	})
}

func (s *Server) handleGetNetworkStats(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	// A basic health stub to satisfy portal dashboard requirements.
	// Can be extended with real metrics in Phase 5 runtime observability.
	writeResult(w, req.ID, map[string]any{
		"activeValidators": len(s.node.GetValidatorSet()),
		"currentEpoch":     0, // Stub
		"currentTime":      time.Now().Unix(),
		"mempoolSize":      0, // Stub
		"tps":              0, // Stub
	})
}

func (s *Server) handleGetLoyaltyBudgetStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	// Twap deviation parameters for the loyalty platform.
	writeResult(w, req.ID, map[string]any{
		"twapScalingFactor": "1.0", // 100% emission baseline
		"budgetRemaining":   "1000000000000000000000",
		"resetAt":           time.Now().Add(24 * time.Hour).Unix(),
	})
}

func (s *Server) handleGetOwnerWalletStats(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	writeResult(w, req.ID, map[string]any{
		"treasuryBalance": "0", // Stub
		"feeAccrual":      "0", // Stub
	})
}

func (s *Server) handleGetSlashingEvents(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	writeResult(w, req.ID, map[string]any{
		"events": []string{}, // Stub
	})
}
