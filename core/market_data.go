package core

import (
	"math/big"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/amm"
	"nhbchain/native/market"
)

// Read-only market data for the native pool (ledger PL-BC-B2): the ticker the wallet's market page shows. Nothing here
// takes part in consensus, writes anything or reads the machine's clock: "now" is the newest block's own timestamp, and
// a trade's time is the timestamp of the block it was included in. The pool's price is information only; nothing that
// prices anything on the chain reads it.

const (
	ammTickerWindowSeconds = 24 * 60 * 60
	// ammTickerScanCap bounds how many trades one call walks back; a window with more trades than this reports a
	// partial result and says so (Truncated).
	ammTickerScanCapDefault = 2000
)

// ammLadderSizesWhole are the sizes, in whole tokens of the asset paid in, the price-impact ladder quotes.
var ammLadderSizesWhole = []int64{1, 10, 100, 1000}

// AmmLadderRow is one quote of the price-impact ladder: what a swap of AmountIn would pay out now, the average price
// it gets (NHB per ZNHB), how far that is from the spot price in basis points, and whether the chain would accept the
// swap (inside the per-swap cap and the reserve floor).
type AmmLadderRow struct {
	Direction  uint8
	AmountIn   *big.Int
	AmountOut  *big.Int
	AvgPrice   string // NHB per ZNHB, 18 digits; "" when nothing would be paid out
	ImpactBps  uint64
	Executable bool
}

// AmmTickerView is the pool's market summary.
type AmmTickerView struct {
	Active          bool
	SwapsOpen       bool
	AsOfHeight      uint64
	AsOfTime        int64
	ReserveZnhbWei  *big.Int
	ReserveNhbWei   *big.Int
	PriceNhbPerZnhb string // spot, reserveNhb/reserveZnhb, 18 digits; "" for an empty pool
	FeeBps          uint32
	MaxSwapBps      uint32
	TradeCount      uint64

	LastTradePrice  string // NHB per ZNHB of the newest swap ever; "" when there has been none
	LastTradeHeight uint64
	LastTradeTime   int64

	// The last 24 hours, up to the newest block's timestamp.
	Open24h       string // price of the oldest swap inside the window
	High24h       string
	Low24h        string
	VolumeZnhb24h *big.Int
	VolumeNhb24h  *big.Int
	Trades24h     uint64
	Truncated     bool // the window held more than the scan cap; the 24 h numbers cover only the newest swaps
	Ladder        []AmmLadderRow
}

// blockTimeAt returns the timestamp of the block at height, caching what it loads for the rest of one call.
func (n *Node) blockTimeAt(cache map[uint64]int64, height uint64) (int64, bool) {
	if ts, ok := cache[height]; ok {
		return ts, true
	}
	block, err := n.GetBlockByHeight(height)
	if err != nil || block == nil || block.Header == nil {
		return 0, false
	}
	cache[height] = block.Header.Timestamp
	return block.Header.Timestamp, true
}

// ammTradePrice is the price a swap got, NHB per ZNHB, or nil when it has no defined price.
func ammTradePrice(dir uint8, in, out *big.Int) *big.Rat {
	if in == nil || out == nil || in.Sign() <= 0 || out.Sign() <= 0 {
		return nil
	}
	if dir == AmmDirZnhbToNhb {
		return new(big.Rat).SetFrac(out, in)
	}
	return new(big.Rat).SetFrac(in, out)
}

func ratString(r *big.Rat) string {
	if r == nil {
		return ""
	}
	return r.FloatString(18)
}

// AmmTrades reports the newest swaps first, each with the timestamp of its block (0 when the block cannot be read).
func (n *Node) AmmTrades(limit int) ([]AmmTradeView, error) {
	sp, err := n.readProcessor()
	if err != nil {
		return nil, err
	}
	trades, err := sp.AmmTrades(limit)
	if err != nil {
		return nil, err
	}
	cache := map[uint64]int64{}
	fillAmmTradeTimes(trades, func(h uint64) (int64, bool) { return n.blockTimeAt(cache, h) })
	return trades, nil
}

