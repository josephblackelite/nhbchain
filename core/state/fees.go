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
	Wallet [20]byte
	Gross  *big.Int
	Fee    *big.Int
	Net    *big.Int
}

func feeCounterKey(domain string, payer [20]byte) []byte {
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

func feeTotalsKey(domain string, wallet [20]byte) []byte {
	normalized := fees.NormalizeDomain(domain)
	hexAddr := hex.EncodeToString(wallet[:])
	buf := make([]byte, len(feesTotalsPrefix)+len(normalized)+1+len(hexAddr))
	copy(buf, feesTotalsPrefix)
	offset := len(feesTotalsPrefix)
	copy(buf[offset:], normalized)
	offset += len(normalized)
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

func (m *Manager) FeesGetCounter(domain string, payer [20]byte) (uint64, time.Time, bool, error) {
	if m == nil {
		return 0, time.Time{}, false, fmt.Errorf("fees: state manager not initialised")
	}
	key := feeCounterKey(domain, payer)
	var stored storedFeeCounter
	ok, err := m.KVGet(key, &stored)
	if err != nil {
		return 0, time.Time{}, false, fmt.Errorf("fees: load counter: %w", err)
	}
	if !ok {
		return 0, time.Time{}, false, nil
	}
	var windowStart time.Time
	if stored.WindowStartUnix != 0 {
		windowStart = time.Unix(int64(stored.WindowStartUnix), 0).UTC()
	}
	return stored.Count, windowStart, true, nil
}

func (m *Manager) FeesPutCounter(domain string, payer [20]byte, count uint64, windowStart time.Time) error {
	if m == nil {
		return fmt.Errorf("fees: state manager not initialised")
	}
	key := feeCounterKey(domain, payer)
	stored := storedFeeCounter{Count: count}
	if !windowStart.IsZero() {
		stored.WindowStartUnix = uint64(windowStart.UTC().Unix())
	}
	return m.KVPut(key, stored)
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
	totals := fees.Totals{Domain: fees.NormalizeDomain(stored.Domain), Wallet: stored.Wallet}
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
	stored := &storedFeeTotals{Domain: fees.NormalizeDomain(record.Domain), Wallet: record.Wallet}
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

func (m *Manager) FeesGetTotals(domain string, wallet [20]byte) (*fees.Totals, bool, error) {
	if m == nil {
		return nil, false, fmt.Errorf("fees: state manager not initialised")
	}
	key := feeTotalsKey(domain, wallet)
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
	key := feeTotalsKey(record.Domain, record.Wallet)
	if err := m.KVPut(key, stored); err != nil {
		return fmt.Errorf("fees: persist totals: %w", err)
	}
	indexKey := feeTotalsIndexKey(record.Domain)
	var indexed [][]byte
	if err := m.KVGetList(indexKey, &indexed); err != nil {
		return fmt.Errorf("fees: load totals index: %w", err)
	}
	found := false
	for _, existing := range indexed {
		if bytes.Equal(existing, record.Wallet[:]) {
			found = true
			break
		}
	}
	if !found {
		if err := m.KVAppend(indexKey, append([]byte(nil), record.Wallet[:]...)); err != nil {
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

func (m *Manager) FeesAccumulateTotals(domain string, wallet [20]byte, gross, fee, net *big.Int) error {
	if m == nil {
		return fmt.Errorf("fees: state manager not initialised")
	}
	record, ok, err := m.FeesGetTotals(domain, wallet)
	if err != nil {
		return err
	}
	if !ok {
		record = &fees.Totals{Domain: fees.NormalizeDomain(domain), Wallet: wallet}
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
	var wallets [][]byte
	if err := m.KVGetList(indexKey, &wallets); err != nil {
		return nil, fmt.Errorf("fees: load totals index: %w", err)
	}
	results := make([]fees.Totals, 0, len(wallets))
	for _, raw := range wallets {
		if len(raw) != 20 {
			continue
		}
		var wallet [20]byte
		copy(wallet[:], raw)
		record, ok, err := m.FeesGetTotals(domain, wallet)
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
