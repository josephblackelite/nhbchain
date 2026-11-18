package state

import (
	"math/big"
	"strconv"

	"nhbchain/native/market"
)

// Per-day rows of the market-wide fill record (ledger PL-BC-B2): for each UTC day with at least one peer-to-peer fill, the
// first, highest, lowest and last price, the volume in both assets and the number of fills. They let a read give a week or
// a month of range and volume with one small lookup per day instead of walking every fill, and they grow state by one small
// row per day, never per fill. The day comes from the fill's own timestamp, which is the block's header time (the engine
// stamps it from the block, never from a clock), so every validator buckets the same fill into the same day.
//
// Like the fill sequence they are written only from the pool's activation height (the caller decides), so before that the
// state is byte-for-byte what it was without them.

const (
	marketDayPrefix  = "market/fillDay/"
	marketDayLastKey = "market/fillDayLast"
	// MarketDaySeconds is the length of a day bucket.
	MarketDaySeconds = 86_400
	// marketDayMaxLookback bounds how many calendar days one read walks back, so a long quiet stretch cannot make a read
	// slow.
	marketDayMaxLookback = 400
)

// MarketDayRow is one UTC day of peer-to-peer fills. A price is NHB per ZNHB as the pair (NHB the seller received, ZNHB
// sold) of the fill it came from, so nothing is rounded in state.
type MarketDayRow struct {
	Day                   uint64 // unix seconds / 86400
	OpenNum, OpenDen      *big.Int
	HighNum, HighDen      *big.Int
	LowNum, LowDen        *big.Int
	CloseNum, CloseDen    *big.Int
	VolumeZnhb, VolumeNhb *big.Int
	Count                 uint64
}

func marketDayKey(day uint64) []byte { return []byte(marketDayPrefix + strconv.FormatUint(day, 10)) }

// cmpMarketPrice compares aNum/aDen with bNum/bDen without dividing.
func cmpMarketPrice(aNum, aDen, bNum, bDen *big.Int) int {
	return new(big.Int).Mul(aNum, bDen).Cmp(new(big.Int).Mul(bNum, aDen))
}

// recordMarketFillDay folds one fill into its day's row. A fill with no price (a zero amount) is not part of any range.
func (m *Manager) recordMarketFillDay(fill *market.Fill) error {
	if fill == nil || fill.CreatedAt < 0 || fill.ZNHBAmount == nil || fill.NHBAmount == nil || fill.ZNHBAmount.Sign() <= 0 || fill.NHBAmount.Sign() <= 0 {
		return nil
	}
	day := uint64(fill.CreatedAt) / MarketDaySeconds
	num, den := fill.NHBAmount, fill.ZNHBAmount
	var row MarketDayRow
	found, err := m.KVGet(marketDayKey(day), &row)
	if err != nil {
		return err
	}
	if !found {
		row = MarketDayRow{
			Day:     day,
			OpenNum: new(big.Int).Set(num), OpenDen: new(big.Int).Set(den),
			HighNum: new(big.Int).Set(num), HighDen: new(big.Int).Set(den),
			LowNum: new(big.Int).Set(num), LowDen: new(big.Int).Set(den),
			VolumeZnhb: new(big.Int), VolumeNhb: new(big.Int),
		}
	} else {
		if cmpMarketPrice(num, den, row.HighNum, row.HighDen) > 0 {
			row.HighNum, row.HighDen = new(big.Int).Set(num), new(big.Int).Set(den)
		}
		if cmpMarketPrice(num, den, row.LowNum, row.LowDen) < 0 {
			row.LowNum, row.LowDen = new(big.Int).Set(num), new(big.Int).Set(den)
		}
	}
	row.CloseNum, row.CloseDen = new(big.Int).Set(num), new(big.Int).Set(den)
	row.VolumeZnhb = new(big.Int).Add(row.VolumeZnhb, fill.ZNHBAmount)
	row.VolumeNhb = new(big.Int).Add(row.VolumeNhb, fill.NHBAmount)
	row.Count++
	if err := m.KVPut(marketDayKey(day), row); err != nil {
		return err
	}
	var last uint64
	lastFound, err := m.KVGet([]byte(marketDayLastKey), &last)
	if err != nil {
		return err
	}
	if !lastFound || day > last {
		return m.KVPut([]byte(marketDayLastKey), day)
	}
	return nil
}

// ListMarketDayRows returns the newest days that have fills first, at most days of them, walking back at most
// marketDayMaxLookback calendar days from the newest recorded day. Days without fills are skipped. It reads state and
// writes nothing.
func (m *Manager) ListMarketDayRows(days int) ([]*MarketDayRow, error) {
	if days <= 0 {
		return nil, nil
	}
	var last uint64
	found, err := m.KVGet([]byte(marketDayLastKey), &last)
	if err != nil || !found {
		return nil, err
	}
	rows := make([]*MarketDayRow, 0, days)
	day := last
	for looked := 0; looked < marketDayMaxLookback && len(rows) < days; looked++ {
		var row MarketDayRow
		ok, err := m.KVGet(marketDayKey(day), &row)
		if err != nil {
			return nil, err
		}
		if ok {
			rows = append(rows, &row)
		}
		if day == 0 {
			break
		}
		day--
	}
	return rows, nil
}
