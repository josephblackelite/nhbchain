package rpc

import (
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

// handleSwapVoucherReverse reverses a minted voucher and moves funds into the refund sink.
func (s *Server) handleSwapVoucherReverse(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected providerTxId", nil)
		return
	}
	var providerTxID string
	if err := json.Unmarshal(req.Params[0], &providerTxID); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid providerTxId", err.Error())
		return
	}
	trimmed := strings.TrimSpace(providerTxID)
	if trimmed == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "providerTxId required", nil)
		return
	}
	err := s.node.SwapReverseVoucher(trimmed)
	if err != nil {
		switch {
		case errors.Is(err, core.ErrSwapVoucherAlreadyReversed):
			writeResult(w, req.ID, map[string]bool{"ok": true})
			return
		case errors.Is(err, core.ErrSwapVoucherNotMinted):
			writeError(w, http.StatusConflict, req.ID, codeInvalidParams, err.Error(), nil)
			return
		case errors.Is(err, core.ErrSwapReversalInsufficientBalance):
			writeError(w, http.StatusConflict, req.ID, codeInvalidParams, err.Error(), nil)
			return
		default:
			if strings.Contains(err.Error(), "not found") {
				writeError(w, http.StatusNotFound, req.ID, codeInvalidParams, err.Error(), trimmed)
				return
			}
			writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to reverse voucher", err.Error())
			return
		}
	}
	writeResult(w, req.ID, map[string]bool{"ok": true})
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
