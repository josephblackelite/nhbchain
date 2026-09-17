package rpc

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"strings"
)

// handleLendingGetRefPriceStatus is a public read: reports the most
// recently accepted lending oracle reference price, if any. A submission
// service should call this before signing and submitting, both to confirm a
// prior submission actually landed and to read back the Timestamp a new
// submission's own Timestamp must exceed (see core/lending_tx.go's
// applyLendingRefPriceTransaction replay-protection check).
func (s *Server) handleLendingGetRefPriceStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	status, err := s.node.LendingRefPriceStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load reference price status", nil)
		return
	}
	writeResult(w, req.ID, status)
}

// handleLendingSubmitRefPrice accepts an M-of-N-signed reference price
// bundle and submits it as a TxTypeLendingRefPrice transaction. Signature
// verification against the genesis-immutable signer quorum happens on-chain
// (core/lending_tx.go's applyLendingRefPriceTransaction, via
// AddTransaction's synchronous simulation) -- this handler does no
// cryptographic verification itself, it only decodes the request and
// forwards it. Auth-gated the same way buyback_submitRefPrice is: meant to
// be called by an authorized submission service, not end users or
// third-party integration partners.
func (s *Server) handleLendingSubmitRefPrice(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "exactly one parameter object expected", nil)
		return
	}
	var params struct {
		RateNum    string   `json:"rateNum"`
		RateDenom  string   `json:"rateDenom"`
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

	txHash, err := s.node.SubmitLendingRefPrice(rateNum, rateDenom, params.Timestamp, signatures)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to submit reference price", err.Error())
		return
	}
	writeResult(w, req.ID, map[string]interface{}{"txHash": txHash})
}
