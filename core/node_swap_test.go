package core

import (
	"math/big"
	"testing"
	"time"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/native/swap"
)

func TestSwapRecordBurnPersistsReceipt(t *testing.T) {
	node := newTestNode(t)
	// seed a voucher for reconciliation
	if err := node.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewLedger(m)
		record := &swap.VoucherRecord{
			Provider:        "gateway",
			ProviderTxID:    "np-1",
			FiatCurrency:    "USD",
			FiatAmount:      "1",
			Rate:            "1",
			Token:           "ZNHB",
			MintAmountWei:   big.NewInt(1),
			QuoteTimestamp:  time.Now().Unix(),
			OracleSource:    "manual",
			MinterSignature: "sig",
		}
		return ledger.Put(record)
	}); err != nil {
		t.Fatalf("seed voucher: %v", err)
	}
	observed := time.Unix(2100000000, 0).Unix()
	receipt := &swap.BurnReceipt{
		ReceiptID:    "burn-1",
		ProviderTxID: "np-1",
		Token:        "ZNHB",
		AmountWei:    big.NewInt(1),
		VoucherIDs:   []string{"np-1"},
		ObservedAt:   observed,
	}
	if err := node.SwapRecordBurn(receipt); err != nil {
		t.Fatalf("record burn: %v", err)
	}
	if err := node.WithState(func(m *nhbstate.Manager) error {
		burnLedger := swap.NewBurnLedger(m)
		stored, ok, err := burnLedger.Get("burn-1")
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("expected burn receipt persisted")
		}
		if stored.ObservedAt != observed {
			t.Fatalf("expected observedAt %d, got %d", observed, stored.ObservedAt)
		}
		voucherLedger := swap.NewLedger(m)
		voucher, ok, err := voucherLedger.Get("np-1")
		if err != nil {
			return err
		}
		if !ok {
			t.Fatalf("expected voucher present")
		}
		if voucher.Status != swap.VoucherStatusReconciled {
			t.Fatalf("expected reconciled status, got %s", voucher.Status)
		}
		return nil
	}); err != nil {
		t.Fatalf("verify state: %v", err)
	}
	emitted := node.Events()
	foundBurn := false
	foundRecon := false
	for _, evt := range emitted {
		switch evt.Type {
		case events.TypeSwapBurnRecorded:
			foundBurn = true
		case events.TypeSwapTreasuryReconciled:
			foundRecon = true
		}
	}
	if !foundBurn {
		t.Fatalf("expected burn recorded event")
	}
	if !foundRecon {
		t.Fatalf("expected treasury reconciled event")
	}
}

func TestSwapListBurnReceipts(t *testing.T) {
	node := newTestNode(t)
	if err := node.WithState(func(m *nhbstate.Manager) error {
		ledger := swap.NewBurnLedger(m)
		ledger.SetClock(func() time.Time { return time.Unix(2200000000, 0) })
		if err := ledger.Put(&swap.BurnReceipt{ReceiptID: "burn-a", Token: "ZNHB", AmountWei: big.NewInt(10)}); err != nil {
			return err
		}
		ledger.SetClock(func() time.Time { return time.Unix(2200000600, 0) })
		if err := ledger.Put(&swap.BurnReceipt{ReceiptID: "burn-b", Token: "ZNHB", AmountWei: big.NewInt(20)}); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("seed burns: %v", err)
	}
	receipts, cursor, err := node.SwapListBurnReceipts(0, 0, "", 1)
	if err != nil {
		t.Fatalf("list burns: %v", err)
	}
	if len(receipts) != 1 || cursor == "" {
		t.Fatalf("unexpected list response: len=%d cursor=%s", len(receipts), cursor)
	}
	if receipts[0].ReceiptID != "burn-a" {
		t.Fatalf("expected burn-a, got %s", receipts[0].ReceiptID)
	}
}
