package rpc

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
)

// handleBuybackGetRefPriceStatus is a public read: reports whether a
// verified reference price is already on file for the requested epoch (or
// the current open epoch, if none is supplied). A submission service should
// call this before signing and submitting -- only the first submission per
// epoch is ever accepted (see core/buyback_tx.go's applyBuybackRefPrice),
// so checking first avoids a wasted signing round, and confirms a prior
// submission actually landed.
func (s *Server) handleBuybackGetRefPriceStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	var params struct {
		Epoch *uint64 `json:"epoch,omitempty"`
	}
	if len(req.Params) > 0 {
		if err := json.Unmarshal(req.Params[0], &params); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
			return
		}
	}

	epoch := uint64(0)
	if params.Epoch != nil {
		epoch = *params.Epoch
	} else {
		current, ok := s.node.CurrentBuybackEpoch()
		if !ok {
			writeError(w, http.StatusServiceUnavailable, req.ID, codeServerError, "buyback epoch scheduling is not enabled on this network", nil)
			return
		}
		epoch = current
	}
	if epoch == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "epoch must be positive", nil)
		return
	}

	status, err := s.node.BuybackRefPriceStatusForEpoch(epoch)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load reference price status", nil)
		return
	}
	writeResult(w, req.ID, status)
}

// handleBuybackSubmitRefPrice accepts an M-of-N-signed reference price
// bundle and submits it as a TxTypeBuybackRefPrice transaction. Signature
// verification against the genesis-immutable signer quorum happens on-chain
// (core/buyback_tx.go's applyBuybackRefPrice, via AddTransaction's
// synchronous simulation) -- this handler does no cryptographic
// verification itself, it only decodes the request and forwards it.
// Auth-gated the same way other ops-only write endpoints are
// (e.g. swap_setManualQuote): this is meant to be called by an authorized
// submission service, not end users or third-party integration partners.
func (s *Server) handleBuybackSubmitRefPrice(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params struct {
		RateNum    string   `json:"rateNum"`
		RateDenom  string   `json:"rateDenom"`
		Epoch      uint64   `json:"epoch"`
		Timestamp  uint64   `json:"timestamp"`
		Signatures []string `json:"signatures"`
	}
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameter object", err.Error())
		return
	}

	rateNum, ok := new(big.Int).SetString(strings.TrimSpace(params.RateNum), 10)
	if !ok || rateNum.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "rateNum must be a positive decimal integer", nil)
		return
	}
	rateDenom, ok := new(big.Int).SetString(strings.TrimSpace(params.RateDenom), 10)
	if !ok || rateDenom.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "rateDenom must be a positive decimal integer", nil)
		return
	}
	if len(params.Signatures) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "at least one signature required", nil)
		return
	}
	signatures := make([][]byte, 0, len(params.Signatures))
	for i, raw := range params.Signatures {
		trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "0x")
		sig, err := hex.DecodeString(trimmed)
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid signature encoding", map[string]interface{}{"index": i})
			return
		}
		signatures = append(signatures, sig)
	}

	txHash, err := s.node.SubmitBuybackRefPrice(rateNum, rateDenom, params.Epoch, params.Timestamp, signatures)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to submit reference price", err.Error())
		return
	}
	writeResult(w, req.ID, map[string]interface{}{"txHash": txHash})
}