// fillAmmTradeTimes sets each swap's Time from the timestamp of its block; a swap whose block cannot be read keeps 0.
func fillAmmTradeTimes(trades []AmmTradeView, timeAt func(uint64) (int64, bool)) {
	for i := range trades {
		if ts, ok := timeAt(trades[i].Height); ok {
			trades[i].Time = ts
		}
	}
}

// AmmTicker reports the pool's spot price, last swap, 24 hour range and volume, and the price-impact ladder.
func (n *Node) AmmTicker() (*AmmTickerView, error) {
	sp, err := n.readProcessor()
	if err != nil {
		return nil, err
	}
	var asOfHeight uint64
	var asOfTime int64
	if chain := n.Chain(); chain != nil {
		if head := chain.CurrentHeader(); head != nil {
			asOfHeight, asOfTime = head.Height, head.Timestamp
		}
	}
	cache := map[uint64]int64{}
	return sp.ammTicker(asOfHeight, asOfTime, func(h uint64) (int64, bool) { return n.blockTimeAt(cache, h) })
}

// ammTicker builds the ticker from the processor's state. asOfTime is "now" (the newest block's timestamp) and timeAt
// gives the timestamp of the block at a height.
func (sp *StateProcessor) ammTicker(asOfHeight uint64, asOfTime int64, timeAt func(uint64) (int64, bool)) (*AmmTickerView, error) {
	pool, err := sp.AmmPool()
	if err != nil {
		return nil, err
	}
	view := &AmmTickerView{
		Active: sp.ammActive(), SwapsOpen: pool.SwapsOpen,
		ReserveZnhbWei: pool.ReserveZnhbWei, ReserveNhbWei: pool.ReserveNhbWei, PriceNhbPerZnhb: pool.PriceNhbPerZnhb,
		FeeBps: pool.FeeBps, MaxSwapBps: pool.MaxSwapBps, TradeCount: pool.TradeCount,
		VolumeZnhb24h: new(big.Int), VolumeNhb24h: new(big.Int),
	}
	view.AsOfHeight, view.AsOfTime = asOfHeight, asOfTime

	// Walk the swaps newest first until one falls out of the 24 hour window.
	windowStart := view.AsOfTime - ammTickerWindowSeconds
	var high, low, open *big.Rat
	scanned := 0
	for id := pool.TradeCount; id >= 1; id-- {
		t, found, err := sp.ammTradeAt(id)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		ts, known := timeAt(t.Height)
		if scanned >= ammTickerScanCap {
			// One more swap exists than the scan covers: the numbers are partial only if it is still inside the window.
			view.Truncated = known && ts >= windowStart
			break
		}
		scanned++
		price := ammTradePrice(t.Direction, t.AmountIn, t.AmountOut)
		if view.LastTradePrice == "" && price != nil {
			view.LastTradePrice, view.LastTradeHeight = ratString(price), t.Height
			if known {
				view.LastTradeTime = ts
			}
		}
		if !known || ts < windowStart {
			// Trades only get older from here; the first one outside the window ends the walk. A trade whose block
			// cannot be read ends it too, rather than guess a time for it.
			break
		}
		view.Trades24h++
		if t.Direction == AmmDirZnhbToNhb {
			view.VolumeZnhb24h.Add(view.VolumeZnhb24h, t.AmountIn)
			view.VolumeNhb24h.Add(view.VolumeNhb24h, t.AmountOut)
		} else {
			view.VolumeNhb24h.Add(view.VolumeNhb24h, t.AmountIn)
			view.VolumeZnhb24h.Add(view.VolumeZnhb24h, t.AmountOut)
		}
		if price != nil {
			if high == nil || price.Cmp(high) > 0 {
				high = price
			}
			if low == nil || price.Cmp(low) < 0 {
				low = price
			}
			open = price // the walk goes newest to oldest, so the last one kept is the oldest in the window
		}
	}
	view.High24h, view.Low24h, view.Open24h = ratString(high), ratString(low), ratString(open)

	// The price-impact ladder, from the same reserves a swap would see.
	if pool.ReserveZnhbWei != nil && pool.ReserveNhbWei != nil && pool.ReserveZnhbWei.Sign() > 0 && pool.ReserveNhbWei.Sign() > 0 {
		spot := new(big.Rat).SetFrac(pool.ReserveNhbWei, pool.ReserveZnhbWei)
		unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
		for _, dir := range []uint8{AmmDirZnhbToNhb, AmmDirNhbToZnhb} {
			reserveIn, reserveOut := pool.ReserveZnhbWei, pool.ReserveNhbWei
			minOut := sp.amm.MinReserveNHB
			if dir == AmmDirNhbToZnhb {
				reserveIn, reserveOut = pool.ReserveNhbWei, pool.ReserveZnhbWei
				minOut = sp.amm.MinReserveZNHB
			}
			limit := new(big.Int).Div(new(big.Int).Mul(reserveIn, big.NewInt(int64(sp.amm.MaxSwapBps))), big.NewInt(amm.FeeDenominator))
			for _, whole := range ammLadderSizesWhole {
				in := new(big.Int).Mul(big.NewInt(whole), unit)
				row := AmmLadderRow{Direction: dir, AmountIn: in, AmountOut: new(big.Int)}
				out, err := amm.AmountOut(reserveIn, reserveOut, in, sp.amm.FeeBps)
				if err == nil && out.Sign() > 0 {
					row.AmountOut = out
					if avg := ammTradePrice(dir, in, out); avg != nil {
						row.AvgPrice = ratString(avg)
						diff := new(big.Rat).Sub(avg, spot)
						diff.Abs(diff)
						bps := new(big.Rat).Quo(new(big.Rat).Mul(diff, big.NewRat(10_000, 1)), spot)
						if impact := new(big.Int).Quo(bps.Num(), bps.Denom()); impact.IsUint64() {
							row.ImpactBps = impact.Uint64()
						}
					}
					floorOK := minOut == nil || new(big.Int).Sub(reserveOut, out).Cmp(minOut) >= 0
					row.Executable = pool.SwapsOpen && in.Cmp(limit) <= 0 && floorOK
				}
				view.Ladder = append(view.Ladder, row)
			}
		}
	}
	return view, nil
}

