package rpc

import (
	"encoding/json"
	"math/big"
	"net/http"
	"strings"

	"nhbchain/core"
)

// Read-only views of the ZNHB bridge escrow and the native ZNHB/NHB pool (ledger PL-BC-C, PL-BC-B). Anonymous, like
// gov_params and the other chain reads: they report what the chain holds and change nothing. The pool's price is
// information only; nothing on the chain reads it.

type bridgeStatusResult struct {
	Active           bool   `json:"active"`
	ActivationHeight uint64 `json:"activationHeight"`
	EscrowAddress    string `json:"escrowAddress"`
	EscrowBalanceWei string `json:"escrowBalanceWei"`
	LockedWei        string `json:"lockedWei"`
	ReleasedWei      string `json:"releasedWei"`
	OutstandingWei   string `json:"outstandingWei"`
	NextLockID       uint64 `json:"nextLockId"`
	MaxLockWei       string `json:"maxLockWei"`

	// Escrow coverage and the limits that bound a loss (Stage 1 of the bridge trust plan).
	EscrowCovered bool     `json:"escrowCovered"`
	Paused        bool     `json:"paused"`
	PendingWei    string   `json:"pendingWei"`
	Signers       []string `json:"signers"`
	Threshold     uint32   `json:"threshold"`
	Guardian      string   `json:"guardian"`
	MaxReleaseWei string   `json:"maxReleaseWei"`
	WindowBlocks  uint64   `json:"windowBlocks"`
	WindowCapWei  string   `json:"windowCapWei"`
	WindowUsedWei string   `json:"windowUsedWei"`
	DelayAboveWei string   `json:"delayAboveWei"`
	DelayBlocks   uint64   `json:"delayBlocks"`
	NextRotation  uint64   `json:"nextRotation"`
}

func (s *Server) handleBridgeStatus(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	view, err := s.node.BridgeStatus()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load the bridge status", err.Error())
		return
	}
	signers := view.Signers
	if signers == nil {
		signers = []string{}
	}
	delayAbove := ""
	if view.DelayAboveWei != nil {
		delayAbove = view.DelayAboveWei.String()
	}
	writeResult(w, req.ID, bridgeStatusResult{
		Active: view.Active, ActivationHeight: view.ActivationHeight, EscrowAddress: view.EscrowAddress,
		EscrowBalanceWei: weiOrZero(view.EscrowBalanceWei), LockedWei: weiOrZero(view.LockedWei), ReleasedWei: weiOrZero(view.ReleasedWei),
		OutstandingWei: weiOrZero(view.OutstandingWei), NextLockID: view.NextLockID, MaxLockWei: weiOrZero(view.MaxLockWei),
		EscrowCovered: view.EscrowCovered, Paused: view.Paused, PendingWei: weiOrZero(view.PendingWei), Signers: signers,
		Threshold: view.Threshold, Guardian: view.Guardian, MaxReleaseWei: weiOrZero(view.MaxReleaseWei), WindowBlocks: view.WindowBlocks,
		WindowCapWei: weiOrZero(view.WindowCapWei), WindowUsedWei: weiOrZero(view.WindowUsedWei), DelayAboveWei: delayAbove,
		DelayBlocks: view.DelayBlocks, NextRotation: view.NextRotation,
	})
}

type ammPoolResult struct {
	Active          bool   `json:"active"`
	SwapsOpen       bool   `json:"swapsOpen"`
	PoolAddress     string `json:"poolAddress"`
	ReserveZnhbWei  string `json:"reserveZnhbWei"`
	ReserveNhbWei   string `json:"reserveNhbWei"`
	TotalShares     string `json:"totalShares"`
	FeeBps          uint32 `json:"feeBps"`
	MaxSwapBps      uint32 `json:"maxSwapBps"`
	TradeCount      uint64 `json:"tradeCount"`
	PriceNhbPerZnhb string `json:"priceNhbPerZnhb"`
}

func (s *Server) handleAmmPool(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	view, err := s.node.AmmPool()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load the pool", err.Error())
		return
	}
	writeResult(w, req.ID, ammPoolResult{
		Active: view.Active, SwapsOpen: view.SwapsOpen, PoolAddress: view.PoolAddress,
		ReserveZnhbWei: weiOrZero(view.ReserveZnhbWei), ReserveNhbWei: weiOrZero(view.ReserveNhbWei), TotalShares: weiOrZero(view.TotalShares),
		FeeBps: view.FeeBps, MaxSwapBps: view.MaxSwapBps, TradeCount: view.TradeCount, PriceNhbPerZnhb: view.PriceNhbPerZnhb,
	})
}

// handleAmmQuote answers amm_quote: params [direction, amountInWei] with direction "znhb_to_nhb" or "nhb_to_znhb".
func (s *Server) handleAmmQuote(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	params := req.Params
	if len(params) != 2 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected [direction, amountInWei]", nil)
		return
	}
	var direction, amount string
	if json.Unmarshal(params[0], &direction) != nil || json.Unmarshal(params[1], &amount) != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "direction and amountInWei must be strings", nil)
		return
	}
	var dir uint8
	switch strings.ToLower(strings.TrimSpace(direction)) {
	case "znhb_to_nhb":
		dir = core.AmmDirZnhbToNhb
	case "nhb_to_znhb":
		dir = core.AmmDirNhbToZnhb
	default:
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "direction must be znhb_to_nhb or nhb_to_znhb", nil)
		return
	}
	in, ok := new(big.Int).SetString(strings.TrimSpace(amount), 10)
	if !ok || in.Sign() <= 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "amountInWei must be a positive decimal integer", nil)
		return
	}
	out, err := s.node.AmmQuote(dir, in)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no quote: "+err.Error(), nil)
		return
	}
	writeResult(w, req.ID, map[string]string{"direction": strings.ToLower(strings.TrimSpace(direction)), "amountInWei": in.String(), "amountOutWei": out.String()})
}

type ammTradeResult struct {
	ID        uint64 `json:"id"`
	Height    uint64 `json:"height"`
	Trader    string `json:"trader"`
	Direction string `json:"direction"`
	AmountIn  string `json:"amountInWei"`
	AmountOut string `json:"amountOutWei"`
	FeeWei    string `json:"feeWei"`
	// Time is the timestamp of the block the swap was included in (0 when it cannot be read).
	Time int64 `json:"time"`
}

// handleAmmTrades answers amm_getTrades: params [] or [limit], newest first, at most 200.
func (s *Server) handleAmmTrades(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	limit := 50
	if len(req.Params) > 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected [] or [limit]", nil)
		return
	}
	if len(req.Params) == 1 {
		var requested int
		if err := json.Unmarshal(req.Params[0], &requested); err != nil || requested <= 0 {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "limit must be a positive integer", nil)
			return
		}
		limit = requested
	}
	trades, err := s.node.AmmTrades(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load the trades", err.Error())
		return
	}
	out := make([]ammTradeResult, 0, len(trades))
	for _, t := range trades {
		direction := "znhb_to_nhb"
		if t.Direction == core.AmmDirNhbToZnhb {
			direction = "nhb_to_znhb"
		}
		out = append(out, ammTradeResult{ID: t.ID, Height: t.Height, Trader: t.Trader, Direction: direction,
			AmountIn: weiOrZero(t.AmountIn), AmountOut: weiOrZero(t.AmountOut), FeeWei: weiOrZero(t.FeeWei), Time: t.Time})
	}
	writeResult(w, req.ID, out)
}
