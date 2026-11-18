package rpc

import (
	"encoding/json"
	"net/http"

	"nhbchain/core"
)

// Read-only market data for the wallet's market page (ledger PL-BC-B2): the native pool's ticker and the peer-to-peer
// market's ticker and recent fills. Anonymous, like the other market reads. They report what the chain holds and change
// nothing; the prices they show are information only and nothing on the chain reads them.

type ammLadderResult struct {
	Direction  string `json:"direction"`
	AmountIn   string `json:"amountInWei"`
	AmountOut  string `json:"amountOutWei"`
	AvgPrice   string `json:"avgPriceNhbPerZnhb"`
	ImpactBps  uint64 `json:"impactBps"`
	Executable bool   `json:"executable"`
}

type ammTickerResult struct {
	Active          bool   `json:"active"`
	SwapsOpen       bool   `json:"swapsOpen"`
	AsOfHeight      uint64 `json:"asOfHeight"`
	AsOfTime        int64  `json:"asOfTime"`
	ReserveZnhbWei  string `json:"reserveZnhbWei"`
	ReserveNhbWei   string `json:"reserveNhbWei"`
	PriceNhbPerZnhb string `json:"priceNhbPerZnhb"`
	FeeBps          uint32 `json:"feeBps"`
	MaxSwapBps      uint32 `json:"maxSwapBps"`
	TradeCount      uint64 `json:"tradeCount"`

	LastTradePrice  string `json:"lastTradePriceNhbPerZnhb"`
	LastTradeHeight uint64 `json:"lastTradeHeight"`
	LastTradeTime   int64  `json:"lastTradeTime"`

	Open24h       string `json:"open24hNhbPerZnhb"`
	High24h       string `json:"high24hNhbPerZnhb"`
	Low24h        string `json:"low24hNhbPerZnhb"`
	VolumeZnhb24h string `json:"volumeZnhb24hWei"`
	VolumeNhb24h  string `json:"volumeNhb24hWei"`
	Trades24h     uint64 `json:"trades24h"`
	Truncated     bool   `json:"truncated"`

	Ladder []ammLadderResult `json:"ladder"`
}

func ammDirectionName(dir uint8) string {
	if dir == core.AmmDirNhbToZnhb {
		return "nhb_to_znhb"
	}
	return "znhb_to_nhb"
}

// handleAmmTicker answers amm_getTicker: no parameters.
func (s *Server) handleAmmTicker(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	if !s.admitHeavy(w, r, req) {
		return
	}
	view, err := s.node.AmmTicker()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load the pool ticker", err.Error())
		return
	}
	ladder := make([]ammLadderResult, 0, len(view.Ladder))
	for _, row := range view.Ladder {
		ladder = append(ladder, ammLadderResult{Direction: ammDirectionName(row.Direction), AmountIn: weiOrZero(row.AmountIn), AmountOut: weiOrZero(row.AmountOut),
			AvgPrice: row.AvgPrice, ImpactBps: row.ImpactBps, Executable: row.Executable})
	}
	writeResult(w, req.ID, ammTickerResult{
		Active: view.Active, SwapsOpen: view.SwapsOpen, AsOfHeight: view.AsOfHeight, AsOfTime: view.AsOfTime,
		ReserveZnhbWei: weiOrZero(view.ReserveZnhbWei), ReserveNhbWei: weiOrZero(view.ReserveNhbWei), PriceNhbPerZnhb: view.PriceNhbPerZnhb,
		FeeBps: view.FeeBps, MaxSwapBps: view.MaxSwapBps, TradeCount: view.TradeCount,
		LastTradePrice: view.LastTradePrice, LastTradeHeight: view.LastTradeHeight, LastTradeTime: view.LastTradeTime,
		Open24h: view.Open24h, High24h: view.High24h, Low24h: view.Low24h,
		VolumeZnhb24h: weiOrZero(view.VolumeZnhb24h), VolumeNhb24h: weiOrZero(view.VolumeNhb24h), Trades24h: view.Trades24h, Truncated: view.Truncated,
		Ladder: ladder,
	})
}

