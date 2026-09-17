package rewards

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"nhbchain/storage"
)

func mustHexAddress(t *testing.T, s string) [20]byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	var addr [20]byte
	copy(addr[:], b)
	return addr
}

func TestLedgerPutAndList(t *testing.T) {
	db := storage.NewMemDB()
	ledger := NewLedger(db)
	now := time.Unix(1700000000, 0).UTC()
	entry := &RewardEntry{
		Epoch:       1,
		Address:     mustHexAddress(t, "0102030405060708090a0b0c0d0e0f1011121314"),
		Amount:      big.NewInt(100),
		Currency:    "ZNHB",
		Status:      RewardStatusReady,
		GeneratedAt: now,
	}
	if err := ledger.Put(entry); err != nil {
		t.Fatalf("put: %v", err)
	}
	results, next, err := ledger.List(RewardFilter{Epoch: &entry.Epoch})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if next != "" {
		t.Fatalf("unexpected next cursor %s", next)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result got %d", len(results))
	}
	got := results[0]
	if got.Amount.Cmp(big.NewInt(100)) != 0 || got.Status != RewardStatusReady {
		t.Fatalf("unexpected entry: %+v", got)
	}
}

func TestLedgerMarkPaidIdempotent(t *testing.T) {
	db := storage.NewMemDB()
	ledger := NewLedger(db)
	addr := mustHexAddress(t, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	entry := &RewardEntry{
		Epoch:    10,
		Address:  addr,
		Amount:   big.NewInt(500),
		Currency: "ZNHB",
		Status:   RewardStatusReady,
	}
	if err := ledger.Put(entry); err != nil {
		t.Fatalf("put: %v", err)
	}
	refs := []MarkPaidReference{{Address: addr, Amount: big.NewInt(500)}}
	count, err := ledger.MarkPaid(10, refs, "tx-1", "auditor", time.Unix(1700, 0).UTC())
	if err != nil {
		t.Fatalf("mark paid: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 update got %d", count)
	}
	// repeat should be idempotent
	count, err = ledger.MarkPaid(10, refs, "tx-1", "auditor", time.Unix(1700, 0).UTC())
	if err != nil {
		t.Fatalf("mark paid repeat: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 updates on repeat got %d", count)
	}
	got, ok, err := ledger.Get(10, addr)
	if err != nil || !ok {
		t.Fatalf("get: %v ok=%v", err, ok)
	}
	if got.Status != RewardStatusPaid {
		t.Fatalf("expected paid status got %s", got.Status)
	}
	if got.TxRef != "tx-1" || got.PaidBy != "auditor" {
		t.Fatalf("unexpected metadata %+v", got)
	}
}

func TestLedgerPagination(t *testing.T) {
	db := storage.NewMemDB()
	ledger := NewLedger(db)
	epoch := uint64(3)
	for i := 0; i < 5; i++ {
		addrBytes := make([]byte, 20)
		addrBytes[19] = byte(i)
		var addr [20]byte
		copy(addr[:], addrBytes)
		entry := &RewardEntry{Epoch: epoch, Address: addr, Amount: big.NewInt(int64(100 + i))}
		if err := ledger.Put(entry); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	limit := 2
	cursor := ""
	total := 0
	for {
		results, next, err := ledger.List(RewardFilter{Epoch: &epoch, Limit: limit, Cursor: cursor})
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		total += len(results)
		if next == "" {
			break
		}
		cursor = next
	}
	if total != 5 {
		t.Fatalf("expected total 5 got %d", total)
	}
}
