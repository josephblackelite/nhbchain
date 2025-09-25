package swap

import (
	"encoding/base64"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"
)

func TestBurnLedgerPutAndGet(t *testing.T) {
	store := newMockStorage()
	ledger := NewBurnLedger(store)
	now := time.Unix(1800000000, 0)
	ledger.SetClock(func() time.Time { return now })
	var burner [20]byte
	copy(burner[:], []byte{1, 2, 3, 4})
	receipt := &BurnReceipt{
		ReceiptID:       "burn-1",
		ProviderTxID:    "np-1",
		Token:           "ZNHB",
		AmountWei:       big.NewInt(12345),
		Burner:          burner,
		RedeemReference: "redeem-1",
		BurnTxHash:      "0xburn",
		TreasuryTxID:    "treasury-1",
		VoucherIDs:      []string{"voucher-a", "voucher-b"},
		Notes:           "processed",
	}
	if err := ledger.Put(receipt); err != nil {
		t.Fatalf("put: %v", err)
	}
	fetched, ok, err := ledger.Get("burn-1")
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if fetched.AmountWei.Cmp(receipt.AmountWei) != 0 {
		t.Fatalf("unexpected amount %s", fetched.AmountWei)
	}
	if len(fetched.VoucherIDs) != 2 || fetched.VoucherIDs[0] != "voucher-a" {
		t.Fatalf("unexpected vouchers %+v", fetched.VoucherIDs)
	}
	if fetched.ObservedAt != now.Unix() {
		t.Fatalf("unexpected observedAt %d", fetched.ObservedAt)
	}
}

func TestBurnLedgerList(t *testing.T) {
	store := newMockStorage()
	ledger := NewBurnLedger(store)
	base := time.Unix(1900000000, 0)
	ledger.SetClock(func() time.Time { return base })
	for i := 0; i < 3; i++ {
		ts := base.Add(time.Duration(i) * time.Minute)
		ledger.SetClock(func() time.Time { return ts })
		receipt := &BurnReceipt{
			ReceiptID:    fmt.Sprintf("r-%d", i),
			ProviderTxID: fmt.Sprintf("p-%d", i),
			Token:        "ZNHB",
			AmountWei:    big.NewInt(int64(i + 1)),
			VoucherIDs:   []string{fmt.Sprintf("voucher-%d", i)},
		}
		if err := ledger.Put(receipt); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	receipts, cursor, err := ledger.List(0, 0, "", 2)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(receipts) != 2 || cursor == "" {
		t.Fatalf("unexpected list response: %d cursor=%s", len(receipts), cursor)
	}
	if receipts[0].ReceiptID != "r-0" || receipts[1].ReceiptID != "r-1" {
		t.Fatalf("unexpected ordering: %+v", receipts)
	}
	next, finalCursor, err := ledger.List(0, 0, cursor, 2)
	if err != nil {
		t.Fatalf("list next: %v", err)
	}
	if len(next) != 1 || next[0].ReceiptID != "r-2" {
		t.Fatalf("unexpected second page: %+v", next)
	}
	if finalCursor != "" {
		t.Fatalf("expected empty cursor, got %s", finalCursor)
	}
}

func TestBurnLedgerExportCSV(t *testing.T) {
	store := newMockStorage()
	ledger := NewBurnLedger(store)
	ledger.SetClock(func() time.Time { return time.Unix(2000000000, 0) })
	if err := ledger.Put(&BurnReceipt{ReceiptID: "burn-a", ProviderTxID: "p1", Token: "ZNHB", AmountWei: big.NewInt(10), VoucherIDs: []string{"v1"}}); err != nil {
		t.Fatalf("put burn-a: %v", err)
	}
	ledger.SetClock(func() time.Time { return time.Unix(2000000600, 0) })
	if err := ledger.Put(&BurnReceipt{ReceiptID: "burn-b", ProviderTxID: "p2", Token: "ZNHB", AmountWei: big.NewInt(20), VoucherIDs: []string{"v2", "v3"}}); err != nil {
		t.Fatalf("put burn-b: %v", err)
	}
	encoded, count, err := ledger.ExportCSV(0, 0)
	if err != nil {
		t.Fatalf("export csv: %v", err)
	}
	if count != 2 {
		t.Fatalf("unexpected count %d", count)
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	rows := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(rows))
	}
	if !strings.Contains(rows[0], "receiptId") {
		t.Fatalf("expected header, got %s", rows[0])
	}
	if !strings.Contains(rows[1], "burn-a") || !strings.Contains(rows[2], "burn-b") {
		t.Fatalf("unexpected csv rows: %v", rows)
	}
}
