package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"nhbchain/core"
)

func (s *Server) handleMintWithSig(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 2 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected voucher and signature", nil)
		return
	}

	rawVoucher := bytes.TrimSpace(req.Params[0])
	var voucherBytes []byte
	if len(rawVoucher) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "voucher payload required", nil)
		return
	}
	if rawVoucher[0] == '"' {
		var voucherStr string
		if err := json.Unmarshal(rawVoucher, &voucherStr); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid voucher payload", err.Error())
			return
		}
		voucherBytes = []byte(strings.TrimSpace(voucherStr))
	} else {
		voucherBytes = rawVoucher
	}
	voucherBytes = bytes.TrimSpace(voucherBytes)
	if len(voucherBytes) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "voucher payload required", nil)
		return
	}

	var voucher core.MintVoucher
	if err := json.Unmarshal(voucherBytes, &voucher); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid voucher payload", err.Error())
		return
	}

	var sigHex string
	if err := json.Unmarshal(req.Params[1], &sigHex); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid signature", err.Error())
		return
	}
	sig := strings.TrimPrefix(strings.TrimSpace(sigHex), "0x")
	if sig == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "signature required", nil)
		return
	}
	signature, err := hex.DecodeString(sig)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid signature", err.Error())
		return
	}

	txHash, err := s.node.MintWithSignature(&voucher, signature)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrMintInvalidSigner):
			writeError(w, http.StatusUnauthorized, req.ID, codeUnauthorized, err.Error(), nil)
		case errors.Is(err, core.ErrMintInvoiceUsed):
			writeError(w, http.StatusConflict, req.ID, codeDuplicateTx, err.Error(), voucher.InvoiceID)
		case errors.Is(err, core.ErrMintExpired), errors.Is(err, core.ErrMintInvalidChainID), errors.Is(err, core.ErrMintInvalidPayload):
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		case errors.Is(err, core.ErrMempoolFull):
			writeError(w, http.StatusServiceUnavailable, req.ID, codeMempoolFull, "mempool full", nil)
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "mint failed", err.Error())
		}
		return
	}

	writeResult(w, req.ID, map[string]string{"txHash": txHash})
}
