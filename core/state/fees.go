package state

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"nhbchain/native/fees"
)

type storedFeeCounter struct {
	Count           uint64
	WindowStartUnix uint64
}

type storedFeeTotals struct {
	Domain string
	Asset  string
	Wallet [20]byte
	Gross  *big.Int
	Fee    *big.Int
	Net    *big.Int
}

func monthStartUTC(ts time.Time) time.Time {
	if ts.IsZero() {
		return time.Time{}
	}
	utc := ts.UTC()
	return time.Date(utc.Year(), utc.Month(), 1, 0, 0, 0, 0, time.UTC)
}

func monthKey(ts time.Time) string {
	start := monthStartUTC(ts)
	if start.IsZero() {
		return "000000"
	}
	return fmt.Sprintf("%04d%02d", start.Year(), int(start.Month()))
}

func feeCounterKey(domain string, window time.Time, scope string, payer [20]byte) []byte {
	normalized := fees.NormalizeDomain(domain)
	month := monthKey(window)
	normalizedScope := fees.NormalizeFreeTierScope(scope)
	hexAddr := hex.EncodeToString(payer[:])
	buf := make([]byte, len(feesCounterPrefix)+len(normalized)+1+len(month)+1+len(normalizedScope)+1+len(hexAddr))
	copy(buf, feesCounterPrefix)
	offset := len(feesCounterPrefix)
	copy(buf[offset:], normalized)
	offset += len(normalized)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], month)
	offset += len(month)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], normalizedScope)
	offset += len(normalizedScope)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], hexAddr)
	return buf
}

func feeCounterLegacyKey(domain string, payer [20]byte) []byte {
	normalized := fees.NormalizeDomain(domain)
	hexAddr := hex.EncodeToString(payer[:])
	buf := make([]byte, len(feesCounterPrefix)+len(normalized)+1+len(hexAddr))
	copy(buf, feesCounterPrefix)
	offset := len(feesCounterPrefix)
	copy(buf[offset:], normalized)
	offset += len(normalized)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], hexAddr)
	return buf
}

func sameCounterMonth(a, b time.Time) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	ua := a.UTC()
	ub := b.UTC()
	return ua.Year() == ub.Year() && ua.Month() == ub.Month()
}

func feeTotalsKey(domain, asset string, wallet [20]byte) []byte {
	normalized := fees.NormalizeDomain(domain)
	normalizedAsset := fees.NormalizeAsset(asset)
	hexAddr := hex.EncodeToString(wallet[:])
	buf := make([]byte, len(feesTotalsPrefix)+len(normalized)+1+len(normalizedAsset)+1+len(hexAddr))
	copy(buf, feesTotalsPrefix)
	offset := len(feesTotalsPrefix)
	copy(buf[offset:], normalized)
	offset += len(normalized)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], normalizedAsset)
	offset += len(normalizedAsset)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], hexAddr)
	return buf
}

func feeTotalsIndexKey(domain string) []byte {
	normalized := fees.NormalizeDomain(domain)
	buf := make([]byte, len(feesTotalsIndexPrefix)+len(normalized))
	copy(buf, feesTotalsIndexPrefix)
	copy(buf[len(feesTotalsIndexPrefix):], normalized)
	return buf
}

func feeTotalsIndexEntry(asset string, wallet [20]byte) []byte {
	normalizedAsset := fees.NormalizeAsset(asset)
	hexAddr := hex.EncodeToString(wallet[:])
	buf := make([]byte, len(normalizedAsset)+1+len(hexAddr))
	copy(buf, normalizedAsset)
	buf[len(normalizedAsset)] = '/'
	copy(buf[len(normalizedAsset)+1:], hexAddr)
	return buf
}

func parseFeeTotalsIndexEntry(raw []byte) (string, [20]byte, bool) {
	parts := bytes.SplitN(raw, []byte{'/'}, 2)
	if len(parts) != 2 {
		return "", [20]byte{}, false
	}
	decoded, err := hex.DecodeString(string(parts[1]))
	if err != nil || len(decoded) != 20 {
		return "", [20]byte{}, false
	}
	var wallet [20]byte
	copy(wallet[:], decoded)
	return string(parts[0]), wallet, true
}

