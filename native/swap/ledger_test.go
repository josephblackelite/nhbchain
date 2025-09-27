package swap

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"
)

type mockStorage struct {
	kv    map[string][]byte
	lists map[string][][]byte
}

func newMockStorage() *mockStorage {
	return &mockStorage{kv: make(map[string][]byte), lists: make(map[string][][]byte)}
}

func (m *mockStorage) KVPut(key []byte, value interface{}) error {
	encoded, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	m.kv[string(key)] = encoded
	return nil
}

func (m *mockStorage) KVGet(key []byte, out interface{}) (bool, error) {
	encoded, ok := m.kv[string(key)]
	if !ok {
		return false, nil
	}
	if out == nil {
		return true, nil
	}
	if err := rlp.DecodeBytes(encoded, out); err != nil {
		return false, err
	}
	return true, nil
}

func (m *mockStorage) KVAppend(key []byte, value []byte) error {
	k := string(key)
	m.lists[k] = append(m.lists[k], append([]byte(nil), value...))
	return nil
}

func (m *mockStorage) KVGetList(key []byte, out interface{}) error {
	encoded, err := rlp.EncodeToBytes(m.lists[string(key)])
	if err != nil {
		return err
	}
	return rlp.DecodeBytes(encoded, out)
}

func TestLedgerPutAndGet(t *testing.T) {
	store := newMockStorage()
	ledger := NewLedger(store)
	ledger.SetClock(func() time.Time { return time.Unix(1700000000, 0) })
	record := &VoucherRecord{
		Provider:          "nowpayments",
		ProviderTxID:      "np-1",
		FiatCurrency:      "USD",
		FiatAmount:        "100.00",
		USD:               "100.00",
		Rate:              "0.50",
		Token:             "ZNHB",
		MintAmountWei:     big.NewInt(5000000000000000000),
		QuoteTimestamp:    time.Unix(1700000000, 0).Unix(),
		OracleSource:      "manual",
		OracleMedian:      "0.49",
		OracleFeeders:     []string{"alpha", "beta"},
		PriceProofID:      "proof-123",
		MinterSignature:   "0xabc",
		TwapRate:          "0.48",
		TwapObservations:  4,
		TwapWindowSeconds: 300,
		TwapStart:         time.Unix(1699999700, 0).Unix(),
		TwapEnd:           time.Unix(1700000000, 0).Unix(),
	}
	if err := ledger.Put(record); err != nil {
		t.Fatalf("put: %v", err)
	}
	fetched, ok, err := ledger.Get("np-1")
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if fetched.Provider != "nowpayments" {
		t.Fatalf("unexpected provider %s", fetched.Provider)
	}
	if fetched.MintAmountWei.Cmp(record.MintAmountWei) != 0 {
		t.Fatalf("unexpected amount %s", fetched.MintAmountWei)
	}
	if fetched.TwapRate != "0.48" || fetched.TwapObservations != 4 {
		t.Fatalf("unexpected twap data: %+v", fetched)
	}
	if fetched.OracleMedian != "0.49" {
		t.Fatalf("unexpected median: %s", fetched.OracleMedian)
	}
	if fetched.PriceProofID != "proof-123" {
		t.Fatalf("unexpected proof id: %s", fetched.PriceProofID)
	}
	if len(fetched.OracleFeeders) != 2 {
		t.Fatalf("unexpected feeders: %+v", fetched.OracleFeeders)
	}
}

