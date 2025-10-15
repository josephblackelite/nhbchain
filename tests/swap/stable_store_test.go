package swap_test

import (
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	swap "nhbchain/native/swap"
)

type mockStableStorage struct {
	kv       map[string][]byte
	lists    map[string][][]byte
	supplies map[string]*big.Int
}

func newMockStableStorage() *mockStableStorage {
	return &mockStableStorage{kv: make(map[string][]byte), lists: make(map[string][][]byte), supplies: make(map[string]*big.Int)}
}

func (m *mockStableStorage) KVPut(key []byte, value interface{}) error {
	encoded, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	m.kv[string(key)] = encoded
	return nil
}

func (m *mockStableStorage) KVDelete(key []byte) error {
	delete(m.kv, string(key))
	return nil
}

func (m *mockStableStorage) KVGet(key []byte, out interface{}) (bool, error) {
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

func (m *mockStableStorage) KVAppend(key []byte, value []byte) error {
	k := string(key)
	m.lists[k] = append(m.lists[k], append([]byte(nil), value...))
	return nil
}

func (m *mockStableStorage) KVGetList(key []byte, out interface{}) error {
	encoded, err := rlp.EncodeToBytes(m.lists[string(key)])
	if err != nil {
		return err
	}
	return rlp.DecodeBytes(encoded, out)
}

func (m *mockStableStorage) AdjustTokenSupply(symbol string, delta *big.Int) (*big.Int, error) {
	if m.supplies == nil {
		m.supplies = make(map[string]*big.Int)
	}
	normalized := strings.ToUpper(strings.TrimSpace(symbol))
	current := new(big.Int)
	if existing, ok := m.supplies[normalized]; ok && existing != nil {
		current = new(big.Int).Set(existing)
	}
	if delta != nil {
		current = current.Add(current, delta)
	}
	if current.Sign() < 0 {
		return nil, fmt.Errorf("supply underflow for %s", normalized)
	}
	m.supplies[normalized] = new(big.Int).Set(current)
	return new(big.Int).Set(current), nil
}

func TestDepositVoucherIdempotent(t *testing.T) {
	store := swap.NewStableStore(newMockStableStorage())
	now := time.Unix(1700000000, 0)
	store.SetClock(func() time.Time { return now })

	voucher := &swap.DepositVoucher{
		InvoiceID:    "inv-001",
		Provider:     "nowpayments",
		StableAsset:  swap.StableAssetUSDC,
		StableAmount: big.NewInt(1_000_000),
		NhbAmount:    big.NewInt(2_000_000_000_000_000_000),
		Account:      "nhb1account",
		Memo:         "first deposit",
	}
	if err := store.PutDepositVoucher(voucher); err != nil {
		t.Fatalf("put voucher: %v", err)
	}
	if err := store.PutDepositVoucher(voucher); err == nil {
		t.Fatalf("expected idempotency error")
	}
	fetched, ok, err := store.GetDepositVoucher("inv-001")
	if err != nil || !ok {
		t.Fatalf("get voucher: %v ok=%v", err, ok)
	}
	if fetched.CreatedAt != now.Unix() {
		t.Fatalf("unexpected created at: %d", fetched.CreatedAt)
	}
	if fetched.StableAmount.Cmp(voucher.StableAmount) != 0 {
		t.Fatalf("unexpected stable amount: %s", fetched.StableAmount)
	}
	inventory, err := store.GetSoftInventory(swap.StableAssetUSDC)
	if err != nil {
		t.Fatalf("get inventory: %v", err)
	}
	if inventory.Deposits.Cmp(voucher.StableAmount) != 0 || inventory.Balance.Cmp(voucher.StableAmount) != 0 {
		t.Fatalf("inventory mismatch: %+v", inventory)
	}
}

func TestCashOutLifecycle_BurnAfterReceipt(t *testing.T) {
	backend := newMockStableStorage()
	store := swap.NewStableStore(backend)
	now := time.Unix(1700000500, 0)
	store.SetClock(func() time.Time { return now })

	deposit := &swap.DepositVoucher{
		InvoiceID:    "inv-002",
		Provider:     "nowpayments",
		StableAsset:  swap.StableAssetUSDC,
		StableAmount: big.NewInt(5_000_000),
		NhbAmount:    big.NewInt(5_000_000_000_000_000_000),
		Account:      "nhb1alice",
	}
	if err := store.PutDepositVoucher(deposit); err != nil {
		t.Fatalf("put deposit: %v", err)
	}
	if _, err := backend.AdjustTokenSupply("NHB", deposit.NhbAmount); err != nil {
		t.Fatalf("prime supply: %v", err)
	}

	intent := &swap.CashOutIntent{
		IntentID:     "intent-001",
		InvoiceID:    "inv-002",
		Account:      "nhb1alice",
		StableAsset:  swap.StableAssetUSDC,
		StableAmount: big.NewInt(3_000_000),
		NhbAmount:    big.NewInt(3_000_000_000_000_000_000),
	}
	if err := store.CreateCashOutIntent(intent); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	recordedIntent, ok, err := store.GetCashOutIntent("intent-001")
	if err != nil || !ok {
		t.Fatalf("get intent: %v ok=%v", err, ok)
	}
	if recordedIntent.Status != swap.CashOutStatusPending {
		t.Fatalf("unexpected status: %s", recordedIntent.Status)
	}
	lock, ok, err := store.GetEscrowLock("intent-001")
	if err != nil || !ok {
		t.Fatalf("get escrow: %v ok=%v", err, ok)
	}
	if lock.Burned {
		t.Fatalf("expected lock unburned")
	}

	receipt := &swap.PayoutReceipt{
		ReceiptID:    "rcpt-001",
		IntentID:     "intent-001",
		StableAsset:  swap.StableAssetUSDC,
		StableAmount: big.NewInt(3_000_000),
		NhbAmount:    big.NewInt(3_000_000_000_000_000_000),
		TxHash:       "0xabc123",
	}
	if err := store.RecordPayoutReceipt(receipt); err != nil {
		t.Fatalf("record receipt: %v", err)
	}
	expectedSupply := new(big.Int).Sub(deposit.NhbAmount, receipt.NhbAmount)
	if total := backend.supplies["NHB"]; total == nil || total.Cmp(expectedSupply) != 0 {
		t.Fatalf("unexpected supply total: got %v want %s", total, expectedSupply)
	}
	settledIntent, ok, err := store.GetCashOutIntent("intent-001")
	if err != nil || !ok {
		t.Fatalf("get settled intent: %v ok=%v", err, ok)
	}
	if settledIntent.Status != swap.CashOutStatusSettled {
		t.Fatalf("expected settled status, got %s", settledIntent.Status)
	}
	lock, ok, err = store.GetEscrowLock("intent-001")
	if err != nil || !ok {
		t.Fatalf("get escrow after settle: %v ok=%v", err, ok)
	}
	if !lock.Burned {
		t.Fatalf("expected escrow burned")
	}
	payout, ok, err := store.GetPayoutReceipt("intent-001")
	if err != nil || !ok {
		t.Fatalf("get receipt: %v ok=%v", err, ok)
	}
	if payout.TxHash != "0xabc123" {
		t.Fatalf("unexpected tx hash: %s", payout.TxHash)
	}
	inventory, err := store.GetSoftInventory(swap.StableAssetUSDC)
	if err != nil {
		t.Fatalf("get inventory: %v", err)
	}
	expectedBalance := big.NewInt(0).Sub(deposit.StableAmount, receipt.StableAmount)
	if inventory.Balance.Cmp(expectedBalance) != 0 {
		t.Fatalf("expected balance %s got %s", expectedBalance, inventory.Balance)
	}
	if err := store.RecordPayoutReceipt(receipt); err == nil {
		t.Fatalf("expected duplicate receipt error")
	}
}

func TestSoftInventoryAccrual(t *testing.T) {
	backend := newMockStableStorage()
	store := swap.NewStableStore(backend)
	store.SetClock(func() time.Time { return time.Unix(1700001000, 0) })

	deposits := []*swap.DepositVoucher{
		{
			InvoiceID:    "inv-10",
			Provider:     "gateway",
			StableAsset:  swap.StableAssetUSDT,
			StableAmount: big.NewInt(2_000_000),
			NhbAmount:    big.NewInt(2_100_000_000_000_000_000),
		},
		{
			InvoiceID:    "inv-11",
			Provider:     "gateway",
			StableAsset:  swap.StableAssetUSDT,
			StableAmount: big.NewInt(1_500_000),
			NhbAmount:    big.NewInt(1_600_000_000_000_000_000),
		},
	}
	for _, voucher := range deposits {
		if err := store.PutDepositVoucher(voucher); err != nil {
			t.Fatalf("put deposit %s: %v", voucher.InvoiceID, err)
		}
		if _, err := backend.AdjustTokenSupply("NHB", voucher.NhbAmount); err != nil {
			t.Fatalf("prime supply %s: %v", voucher.InvoiceID, err)
		}
	}
	inventory, err := store.GetSoftInventory(swap.StableAssetUSDT)
	if err != nil {
		t.Fatalf("inventory: %v", err)
	}
	totalDeposits := big.NewInt(0).Add(deposits[0].StableAmount, deposits[1].StableAmount)
	if inventory.Deposits.Cmp(totalDeposits) != 0 || inventory.Balance.Cmp(totalDeposits) != 0 {
		t.Fatalf("unexpected inventory after deposits: %+v", inventory)
	}

	intent := &swap.CashOutIntent{
		IntentID:     "intent-22",
		StableAsset:  swap.StableAssetUSDT,
		StableAmount: big.NewInt(1_000_000),
		NhbAmount:    big.NewInt(1_050_000_000_000_000_000),
	}
	if err := store.CreateCashOutIntent(intent); err != nil {
		t.Fatalf("create intent: %v", err)
	}
	receipt := &swap.PayoutReceipt{
		ReceiptID:    "rcpt-22",
		IntentID:     "intent-22",
		StableAsset:  swap.StableAssetUSDT,
		StableAmount: big.NewInt(1_000_000),
		NhbAmount:    big.NewInt(1_050_000_000_000_000_000),
	}
	if err := store.RecordPayoutReceipt(receipt); err != nil {
		t.Fatalf("record receipt: %v", err)
	}
	inventory, err = store.GetSoftInventory(swap.StableAssetUSDT)
	if err != nil {
		t.Fatalf("inventory after payout: %v", err)
	}
	expectedBalance := big.NewInt(0).Sub(totalDeposits, receipt.StableAmount)
	if inventory.Balance.Cmp(expectedBalance) != 0 {
		t.Fatalf("unexpected final balance: %s vs %s", inventory.Balance, expectedBalance)
	}
	if inventory.Payouts.Cmp(receipt.StableAmount) != 0 {
		t.Fatalf("unexpected payouts total: %s", inventory.Payouts)
	}
	expectedSupply := new(big.Int).Sub(new(big.Int).Add(deposits[0].NhbAmount, deposits[1].NhbAmount), receipt.NhbAmount)
	if total := backend.supplies["NHB"]; total == nil || total.Cmp(expectedSupply) != 0 {
		t.Fatalf("unexpected supply after payout: got %v want %s", total, expectedSupply)
	}
}