// MarketTickerView is the peer-to-peer market's summary: what the open listings ask now and what the fills of the
// market-wide fill sequence show. Fills are in that sequence only from the pool's activation height (IndexStartHeight);
// before it nothing was recorded, so a market with no fills since then reports none rather than guessing.
type MarketTickerView struct {
	IndexActive      bool   // the fill sequence is being written (the pool's activation height has been reached)
	IndexStartHeight uint64 // 0 when the pool is not configured
	AsOfHeight       uint64
	AsOfTime         int64

	OpenListings uint64
	OpenZnhbWei  *big.Int
	BestAsk      string // NHB per ZNHB of the cheapest open listing; "" when none
	VWAPAsk      string // volume-weighted over every open listing, by the ZNHB still unsold

	FillCount      uint64 // fills in the sequence
	LastPrice      string // NHB per ZNHB the seller received in the newest fill; "" when none
	LastFillHeight uint64
	LastFillTime   int64
	Open24h        string
	High24h        string
	Low24h         string
	VolumeZnhb24h  *big.Int
	VolumeNhb24h   *big.Int
	Fills24h       uint64
	Truncated      bool
}

const marketTickerScanCapDefault = 2000

// marketTickerScanCap is a variable only so a test can make the cap small; nothing else changes it.
var marketTickerScanCap uint64 = marketTickerScanCapDefault