func TestLedgerListAndCursor(t *testing.T) {
	store := newMockStorage()
	ledger := NewLedger(store)
	timestamps := []time.Time{
		time.Unix(1700000100, 0),
		time.Unix(1700000200, 0),
		time.Unix(1700000300, 0),
	}
	idx := 0
	ledger.SetClock(func() time.Time {
		current := timestamps[idx]
		if idx < len(timestamps)-1 {
			idx++
		}
		return current
	})
	for i := 0; i < 3; i++ {
		rec := &VoucherRecord{
			Provider:        "manual",
			ProviderTxID:    fmt.Sprintf("id-%d", i),
			FiatCurrency:    "USD",
			FiatAmount:      "50.00",
			Rate:            "0.25",
			Token:           "ZNHB",
			MintAmountWei:   big.NewInt(int64(i + 1)),
			QuoteTimestamp:  timestamps[i].Unix(),
			OracleSource:    "manual",
			MinterSignature: "0xsig",
		}
		if err := ledger.Put(rec); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	page, cursor, err := ledger.List(0, 0, "", 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page) != 2 || cursor == "" {
		t.Fatalf("unexpected page len=%d cursor=%s", len(page), cursor)
	}
	if page[0].ProviderTxID != "id-0" || page[1].ProviderTxID != "id-1" {
		t.Fatalf("unexpected ordering: %+v", page)
	}
	second, next, err := ledger.List(0, 0, cursor, 2)
	if err != nil {
		t.Fatalf("list next: %v", err)
	}
	if len(second) != 1 || second[0].ProviderTxID != "id-2" {
		t.Fatalf("unexpected second page: %+v", second)
	}
	if next != "" {
		t.Fatalf("expected empty cursor, got %s", next)
	}
}

func TestLedgerExportCSV(t *testing.T) {
	store := newMockStorage()
	ledger := NewLedger(store)
	ledger.SetClock(func() time.Time { return time.Unix(1700000400, 0) })
	_ = ledger.Put(&VoucherRecord{Provider: "p", ProviderTxID: "a", FiatCurrency: "USD", FiatAmount: "10", Rate: "0.1", Token: "ZNHB", MintAmountWei: big.NewInt(100), QuoteTimestamp: time.Unix(1700000400, 0).Unix(), OracleSource: "manual", OracleMedian: "0.11", OracleFeeders: []string{"manual"}, PriceProofID: "proof-a", MinterSignature: "0xsig", TwapRate: "0.11", TwapObservations: 3, TwapWindowSeconds: 180, TwapStart: time.Unix(1700000300, 0).Unix(), TwapEnd: time.Unix(1700000400, 0).Unix()})
	ledger.SetClock(func() time.Time { return time.Unix(1700000500, 0) })
	_ = ledger.Put(&VoucherRecord{Provider: "p", ProviderTxID: "b", FiatCurrency: "USD", FiatAmount: "20", Rate: "0.2", Token: "ZNHB", MintAmountWei: big.NewInt(200), QuoteTimestamp: time.Unix(1700000500, 0).Unix(), OracleSource: "manual", OracleMedian: "0.19", OracleFeeders: []string{"manual"}, PriceProofID: "proof-b", MinterSignature: "0xsig", TwapRate: "0.19", TwapObservations: 4, TwapWindowSeconds: 240, TwapStart: time.Unix(1700000400, 0).Unix(), TwapEnd: time.Unix(1700000500, 0).Unix()})
	encoded, count, total, err := ledger.ExportCSV(0, 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if count != 2 {
		t.Fatalf("unexpected count %d", count)
	}
	if total.Cmp(big.NewInt(300)) != 0 {
		t.Fatalf("unexpected total %s", total)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	rows := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if !strings.Contains(rows[0], "oracleMedian") {
		t.Fatalf("expected oracle metadata header, got %s", rows[0])
	}
	if !strings.Contains(rows[0], "twapRate") {
		t.Fatalf("expected twap header, got %s", rows[0])
	}
	if !strings.Contains(rows[1], "0.11") || !strings.Contains(rows[2], "0.19") {
		t.Fatalf("unexpected csv rows: %v", rows)
	}
}

func TestLedgerExportCSVNoPagination(t *testing.T) {
	store := newMockStorage()
	ledger := NewLedger(store)
	base := time.Unix(1800000000, 0)
	idx := 0
	ledger.SetClock(func() time.Time {
		current := base.Add(time.Duration(idx) * time.Second)
		idx++
		return current
	})
	expectedTotal := big.NewInt(0)
	for i := 0; i < 60; i++ {
		amount := big.NewInt(int64(i + 1))
		expectedTotal = new(big.Int).Add(expectedTotal, amount)
		record := &VoucherRecord{
			Provider:        "p",
			ProviderTxID:    fmt.Sprintf("bulk-%d", i),
			FiatCurrency:    "USD",
			FiatAmount:      "1",
			Rate:            "1",
			Token:           "ZNHB",
			MintAmountWei:   amount,
			QuoteTimestamp:  base.Add(time.Duration(i) * time.Second).Unix(),
			OracleSource:    "manual",
			OracleMedian:    "1",
			OracleFeeders:   []string{"manual"},
			PriceProofID:    fmt.Sprintf("proof-%d", i),
			MinterSignature: "0xsig",
		}
		if err := ledger.Put(record); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	encoded, count, total, err := ledger.ExportCSV(0, 0)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if count != 60 {
		t.Fatalf("expected 60 entries, got %d", count)
	}
	if total.Cmp(expectedTotal) != 0 {
		t.Fatalf("unexpected total %s", total)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	rows := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(rows) != 61 {
		t.Fatalf("expected 61 rows, got %d", len(rows))
	}
	if !strings.Contains(rows[len(rows)-1], "bulk-59") {
		t.Fatalf("expected last row to include bulk-59, got %s", rows[len(rows)-1])
	}
}

func TestLedgerMarkReconciled(t *testing.T) {
	store := newMockStorage()
	ledger := NewLedger(store)
	ledger.SetClock(func() time.Time { return time.Unix(1700000600, 0) })
	if err := ledger.Put(&VoucherRecord{Provider: "p", ProviderTxID: "id", FiatCurrency: "USD", FiatAmount: "1", Rate: "0.1", Token: "ZNHB", MintAmountWei: big.NewInt(1), QuoteTimestamp: time.Unix(1700000600, 0).Unix(), OracleSource: "manual", MinterSignature: "0xsig"}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := ledger.MarkReconciled([]string{"id"}); err != nil {
		t.Fatalf("mark reconciled: %v", err)
	}
	rec, ok, err := ledger.Get("id")
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if rec.Status != VoucherStatusReconciled {
		t.Fatalf("expected reconciled status, got %s", rec.Status)
	}
}
