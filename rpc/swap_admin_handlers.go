package rpc

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"

	"nhbchain/core"
	"nhbchain/crypto"
	swap "nhbchain/native/swap"
)

// handleSwapLimits returns the current usage counters and remaining capacity for a swap participant.
func (s *Server) handleSwapLimits(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected address", nil)
		return
	}
	var addrStr string
	if err := json.Unmarshal(req.Params[0], &addrStr); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	decoded, err := crypto.DecodeAddress(strings.TrimSpace(addrStr))
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	var addr [20]byte
	copy(addr[:], decoded.Bytes())

	usage, params, err := s.node.SwapLimits(addr)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load limits", err.Error())
		return
	}
	dayMinted := big.NewInt(0)
	if usage.DayTotalWei != nil {
		dayMinted = new(big.Int).Set(usage.DayTotalWei)
	}
	monthMinted := big.NewInt(0)
	if usage.MonthTotalWei != nil {
		monthMinted = new(big.Int).Set(usage.MonthTotalWei)
	}
	var dayRemaining *big.Int
	if params.PerAddressDailyCapWei != nil && params.PerAddressDailyCapWei.Sign() > 0 {
		dayRemaining = new(big.Int).Sub(params.PerAddressDailyCapWei, dayMinted)
		if dayRemaining.Sign() < 0 {
			dayRemaining = big.NewInt(0)
		}
	}
	var monthRemaining *big.Int
	if params.PerAddressMonthlyCapWei != nil && params.PerAddressMonthlyCapWei.Sign() > 0 {
		monthRemaining = new(big.Int).Sub(params.PerAddressMonthlyCapWei, monthMinted)
		if monthRemaining.Sign() < 0 {
			monthRemaining = big.NewInt(0)
		}
	}
	velocityObserved := 0
	velocityRemaining := int64(-1)
	if params.VelocityWindowSeconds > 0 && params.VelocityMaxMints > 0 {
		cutoff := time.Now().Add(-time.Duration(params.VelocityWindowSeconds) * time.Second).Unix()
		for _, sample := range usage.VelocityTimestamps {
			if sample >= cutoff {
				velocityObserved++
			}
		}
		remaining := int64(params.VelocityMaxMints) - int64(velocityObserved)
		if remaining < 0 {
			remaining = 0
		}
		velocityRemaining = remaining
	}
	result := map[string]interface{}{
		"address": decoded.String(),
		"day": map[string]string{
			"bucket":    usage.Day,
			"mintedWei": dayMinted.String(),
		},
		"month": map[string]string{
			"bucket":    usage.Month,
			"mintedWei": monthMinted.String(),
		},
	}
	if dayRemaining != nil {
		result["dayRemainingWei"] = dayRemaining.String()
	}
	if monthRemaining != nil {
		result["monthRemainingWei"] = monthRemaining.String()
	}
	if params.VelocityWindowSeconds > 0 && params.VelocityMaxMints > 0 {
		velocityInfo := map[string]interface{}{
			"windowSeconds": params.VelocityWindowSeconds,
			"maxMints":      params.VelocityMaxMints,
			"observed":      velocityObserved,
		}
		if velocityRemaining >= 0 {
			velocityInfo["remaining"] = velocityRemaining
		}
		result["velocity"] = velocityInfo
	}
	writeResult(w, req.ID, result)
}

// handleSwapProviderStatus returns the configured provider allow list and oracle health metadata.
func (s *Server) handleSwapProviderStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	status := s.node.SwapProviderStatus()
	result := map[string]interface{}{
		"allow":                 status.Allow,
		"lastOracleHealthCheck": status.LastOracleHealthCheck,
	}
	if len(status.OracleFeeds) > 0 {
		result["oracleFeeds"] = status.OracleFeeds
	}
	writeResult(w, req.ID, result)
}

func (s *Server) handleSwapBurnList(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
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
	receipts, nextCursor, err := s.node.SwapListBurnReceipts(startTs, endTs, cursor, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to list burn receipts", err.Error())
		return
	}
	formatted := make([]map[string]interface{}, 0, len(receipts))
	for _, receipt := range receipts {
		formatted = append(formatted, formatBurnReceipt(receipt))
	}
	writeResult(w, req.ID, map[string]interface{}{"receipts": formatted, "nextCursor": nextCursor})
}