// IndexStartHeight is the height from which the market-wide fill sequence is written (0 = never).
func (n *Node) fillIndexStartHeight() uint64 {
	if n == nil {
		return 0
	}
	n.stateMu.Lock()
	defer n.stateMu.Unlock()
	return n.ammSettings.ActivationHeight
}

// MarketTicker summarises the open listings and the indexed fills. It reads state and writes nothing.
func (n *Node) MarketTicker() (*MarketTickerView, error) {
	view := &MarketTickerView{IndexStartHeight: n.fillIndexStartHeight(), OpenZnhbWei: new(big.Int), VolumeZnhb24h: new(big.Int), VolumeNhb24h: new(big.Int)}
	if chain := n.Chain(); chain != nil {
		if head := chain.CurrentHeader(); head != nil {
			view.AsOfHeight, view.AsOfTime = head.Height, head.Timestamp
		}
	}
	view.IndexActive = view.IndexStartHeight != 0 && view.AsOfHeight+1 >= view.IndexStartHeight
	windowStart := view.AsOfTime - ammTickerWindowSeconds
	if err := n.WithStateView(func(manager *nhbstate.Manager) error { return fillMarketTicker(manager, view, windowStart) }); err != nil {
		return nil, err
	}
	return view, nil
}

// fillMarketTicker adds the open listings' asks and the indexed fills' numbers to view, from the manager's state.
func fillMarketTicker(manager *nhbstate.Manager, view *MarketTickerView, windowStart int64) error {
	{
		open, err := manager.ListOpenMarketListings()
		if err != nil {
			return err
		}
		var bestAsk *big.Rat
		askValue := new(big.Rat) // sum of price * remaining ZNHB
		for _, listing := range open {
			if listing == nil || listing.RemainingAmount == nil || listing.RemainingAmount.Sign() <= 0 ||
				listing.RateNumerator == nil || listing.RateNumerator.Sign() <= 0 || listing.RateDenominator == nil || listing.RateDenominator.Sign() <= 0 {
				continue
			}
			view.OpenListings++
			view.OpenZnhbWei.Add(view.OpenZnhbWei, listing.RemainingAmount)
			// A listing's rate is ZNHB per NHB (the cost of a fill is ceil(znhb * denominator / numerator)), so the
			// price in NHB per ZNHB is denominator/numerator.
			price := new(big.Rat).SetFrac(listing.RateDenominator, listing.RateNumerator)
			if bestAsk == nil || price.Cmp(bestAsk) < 0 {
				bestAsk = price
			}
			askValue.Add(askValue, new(big.Rat).Mul(price, new(big.Rat).SetInt(listing.RemainingAmount)))
		}
		view.BestAsk = ratString(bestAsk)
		if view.OpenZnhbWei.Sign() > 0 {
			view.VWAPAsk = ratString(new(big.Rat).Quo(askValue, new(big.Rat).SetInt(view.OpenZnhbWei)))
		}

		count, err := manager.MarketFillSequence()
		if err != nil {
			return err
		}
		view.FillCount = count
		scan := count
		if scan > marketTickerScanCap {
			scan = marketTickerScanCap
		}
		fills, err := manager.ListRecentMarketFills(int(scan))
		if err != nil {
			return err
		}
		var high, low, openPrice *big.Rat
		leftWindow := false
		for _, fill := range fills {
			if fill == nil || fill.ZNHBAmount == nil || fill.NHBAmount == nil || fill.ZNHBAmount.Sign() <= 0 || fill.NHBAmount.Sign() <= 0 {
				continue
			}
			price := new(big.Rat).SetFrac(fill.NHBAmount, fill.ZNHBAmount)
			if view.LastPrice == "" {
				view.LastPrice, view.LastFillHeight, view.LastFillTime = ratString(price), fill.BlockHeight, fill.CreatedAt
			}
			if fill.CreatedAt < windowStart {
				// Fills only get older from here.
				leftWindow = true
				break
			}
			view.Fills24h++
			view.VolumeZnhb24h.Add(view.VolumeZnhb24h, fill.ZNHBAmount)
			view.VolumeNhb24h.Add(view.VolumeNhb24h, fill.NHBAmount)
			if high == nil || price.Cmp(high) > 0 {
				high = price
			}
			if low == nil || price.Cmp(low) < 0 {
				low = price
			}
			openPrice = price
		}
		view.High24h, view.Low24h, view.Open24h = ratString(high), ratString(low), ratString(openPrice)
		// More fills exist than were walked, and the walk never reached one outside the window: the 24 h numbers
		// cover only the newest fills.
		view.Truncated = count > scan && !leftWindow
		return nil
	}
}