func (m *Manager) FeesGetCounter(domain string, payer [20]byte, window time.Time, scope string) (uint64, time.Time, bool, error) {
	if m == nil {
		return 0, time.Time{}, false, fmt.Errorf("fees: state manager not initialised")
	}
	normalizedWindow := monthStartUTC(window)
	if normalizedWindow.IsZero() {
		return 0, time.Time{}, false, fmt.Errorf("fees: window start required")
	}
	normalizedScope := fees.NormalizeFreeTierScope(scope)
	key := feeCounterKey(domain, normalizedWindow, normalizedScope, payer)
	var stored storedFeeCounter
	ok, err := m.KVGet(key, &stored)
	if err != nil {
		return 0, time.Time{}, false, fmt.Errorf("fees: load counter: %w", err)
	}
	if ok {
		windowStart := normalizedWindow
		if stored.WindowStartUnix != 0 {
			windowStart = time.Unix(int64(stored.WindowStartUnix), 0).UTC()
		}
		return stored.Count, windowStart, true, nil
	}
	if normalizedScope == fees.FreeTierScopeAggregate {
		legacyKey := feeCounterLegacyKey(domain, payer)
		var legacy storedFeeCounter
		legacyOK, legacyErr := m.KVGet(legacyKey, &legacy)
		if legacyErr != nil {
			return 0, time.Time{}, false, fmt.Errorf("fees: load legacy counter: %w", legacyErr)
		}
		if legacyOK {
			var legacyWindow time.Time
			if legacy.WindowStartUnix != 0 {
				legacyWindow = time.Unix(int64(legacy.WindowStartUnix), 0).UTC()
			}
			if sameCounterMonth(legacyWindow, normalizedWindow) {
				return legacy.Count, legacyWindow, true, nil
			}
		}
	}
	return 0, normalizedWindow, false, nil
}

func (m *Manager) FeesPutCounter(domain string, payer [20]byte, windowStart time.Time, scope string, count uint64) error {
	if m == nil {
		return fmt.Errorf("fees: state manager not initialised")
	}
	normalizedWindow := monthStartUTC(windowStart)
	if normalizedWindow.IsZero() {
		return fmt.Errorf("fees: window start required")
	}
	normalizedScope := fees.NormalizeFreeTierScope(scope)
	key := feeCounterKey(domain, normalizedWindow, normalizedScope, payer)
	stored := storedFeeCounter{Count: count}
	stored.WindowStartUnix = uint64(normalizedWindow.UTC().Unix())
	if err := m.KVPut(key, stored); err != nil {
		return fmt.Errorf("fees: persist counter: %w", err)
	}
	return nil
}

func ensureFeeTotalsDefaults(stored *storedFeeTotals) {
	if stored == nil {
		return
	}
	if stored.Gross == nil {
		stored.Gross = big.NewInt(0)
	}
	if stored.Fee == nil {
		stored.Fee = big.NewInt(0)
	}
	if stored.Net == nil {
		stored.Net = big.NewInt(0)
	}
}

func (stored *storedFeeTotals) toTotals() fees.Totals {
	ensureFeeTotalsDefaults(stored)
	totals := fees.Totals{Domain: fees.NormalizeDomain(stored.Domain), Asset: fees.NormalizeAsset(stored.Asset), Wallet: stored.Wallet}
	if stored.Gross != nil {
		totals.Gross = new(big.Int).Set(stored.Gross)
	}
	if stored.Fee != nil {
		totals.Fee = new(big.Int).Set(stored.Fee)
	}
	if stored.Net != nil {
		totals.Net = new(big.Int).Set(stored.Net)
	}
	return totals
}

