package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/core"
	"nhbchain/crypto"
	swap "nhbchain/native/swap"
)

func (s *Server) handleSwapSubmitVoucher(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected payload with voucher and sig", nil)
		return
	}
	var payload struct {
		Voucher      json.RawMessage `json:"voucher"`
		Sig          string          `json:"sig"`
		Provider     string          `json:"provider"`
		ProviderTxID string          `json:"providerTxId"`
		Username     string          `json:"username,omitempty"`
		Address      string          `json:"address,omitempty"`
		USDAmount    string          `json:"usdAmount,omitempty"`
		PriceProof   struct {
			Domain    string `json:"domain"`
			Provider  string `json:"provider"`
			Pair      string `json:"pair"`
			Rate      string `json:"rate"`
			Timestamp int64  `json:"timestamp"`
			Signature string `json:"signature"`
		} `json:"priceProof"`
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
	submission := &swap.VoucherSubmission{
		Voucher:      &voucher,
		Signature:    signature,
		Provider:     strings.TrimSpace(payload.Provider),
		ProviderTxID: strings.TrimSpace(payload.ProviderTxID),
		Username:     strings.TrimSpace(payload.Username),
		Address:      strings.TrimSpace(payload.Address),
		USDAmount:    strings.TrimSpace(payload.USDAmount),
	}
	proofSig := strings.TrimSpace(payload.PriceProof.Signature)
	if proofSig != "" {
		proofSig = strings.TrimPrefix(proofSig, "0x")
		priceProofSig, err := hex.DecodeString(proofSig)
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid priceProof.signature", err.Error())
			return
		}
		proof, err := swap.NewPriceProof(
			payload.PriceProof.Domain,
			payload.PriceProof.Provider,
			payload.PriceProof.Pair,
			payload.PriceProof.Rate,
			payload.PriceProof.Timestamp,
			priceProofSig,
		)
		if err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid priceProof", err.Error())
			return
		}
		submission.PriceProof = proof
	} else if strings.TrimSpace(payload.PriceProof.Domain) != "" || strings.TrimSpace(payload.PriceProof.Provider) != "" || strings.TrimSpace(payload.PriceProof.Pair) != "" || strings.TrimSpace(payload.PriceProof.Rate) != "" || payload.PriceProof.Timestamp != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "priceProof.signature required", nil)
		return
	}
	txHash, minted, err := s.node.SwapSubmitVoucher(submission)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrSwapInvalidSigner):
			writeError(w, http.StatusUnauthorized, req.ID, codeUnauthorized, err.Error(), nil)
		case errors.Is(err, core.ErrSwapProviderNotAllowed):
			writeError(w, http.StatusForbidden, req.ID, codeUnauthorized, err.Error(), nil)
		case errors.Is(err, core.ErrSwapSanctioned):
			writeError(w, http.StatusForbidden, req.ID, codeUnauthorized, err.Error(), nil)
		case errors.Is(err, core.ErrSwapAmountBelowMinimum),
			errors.Is(err, core.ErrSwapAmountAboveMaximum),
			errors.Is(err, core.ErrSwapDailyCapExceeded),
			errors.Is(err, core.ErrSwapMonthlyCapExceeded):
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		case errors.Is(err, core.ErrSwapVelocityExceeded):
			writeError(w, http.StatusTooManyRequests, req.ID, codeRateLimited, err.Error(), nil)
		case errors.Is(err, core.ErrSwapNonceUsed), errors.Is(err, core.ErrSwapDuplicateProviderTx):
			writeError(w, http.StatusConflict, req.ID, codeDuplicateTx, err.Error(), voucher.OrderID)
		case errors.Is(err, core.ErrSwapInvalidDomain),
			errors.Is(err, core.ErrSwapInvalidChainID),
			errors.Is(err, core.ErrSwapExpired),
			errors.Is(err, core.ErrSwapInvalidToken),
			errors.Is(err, core.ErrSwapInvalidSignature),
			errors.Is(err, core.ErrSwapPriceProofRequired),
			errors.Is(err, core.ErrSwapPriceProofInvalid),
			errors.Is(err, core.ErrSwapPriceProofSignerUnknown),
			errors.Is(err, core.ErrSwapPriceProofStale),
			errors.Is(err, core.ErrSwapPriceProofDeviation),
			errors.Is(err, core.ErrSwapMintPaused),
			errors.Is(err, core.ErrSwapUnsupportedFiat),
			errors.Is(err, core.ErrSwapOracleUnavailable),
			errors.Is(err, core.ErrSwapQuoteStale),
			errors.Is(err, core.ErrSwapSlippageExceeded):
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, err.Error(), nil)
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "swap voucher failed", err.Error())
		}
		return
	}
	writeResult(w, req.ID, map[string]interface{}{"txHash": txHash, "minted": minted})
}