// MarketRecentFills returns the newest fills of the market-wide fill sequence first, at most limit (and at most 200).
func (n *Node) MarketRecentFills(limit int) ([]*market.Fill, error) {
	if limit <= 0 {
		limit = 50
	} else if limit > 200 {
		limit = 200
	}
	var out []*market.Fill
	err := n.WithStateView(func(manager *nhbstate.Manager) error {
		fills, err := manager.ListRecentMarketFills(limit)
		out = fills
		return err
	})
	return out, err
}

// MarketDayView is one UTC day of peer-to-peer fills as the daily read reports it: the first, highest, lowest and last price
// (NHB per ZNHB, 18 digits), the volume in both assets and the number of fills.
type MarketDayView struct {
	DayStart   int64 // unix seconds of 00:00 UTC of the day
	Open       string
	High       string
	Low        string
	Close      string
	VolumeZnhb *big.Int
	VolumeNhb  *big.Int
	Fills      uint64
}

const (
	marketDailyDefaultDays = 7
	marketDailyMaxDays     = 90
)

func marketDayPrice(num, den *big.Int) string {
	if num == nil || den == nil || num.Sign() <= 0 || den.Sign() <= 0 {
		return ""
	}
	return new(big.Rat).SetFrac(num, den).FloatString(18)
}

// MarketDailyStats returns the newest days that have fills first (default 7, at most 90 days). It reads state and writes
// nothing.
func (n *Node) MarketDailyStats(days int) ([]MarketDayView, error) {
	if days <= 0 {
		days = marketDailyDefaultDays
	} else if days > marketDailyMaxDays {
		days = marketDailyMaxDays
	}
	var out []MarketDayView
	err := n.WithStateView(func(manager *nhbstate.Manager) error {
		rows, err := manager.ListMarketDayRows(days)
		if err != nil {
			return err
		}
		for _, row := range rows {
			out = append(out, MarketDayView{
				DayStart: int64(row.Day) * nhbstate.MarketDaySeconds,
				Open:     marketDayPrice(row.OpenNum, row.OpenDen), High: marketDayPrice(row.HighNum, row.HighDen),
				Low: marketDayPrice(row.LowNum, row.LowDen), Close: marketDayPrice(row.CloseNum, row.CloseDen),
				VolumeZnhb: row.VolumeZnhb, VolumeNhb: row.VolumeNhb, Fills: row.Count,
			})
		}
		return nil
	})
	return out, err
}

// ammTradeAt returns the swap with number id. It reads state and writes nothing.
func (sp *StateProcessor) ammTradeAt(id uint64) (AmmTradeView, bool, error) {
	manager := nhbstate.NewManager(sp.Trie)
	var t ammTradeRecord
	found, err := manager.KVGet(ammTradeKey(id), &t)
	if err != nil || !found {
		return AmmTradeView{}, false, err
	}
	return AmmTradeView{ID: id, Height: t.Height, Trader: ammAddrString(t.Trader), Direction: t.Dir, AmountIn: t.In, AmountOut: t.Out, FeeWei: t.Fee}, true, nil
}

// ammTickerScanCap is a variable only so a test can make the cap small; nothing else changes it.
var ammTickerScanCap = ammTickerScanCapDefault