func newStoredFeeTotals(record *fees.Totals) *storedFeeTotals {
	if record == nil {
		return &storedFeeTotals{}
	}
	stored := &storedFeeTotals{Domain: fees.NormalizeDomain(record.Domain), Asset: fees.NormalizeAsset(record.Asset), Wallet: record.Wallet}
	if record.Gross != nil {
		stored.Gross = new(big.Int).Set(record.Gross)
	}
	if record.Fee != nil {
		stored.Fee = new(big.Int).Set(record.Fee)
	}
	if record.Net != nil {
		stored.Net = new(big.Int).Set(record.Net)
	}
	ensureFeeTotalsDefaults(stored)
	return stored
}

func (m *Manager) FeesGetTotals(domain, asset string, wallet [20]byte) (*fees.Totals, bool, error) {
	if m == nil {
		return nil, false, fmt.Errorf("fees: state manager not initialised")
	}
	key := feeTotalsKey(domain, asset, wallet)
	var stored storedFeeTotals
	ok, err := m.KVGet(key, &stored)
	if err != nil {
		return nil, false, fmt.Errorf("fees: load totals: %w", err)
	}
	if !ok {
		return nil, false, nil
	}
	record := stored.toTotals()
	return &record, true, nil
}

func (m *Manager) FeesPutTotals(record *fees.Totals) error {
	if m == nil {
		return fmt.Errorf("fees: state manager not initialised")
	}
	if record == nil {
		return fmt.Errorf("fees: totals record required")
	}
	stored := newStoredFeeTotals(record)
	key := feeTotalsKey(record.Domain, record.Asset, record.Wallet)
	if err := m.KVPut(key, stored); err != nil {
		return fmt.Errorf("fees: persist totals: %w", err)
	}
	indexKey := feeTotalsIndexKey(record.Domain)
	var indexed [][]byte
	if err := m.KVGetList(indexKey, &indexed); err != nil {
		return fmt.Errorf("fees: load totals index: %w", err)
	}
	found := false
	entry := feeTotalsIndexEntry(record.Asset, record.Wallet)
	for _, existing := range indexed {
		if bytes.Equal(existing, entry) {
			found = true
			break
		}
	}
	if !found {
		if err := m.KVAppend(indexKey, append([]byte(nil), entry...)); err != nil {
			return fmt.Errorf("fees: update totals index: %w", err)
		}
	}
	return nil
}

func addToTotals(dest **big.Int, delta *big.Int) {
	if delta == nil || delta.Sign() == 0 {
		return
	}
	if *dest == nil {
		*dest = big.NewInt(0)
	}
	(*dest).Add(*dest, delta)
}

func (m *Manager) FeesAccumulateTotals(domain, asset string, wallet [20]byte, gross, fee, net *big.Int) error {
	if m == nil {
		return fmt.Errorf("fees: state manager not initialised")
	}
	record, ok, err := m.FeesGetTotals(domain, asset, wallet)
	if err != nil {
		return err
	}
	if !ok {
		record = &fees.Totals{Domain: fees.NormalizeDomain(domain), Asset: fees.NormalizeAsset(asset), Wallet: wallet}
	}
	addToTotals(&record.Gross, gross)
	addToTotals(&record.Fee, fee)
	addToTotals(&record.Net, net)
	return m.FeesPutTotals(record)
}

func (m *Manager) FeesListTotals(domain string) ([]fees.Totals, error) {
	if m == nil {
		return nil, fmt.Errorf("fees: state manager not initialised")
	}
	indexKey := feeTotalsIndexKey(domain)
	var entries [][]byte
	if err := m.KVGetList(indexKey, &entries); err != nil {
		return nil, fmt.Errorf("fees: load totals index: %w", err)
	}
	results := make([]fees.Totals, 0, len(entries))
	for _, raw := range entries {
		asset, wallet, ok := parseFeeTotalsIndexEntry(raw)
		if !ok {
			continue
		}
		record, ok, err := m.FeesGetTotals(domain, asset, wallet)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		results = append(results, record.Clone())
	}
	return results, nil
}