// swapManualQuoteParams captures the payload accepted by swap_setManualQuote.
type swapManualQuoteParams struct {
	Base      string `json:"base"`
	Quote     string `json:"quote"`
	Rate      string `json:"rate"`
	Timestamp *int64 `json:"timestamp,omitempty"`
}

// handleSwapSetManualQuote publishes a manual override rate for the supplied
// currency pair. The manual oracle tier is the lowest-priority circuit
// breaker used during incidents (see docs/treasury/peg-policy.md); without
// this endpoint the seed quote set at process startup goes stale after
// MaxQuoteAgeSeconds with no way to refresh it short of restarting the node.
func (s *Server) handleSwapSetManualQuote(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected base, quote, and rate payload", nil)
		return
	}
	var params swapManualQuoteParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid payload", err.Error())
		return
	}
	base := strings.TrimSpace(params.Base)
	quote := strings.TrimSpace(params.Quote)
	rate := strings.TrimSpace(params.Rate)
	if base == "" || quote == "" || rate == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "base, quote, and rate required", nil)
		return
	}
	ts := time.Now().UTC()
	if params.Timestamp != nil {
		if *params.Timestamp < 0 {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "timestamp must be non-negative", nil)
			return
		}
		ts = time.Unix(*params.Timestamp, 0).UTC()
	}
	if err := s.node.SetSwapManualQuote(base, quote, rate, ts); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "failed to set manual quote", err.Error())
		return
	}
	writeResult(w, req.ID, map[string]any{
		"ok":         true,
		"base":       strings.ToUpper(base),
		"quote":      strings.ToUpper(quote),
		"rate":       rate,
		"observedAt": ts.Format(time.RFC3339),
	})
}

// swapVoucherReverseParams captures the payload accepted by
// swap_voucher_reverse. NHB-AUDIT-C4 follow-up: this handler no longer
// mutates state itself -- signature must be a hex-encoded 65-byte
// secp256k1 signature, produced off-chain by an operator key holding
// on-chain RoleSwapAdmin, over core/swap_admin_tx.go's
// SwapVoucherReverseSigningHash(providerTxId). This handler only wraps
// that caller-supplied signature into a real TxTypeSwapVoucherReverse
// transaction and submits it via Node.SwapReverseVoucher -> AddTransaction,
// exactly mirroring swap_submitVoucher's own "accept an already-signed
// payload, never sign anything itself" contract (see
// core.Node.SwapSubmitVoucher's doc comment). The real authorization check
// now lives in applySwapVoucherReverseTransaction, enforced identically by
// every validator; requireAuthInto below remains defense-in-depth so only
// already-trusted callers can even reach this endpoint.
type swapVoucherReverseParams struct {
	ProviderTxID string `json:"providerTxId"`
	Signature    string `json:"signature"`
}

// handleSwapVoucherReverse submits a signed reversal of a minted voucher --
// see swapVoucherReverseParams's doc comment.
func (s *Server) handleSwapVoucherReverse(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected {providerTxId, signature}", nil)
		return
	}
	var params swapVoucherReverseParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid payload", err.Error())
		return
	}
	trimmed := strings.TrimSpace(params.ProviderTxID)
	if trimmed == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "providerTxId required", nil)
		return
	}
	signature, sigErr := decodeSwapAdminSignatureParam(params.Signature)
	if sigErr != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, sigErr.Error(), nil)
		return
	}
	txHash, err := s.node.SwapReverseVoucher(trimmed, signature)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrSwapVoucherAlreadyReversed):
			writeResult(w, req.ID, map[string]bool{"ok": true})
			return
		case errors.Is(err, core.ErrSwapAdminUnauthorized):
			writeError(w, http.StatusForbidden, req.ID, codeUnauthorized, err.Error(), nil)
			return
		case errors.Is(err, core.ErrSwapVoucherNotMinted),
			errors.Is(err, core.ErrSwapReversalInsufficientBalance):
			writeError(w, http.StatusConflict, req.ID, codeInvalidParams, err.Error(), nil)
			return
		case errors.Is(err, core.ErrSwapVoucherReversalNotFound):
			writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, err.Error(), trimmed)
			return
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to reverse voucher", err.Error())
			return
		}
	}
	writeResult(w, req.ID, map[string]any{"ok": true, "txHash": txHash})
}