type marketTickerResult struct {
	IndexActive      bool   `json:"indexActive"`
	IndexStartHeight uint64 `json:"indexStartHeight"`
	AsOfHeight       uint64 `json:"asOfHeight"`
	AsOfTime         int64  `json:"asOfTime"`

	OpenListings uint64 `json:"openListings"`
	OpenZnhbWei  string `json:"openZnhbWei"`
	BestAsk      string `json:"bestAskNhbPerZnhb"`
	VWAPAsk      string `json:"vwapAskNhbPerZnhb"`

	FillCount      uint64 `json:"fillCount"`
	LastPrice      string `json:"lastPriceNhbPerZnhb"`
	LastFillHeight uint64 `json:"lastFillHeight"`
	LastFillTime   int64  `json:"lastFillTime"`
	Open24h        string `json:"open24hNhbPerZnhb"`
	High24h        string `json:"high24hNhbPerZnhb"`
	Low24h         string `json:"low24hNhbPerZnhb"`
	VolumeZnhb24h  string `json:"volumeZnhb24hWei"`
	VolumeNhb24h   string `json:"volumeNhb24hWei"`
	Fills24h       uint64 `json:"fills24h"`
	Truncated      bool   `json:"truncated"`
}

// handleMarketTicker answers market_getTicker: no parameters.
func (s *Server) handleMarketTicker(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	if len(req.Params) != 0 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "no parameters expected", nil)
		return
	}
	if !s.admitHeavy(w, r, req) {
		return
	}
	view, err := s.node.MarketTicker()
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load the market ticker", err.Error())
		return
	}
	writeResult(w, req.ID, marketTickerResult{
		IndexActive: view.IndexActive, IndexStartHeight: view.IndexStartHeight, AsOfHeight: view.AsOfHeight, AsOfTime: view.AsOfTime,
		OpenListings: view.OpenListings, OpenZnhbWei: weiOrZero(view.OpenZnhbWei), BestAsk: view.BestAsk, VWAPAsk: view.VWAPAsk,
		FillCount: view.FillCount, LastPrice: view.LastPrice, LastFillHeight: view.LastFillHeight, LastFillTime: view.LastFillTime,
		Open24h: view.Open24h, High24h: view.High24h, Low24h: view.Low24h,
		VolumeZnhb24h: weiOrZero(view.VolumeZnhb24h), VolumeNhb24h: weiOrZero(view.VolumeNhb24h), Fills24h: view.Fills24h, Truncated: view.Truncated,
	})
}

// handleMarketRecentFills answers market_getRecentFills: params [] or [limit], newest first, at most 200.
func (s *Server) handleMarketRecentFills(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
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
	if !s.admitHeavy(w, r, req) {
		return
	}
	fills, err := s.node.MarketRecentFills(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load the recent fills", err.Error())
		return
	}
	out := make([]*marketFillResult, 0, len(fills))
	for _, fill := range fills {
		out = append(out, newMarketFillResult(fill))
	}
	writeResult(w, req.ID, map[string]interface{}{"fills": out})
}

type marketDayResult struct {
	DayStart      int64  `json:"dayStart"`
	Open          string `json:"openNhbPerZnhb"`
	High          string `json:"highNhbPerZnhb"`
	Low           string `json:"lowNhbPerZnhb"`
	Close         string `json:"closeNhbPerZnhb"`
	VolumeZnhbWei string `json:"volumeZnhbWei"`
	VolumeNhbWei  string `json:"volumeNhbWei"`
	Fills         uint64 `json:"fills"`
}

// handleMarketDailyStats answers market_getDailyStats: params [] or [days] (default 7, at most 90), newest day first, only
// days that have fills. The rows come from the market-wide fill record, so they start at the pool's activation block.
func (s *Server) handleMarketDailyStats(w http.ResponseWriter, r *http.Request, req *RPCRequest) {
	days := 0
	if len(req.Params) > 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected [] or [days]", nil)
		return
	}
	if len(req.Params) == 1 {
		var requested int
		if err := json.Unmarshal(req.Params[0], &requested); err != nil || requested <= 0 {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "days must be a positive integer", nil)
			return
		}
		days = requested
	}
	if !s.admitHeavy(w, r, req) {
		return
	}
	rows, err := s.node.MarketDailyStats(days)
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load the daily stats", err.Error())
		return
	}
	out := make([]marketDayResult, 0, len(rows))
	for _, row := range rows {
		out = append(out, marketDayResult{DayStart: row.DayStart, Open: row.Open, High: row.High, Low: row.Low, Close: row.Close,
			VolumeZnhbWei: weiOrZero(row.VolumeZnhb), VolumeNhbWei: weiOrZero(row.VolumeNhb), Fills: row.Fills})
	}
	writeResult(w, req.ID, map[string]interface{}{"days": out})
}
