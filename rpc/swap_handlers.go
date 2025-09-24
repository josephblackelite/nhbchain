package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"nhbchain/core"
	swap "nhbchain/native/swap"
)

func (s *Server) handleSwapSubmitVoucher(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected payload with voucher and sig", nil)
		return
	}
	var payload struct {
		Voucher json.RawMessage `json:"voucher"`
		Sig     string          `json:"sig"`
	}
	if err := json.Unmarshal(req.Params[0], &payload); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid payload", err.Error())
		return
	}
	if len(bytes.TrimSpace(payload.Voucher)) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "voucher required", nil)
		return
	}
	var voucher swap.VoucherV1
	if err := json.Unmarshal(payload.Voucher, &voucher); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid voucher", err.Error())
		return
	}
	sigHex := strings.TrimSpace(payload.Sig)
	if sigHex == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "signature required", nil)
		return
	}
	sigHex = strings.TrimPrefix(sigHex, "0x")
	signature, err := hex.DecodeString(sigHex)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid signature", err.Error())
		return
	}
	txHash, minted, err := s.node.SwapSubmitVoucher(&voucher, signature)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrSwapInvalidSigner):
			writeError(w, http.StatusUnauthorized, req.ID, codeUnauthorized, err.Error(), nil)
		case errors.Is(err, core.ErrSwapNonceUsed):
			writeError(w, http.StatusConflict, req.ID, codeDuplicateTx, err.Error(), voucher.OrderID)
		case errors.Is(err, core.ErrSwapInvalidDomain),
			errors.Is(err, core.ErrSwapInvalidChainID),
			errors.Is(err, core.ErrSwapExpired),
			errors.Is(err, core.ErrSwapInvalidToken),
			errors.Is(err, core.ErrSwapInvalidSignature),
			errors.Is(err, core.ErrSwapMintPaused):
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "swap voucher failed", err.Error())
		}
		return
	}
	writeResult(w, req.ID, map[string]interface{}{"txHash": txHash, "minted": minted})
}