func (s *Server) handleSwapVoucherGet(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected providerTxId", nil)
		return
	}
	var providerTxID string
	if err := json.Unmarshal(req.Params[0], &providerTxID); err != nil {
		var wrapper struct {
			ProviderTxID string `json:"providerTxId"`
		}
		if err := json.Unmarshal(req.Params[0], &wrapper); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid providerTxId", nil)
			return
		}
		providerTxID = wrapper.ProviderTxID
	}
	providerTxID = strings.TrimSpace(providerTxID)
	if providerTxID == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "providerTxId required", nil)
		return
	}
	record, ok, err := s.node.SwapGetVoucher(providerTxID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load voucher", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, "voucher not found", providerTxID)
		return
	}
	writeResult(w, req.ID, formatVoucherRecord(record))
}

func (s *Server) handleSwapVoucherList(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) < 2 || len(req.Params) > 4 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected startTs, endTs, [cursor], [limit]", nil)
		return
	}
	var startTs, endTs int64
	if err := json.Unmarshal(req.Params[0], &startTs); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid startTs", err.Error())
		return
	}
	if err := json.Unmarshal(req.Params[1], &endTs); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid endTs", err.Error())
		return
	}
	cursor := ""
	if len(req.Params) >= 3 {
		if err := json.Unmarshal(req.Params[2], &cursor); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid cursor", err.Error())
			return
		}
		cursor = strings.TrimSpace(cursor)
	}
	limit := 50
	if len(req.Params) == 4 {
		var limit64 int64
		if err := json.Unmarshal(req.Params[3], &limit64); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid limit", err.Error())
			return
		}
		if limit64 > 0 {
			limit = int(limit64)
		}
	}
	records, nextCursor, err := s.node.SwapListVouchers(startTs, endTs, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to list vouchers", err.Error())
		return
	}
	formatted := make([]map[string]interface{}, 0, len(records))
	for _, record := range records {
		formatted = append(formatted, formatVoucherRecord(record))
	}
	writeResult(w, req.ID, map[string]interface{}{"vouchers": formatted, "nextCursor": nextCursor})
}

func (s *Server) handleSwapVoucherExport(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 2 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected startTs and endTs", nil)
		return
	}
	var startTs, endTs int64
	if err := json.Unmarshal(req.Params[0], &startTs); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid startTs", err.Error())
		return
	}
	if err := json.Unmarshal(req.Params[1], &endTs); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid endTs", err.Error())
		return
	}
	csvBase64, count, total, err := s.node.SwapExportVouchers(startTs, endTs)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to export vouchers", err.Error())
		return
	}
	result := map[string]interface{}{
		"csvBase64":    csvBase64,
		"count":        count,
		"totalMintWei": total.String(),
	}
	writeResult(w, req.ID, result)
}

func formatVoucherRecord(record *swap.VoucherRecord) map[string]interface{} {
	if record == nil {
		return nil
	}
	response := map[string]interface{}{
		"provider":      record.Provider,
		"providerTxId":  record.ProviderTxID,
		"fiatCurrency":  record.FiatCurrency,
		"fiatAmount":    record.FiatAmount,
		"usd":           record.USD,
		"rate":          record.Rate,
		"token":         record.Token,
		"mintAmountWei": mintAmountToString(record.MintAmountWei),
		"username":      record.Username,
		"address":       record.Address,
		"quoteTs":       record.QuoteTimestamp,
		"source":        record.OracleSource,
		"priceProofId":  record.PriceProofID,
		"oracleMedian":  record.OracleMedian,
		"minterSig":     record.MinterSignature,
		"status":        record.Status,
		"createdAt":     record.CreatedAt,
	}
	if len(record.OracleFeeders) > 0 {
		response["oracleFeeders"] = append([]string{}, record.OracleFeeders...)
	}
	if strings.TrimSpace(record.PriceProofID) == "" {
		delete(response, "priceProofId")
	}
	if strings.TrimSpace(record.OracleMedian) == "" {
		delete(response, "oracleMedian")
	}
	if strings.TrimSpace(record.TwapRate) != "" {
		response["twapRate"] = record.TwapRate
	}
	response["twapObservations"] = record.TwapObservations
	response["twapWindowSeconds"] = record.TwapWindowSeconds
	if record.TwapStart > 0 {
		response["twapStart"] = record.TwapStart
	}
	if record.TwapEnd > 0 {
		response["twapEnd"] = record.TwapEnd
	}
	if record.Recipient != ([20]byte{}) {
		response["recipient"] = crypto.MustNewAddress(crypto.NHBPrefix, record.Recipient[:]).String()
	}
	return response
}

func mintAmountToString(amount *big.Int) string {
	if amount == nil {
		return "0"
	}
	return amount.String()
}