// swapMarkReconciledParams captures the payload accepted by
// swap_markReconciled -- mirrors swapVoucherReverseParams's contract; see
// its doc comment. signature must cover
// SwapMarkReconciledSigningHash(providerTxIds) over the exact trimmed,
// blank-filtered slice Node.SwapMarkReconciled derives from providerTxIds
// below (whitespace trimmed, blanks removed, original order preserved).
type swapMarkReconciledParams struct {
	ProviderTxIDs []string `json:"providerTxIds"`
	Signature     string   `json:"signature"`
}

// handleSwapMarkReconciled submits a signed batch marking the supplied
// vouchers as reconciled against treasury records -- see
// swapMarkReconciledParams's doc comment.
func (s *Server) handleSwapMarkReconciled(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected {providerTxIds, signature}", nil)
		return
	}
	var params swapMarkReconciledParams
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid payload", err.Error())
		return
	}
	if len(params.ProviderTxIDs) == 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "providerTxIds required", nil)
		return
	}
	signature, sigErr := decodeSwapAdminSignatureParam(params.Signature)
	if sigErr != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, sigErr.Error(), nil)
		return
	}
	txHash, err := s.node.SwapMarkReconciled(params.ProviderTxIDs, signature)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrSwapAdminUnauthorized):
			writeError(w, http.StatusForbidden, req.ID, codeUnauthorized, err.Error(), nil)
			return
		default:
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to mark vouchers reconciled", err.Error())
			return
		}
	}
	writeResult(w, req.ID, map[string]any{"ok": true, "txHash": txHash})
}

// decodeSwapAdminSignatureParam decodes an RPC-supplied hex signature
// string (with or without a "0x" prefix). It intentionally leaves length
// validation (must be exactly 65 bytes) to
// core/swap_admin_tx.go's decodeSwapAdminSignature, which
// applySwapVoucherReverseTransaction/applySwapMarkReconciledTransaction
// consult identically on every validator -- this handler-level decode only
// needs to produce well-formed bytes to embed in the transaction payload.
func decodeSwapAdminSignatureParam(raw string) ([]byte, error) {
	sigHex := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(raw)), "0x")
	if sigHex == "" {
		return nil, errors.New("signature required")
	}
	signature, err := hex.DecodeString(sigHex)
	if err != nil {
		return nil, errors.New("invalid signature")
	}
	return signature, nil
}

func formatBurnReceipt(receipt *swap.BurnReceipt) map[string]interface{} {
	if receipt == nil {
		return nil
	}
	payload := map[string]interface{}{
		"receiptId":    receipt.ReceiptID,
		"providerTxId": receipt.ProviderTxID,
		"token":        receipt.Token,
		"amountWei":    mintAmountToString(receipt.AmountWei),
		"observedAt":   receipt.ObservedAt,
	}
	if receipt.Burner != ([20]byte{}) {
		payload["burner"] = crypto.MustNewAddress(crypto.NHBPrefix, receipt.Burner[:]).String()
	}
	if strings.TrimSpace(receipt.RedeemReference) != "" {
		payload["redeemRef"] = strings.TrimSpace(receipt.RedeemReference)
	}
	if strings.TrimSpace(receipt.BurnTxHash) != "" {
		payload["burnTx"] = strings.TrimSpace(receipt.BurnTxHash)
	}
	if strings.TrimSpace(receipt.TreasuryTxID) != "" {
		payload["treasuryTx"] = strings.TrimSpace(receipt.TreasuryTxID)
	}
	if len(receipt.VoucherIDs) > 0 {
		payload["voucherIds"] = append([]string{}, receipt.VoucherIDs...)
	}
	if strings.TrimSpace(receipt.Notes) != "" {
		payload["notes"] = strings.TrimSpace(receipt.Notes)
	}
	return payload
}
