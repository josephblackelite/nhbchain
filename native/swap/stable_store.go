package swap

import (
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/rlp"
)

type storedDepositVoucher struct {
	InvoiceID    string
	Provider     string
	StableAsset  string
	StableAmount string
	NhbAmount    string
	Account      string
	Memo         string
	CreatedAt    uint64
}

type storedCashOutIntent struct {
	IntentID     string
	InvoiceID    string
	Account      string
	StableAsset  string
	StableAmount string
	NhbAmount    string
	CreatedAt    uint64
	Status       string
	EscrowLockID string
	SettledAt    uint64
}

type storedEscrowLock struct {
	LockID    string
	IntentID  string
	Account   string
	NhbAmount string
	LockedAt  uint64
	Burned    bool
	BurnedAt  uint64
}

type storedPayoutReceipt struct {
	ReceiptID    string
	IntentID     string
	StableAsset  string
	StableAmount string
	NhbAmount    string
	TxHash       string
	EvidenceURI  string
	SettledAt    uint64
}

type storedSoftInventory struct {
	StableAsset string
	Deposits    string
	Payouts     string
	Balance     string
	UpdatedAt   uint64
}

type stableVoucherIndexEntry struct {
	InvoiceID string
	CreatedAt uint64
}

// StableStore persists stablecoin voucher and redemption state.
type StableStore struct {
	store Storage
	clock func() time.Time
}

// NewStableStore constructs a StableStore backed by the provided storage.
func NewStableStore(store Storage) *StableStore {
	return &StableStore{store: store, clock: time.Now}
}

// SetClock overrides the time source for deterministic testing.
func (s *StableStore) SetClock(clock func() time.Time) {
	if s == nil || clock == nil {
		return
	}
	s.clock = clock
}

// PutDepositVoucher records an on-chain representation of an off-chain deposit.
func (s *StableStore) PutDepositVoucher(voucher *DepositVoucher) error {
	if s == nil {
		return fmt.Errorf("stable: store not initialised")
	}
	if voucher == nil {
		return fmt.Errorf("stable: voucher must not be nil")
	}
	invoice := strings.TrimSpace(voucher.InvoiceID)
	if invoice == "" {
		return fmt.Errorf("stable: invoice id required")
	}
	asset, err := normaliseAsset(voucher.StableAsset)
	if err != nil {
		return err
	}
	stableAmount, err := ensurePositiveAmount(voucher.StableAmount)
	if err != nil {
		return fmt.Errorf("stable: stable amount: %w", err)
	}
	nhbAmount, err := ensurePositiveAmount(voucher.NhbAmount)
	if err != nil {
		return fmt.Errorf("stable: nhb amount: %w", err)
	}
	key := stableDepositVoucherKey(invoice)
	var existing storedDepositVoucher
	ok, err := s.store.KVGet(key, &existing)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("stable: voucher %s already recorded", invoice)
	}
	createdAt := voucher.CreatedAt
	if createdAt <= 0 {
		now := s.clock().UTC().Unix()
		if now < 0 {
			createdAt = 0
		} else {
			createdAt = now
		}
	}
	stored := storedDepositVoucher{
		InvoiceID:    invoice,
		Provider:     strings.TrimSpace(voucher.Provider),
		StableAsset:  string(asset),
		StableAmount: stableAmount.String(),
		NhbAmount:    nhbAmount.String(),
		Account:      strings.TrimSpace(voucher.Account),
		Memo:         strings.TrimSpace(voucher.Memo),
		CreatedAt:    sanitizeUnix(createdAt),
	}
	if err := s.store.KVPut(key, stored); err != nil {
		return err
	}
	if err := s.adjustSoftInventory(asset, stableAmount, nil); err != nil {
		return err
	}
	entry := stableVoucherIndexEntry{InvoiceID: invoice, CreatedAt: stored.CreatedAt}
	encoded, err := rlp.EncodeToBytes(entry)
	if err != nil {
		return err
	}
	return s.store.KVAppend(stableDepositVoucherIndexKey, encoded)
}

// GetDepositVoucher retrieves a deposit voucher by invoice identifier.
func (s *StableStore) GetDepositVoucher(invoiceID string) (*DepositVoucher, bool, error) {
	if s == nil {
		return nil, false, fmt.Errorf("stable: store not initialised")
	}
	key := stableDepositVoucherKey(invoiceID)
	var stored storedDepositVoucher
	ok, err := s.store.KVGet(key, &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	voucher, err := toDepositVoucher(&stored)
	if err != nil {
		return nil, false, err
	}
	return voucher, true, nil
}

// CreateCashOutIntent locks NHB into escrow pending fiat settlement.
func (s *StableStore) CreateCashOutIntent(intent *CashOutIntent) error {
	if s == nil {
		return fmt.Errorf("stable: store not initialised")
	}
	if intent == nil {
		return fmt.Errorf("stable: intent must not be nil")
	}
	intentID := strings.TrimSpace(intent.IntentID)
	if intentID == "" {
		return fmt.Errorf("stable: intent id required")
	}
	asset, err := normaliseAsset(intent.StableAsset)
	if err != nil {
		return err
	}
	stableAmount, err := ensurePositiveAmount(intent.StableAmount)
	if err != nil {
		return fmt.Errorf("stable: stable amount: %w", err)
	}
	nhbAmount, err := ensurePositiveAmount(intent.NhbAmount)
	if err != nil {
		return fmt.Errorf("stable: nhb amount: %w", err)
	}
	// Ensure inventory can satisfy the requested payout.
	inventory, err := s.GetSoftInventory(asset)
	if err != nil {
		return err
	}
	if inventory.Balance.Cmp(stableAmount) < 0 {
		return fmt.Errorf("stable: insufficient soft inventory for %s", intentID)
	}
	key := stableCashOutIntentKey(intentID)
	var existing storedCashOutIntent
	ok, err := s.store.KVGet(key, &existing)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("stable: intent %s already exists", intentID)
	}
	createdAt := intent.CreatedAt
	if createdAt <= 0 {
		now := s.clock().UTC().Unix()
		if now < 0 {
			createdAt = 0
		} else {
			createdAt = now
		}
	}
	status := intent.Status
	if status == "" {
		status = CashOutStatusPending
	}
	lockID := strings.TrimSpace(intent.EscrowLockID)
	if lockID == "" {
		lockID = intentID
	}
	stored := storedCashOutIntent{
		IntentID:     intentID,
		InvoiceID:    strings.TrimSpace(intent.InvoiceID),
		Account:      strings.TrimSpace(intent.Account),
		StableAsset:  string(asset),
		StableAmount: stableAmount.String(),
		NhbAmount:    nhbAmount.String(),
		CreatedAt:    sanitizeUnix(createdAt),
		Status:       string(status),
		EscrowLockID: lockID,
	}
	if err := s.store.KVPut(key, stored); err != nil {
		return err
	}
	lock := storedEscrowLock{
		LockID:    lockID,
		IntentID:  intentID,
		Account:   stored.Account,
		NhbAmount: nhbAmount.String(),
		LockedAt:  stored.CreatedAt,
	}
	if err := s.store.KVPut(stableEscrowLockKey(intentID), lock); err != nil {
		return err
	}
	return nil
}

// GetCashOutIntent loads a previously recorded cash-out intent.
func (s *StableStore) GetCashOutIntent(intentID string) (*CashOutIntent, bool, error) {
	if s == nil {
		return nil, false, fmt.Errorf("stable: store not initialised")
	}
	var stored storedCashOutIntent
	ok, err := s.store.KVGet(stableCashOutIntentKey(intentID), &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	intent, err := toCashOutIntent(&stored)
	if err != nil {
		return nil, false, err
	}
	return intent, true, nil
}

// GetEscrowLock retrieves the escrow lock for a given intent.
func (s *StableStore) GetEscrowLock(intentID string) (*EscrowLock, bool, error) {
	if s == nil {
		return nil, false, fmt.Errorf("stable: store not initialised")
	}
	var stored storedEscrowLock
	ok, err := s.store.KVGet(stableEscrowLockKey(intentID), &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	lock, err := toEscrowLock(&stored)
	if err != nil {
		return nil, false, err
	}
	return lock, true, nil
}

// RecordPayoutReceipt finalises a cash-out intent and burns the locked NHB.
func (s *StableStore) RecordPayoutReceipt(receipt *PayoutReceipt) error {
	if s == nil {
		return fmt.Errorf("stable: store not initialised")
	}
	if receipt == nil {
		return fmt.Errorf("stable: receipt must not be nil")
	}
	intentID := strings.TrimSpace(receipt.IntentID)
	if intentID == "" {
		return fmt.Errorf("stable: intent id required")
	}
	var existingReceipt storedPayoutReceipt
	exists, err := s.store.KVGet(stablePayoutReceiptKey(intentID), &existingReceipt)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("stable: receipt already stored for %s", intentID)
	}
	intent, ok, err := s.GetCashOutIntent(intentID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("stable: intent %s not found", intentID)
	}
	if intent.Status != CashOutStatusPending {
		return fmt.Errorf("stable: intent %s is not pending", intentID)
	}
	asset, err := normaliseAsset(receipt.StableAsset)
	if err != nil {
		asset = intent.StableAsset
	}
	if asset != intent.StableAsset {
		return fmt.Errorf("stable: receipt asset mismatch for %s", intentID)
	}
	stableAmount, err := ensurePositiveAmount(receipt.StableAmount)
	if err != nil {
		stableAmount = intent.StableAmount
	}
	if stableAmount.Cmp(intent.StableAmount) != 0 {
		return fmt.Errorf("stable: receipt amount mismatch for %s", intentID)
	}
	nhbAmount, err := ensurePositiveAmount(receipt.NhbAmount)
	if err != nil {
		nhbAmount = intent.NhbAmount
	}
	if nhbAmount.Cmp(intent.NhbAmount) != 0 {
		return fmt.Errorf("stable: receipt nhb mismatch for %s", intentID)
	}
	settledAt := receipt.SettledAt
	if settledAt <= 0 {
		now := s.clock().UTC().Unix()
		if now < 0 {
			settledAt = 0
		} else {
			settledAt = now
		}
	}
	if err := s.adjustSoftInventory(asset, nil, stableAmount); err != nil {
		return err
	}
	// Burn escrowed NHB.
	var storedLock storedEscrowLock
	ok, err = s.store.KVGet(stableEscrowLockKey(intentID), &storedLock)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("stable: escrow lock missing for %s", intentID)
	}
	if storedLock.Burned {
		return fmt.Errorf("stable: escrow already burned for %s", intentID)
	}
	storedLock.Burned = true
	storedLock.BurnedAt = sanitizeUnix(settledAt)
	if err := s.store.KVPut(stableEscrowLockKey(intentID), storedLock); err != nil {
		return err
	}
	if nhbAmount.Sign() > 0 {
		delta := new(big.Int).Neg(new(big.Int).Set(nhbAmount))
		if _, err := s.store.AdjustTokenSupply("NHB", delta); err != nil {
			return err
		}
	}
	// Mark intent settled.
	intent.Status = CashOutStatusSettled
	intent.SettledAt = settledAt
	storedIntent := storedCashOutIntent{
		IntentID:     intent.IntentID,
		InvoiceID:    intent.InvoiceID,
		Account:      intent.Account,
		StableAsset:  string(intent.StableAsset),
		StableAmount: intent.StableAmount.String(),
		NhbAmount:    intent.NhbAmount.String(),
		CreatedAt:    sanitizeUnix(intent.CreatedAt),
		Status:       string(intent.Status),
		EscrowLockID: intent.EscrowLockID,
		SettledAt:    sanitizeUnix(settledAt),
	}
	if err := s.store.KVPut(stableCashOutIntentKey(intentID), storedIntent); err != nil {
		return err
	}
	storedReceipt := storedPayoutReceipt{
		ReceiptID:    strings.TrimSpace(receipt.ReceiptID),
		IntentID:     intentID,
		StableAsset:  string(asset),
		StableAmount: stableAmount.String(),
		NhbAmount:    nhbAmount.String(),
		TxHash:       strings.TrimSpace(receipt.TxHash),
		EvidenceURI:  strings.TrimSpace(receipt.EvidenceURI),
		SettledAt:    sanitizeUnix(settledAt),
	}
	return s.store.KVPut(stablePayoutReceiptKey(intentID), storedReceipt)
}

// GetPayoutReceipt fetches the payout receipt for an intent if present.
func (s *StableStore) GetPayoutReceipt(intentID string) (*PayoutReceipt, bool, error) {
	if s == nil {
		return nil, false, fmt.Errorf("stable: store not initialised")
	}
	var stored storedPayoutReceipt
	ok, err := s.store.KVGet(stablePayoutReceiptKey(intentID), &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	receipt, err := toPayoutReceipt(&stored)
	if err != nil {
		return nil, false, err
	}
	return receipt, true, nil
}

// GetSoftInventory returns the current treasury soft inventory for the asset.
func (s *StableStore) GetSoftInventory(asset StableAsset) (*TreasurySoftInventory, error) {
	if s == nil {
		return nil, fmt.Errorf("stable: store not initialised")
	}
	normalised, err := normaliseAsset(asset)
	if err != nil {
		return nil, err
	}
	var stored storedSoftInventory
	ok, err := s.store.KVGet(stableInventoryKey(string(normalised)), &stored)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &TreasurySoftInventory{
			StableAsset: normalised,
			Deposits:    big.NewInt(0),
			Payouts:     big.NewInt(0),
			Balance:     big.NewInt(0),
		}, nil
	}
	inventory, err := toSoftInventory(&stored)
	if err != nil {
		return nil, err
	}
	return inventory, nil
}

func (s *StableStore) adjustSoftInventory(asset StableAsset, depositDelta, payoutDelta *big.Int) error {
	normalised, err := normaliseAsset(asset)
	if err != nil {
		return err
	}
	var stored storedSoftInventory
	key := stableInventoryKey(string(normalised))
	ok, err := s.store.KVGet(key, &stored)
	if err != nil {
		return err
	}
	var deposits, payouts *big.Int
	if ok {
		deposits, err = parseAmount(stored.Deposits)
		if err != nil {
			return fmt.Errorf("stable: corrupted deposits for %s", asset)
		}
		payouts, err = parseAmount(stored.Payouts)
		if err != nil {
			return fmt.Errorf("stable: corrupted payouts for %s", asset)
		}
	} else {
		deposits = big.NewInt(0)
		payouts = big.NewInt(0)
	}
	if depositDelta != nil {
		if depositDelta.Sign() < 0 {
			return fmt.Errorf("stable: deposit delta must be positive")
		}
		deposits = new(big.Int).Add(deposits, depositDelta)
	}
	if payoutDelta != nil {
		if payoutDelta.Sign() < 0 {
			return fmt.Errorf("stable: payout delta must be positive")
		}
		payouts = new(big.Int).Add(payouts, payoutDelta)
	}
	if payouts.Cmp(deposits) > 0 {
		return fmt.Errorf("stable: payout exceeds deposits for %s", asset)
	}
	balance := new(big.Int).Sub(deposits, payouts)
	updatedAt := s.clock().UTC().Unix()
	if updatedAt < 0 {
		updatedAt = 0
	}
	stored = storedSoftInventory{
		StableAsset: string(normalised),
		Deposits:    deposits.String(),
		Payouts:     payouts.String(),
		Balance:     balance.String(),
		UpdatedAt:   uint64(updatedAt),
	}
	return s.store.KVPut(key, stored)
}

func toDepositVoucher(stored *storedDepositVoucher) (*DepositVoucher, error) {
	if stored == nil {
		return nil, fmt.Errorf("stable: stored voucher nil")
	}
	amount, err := parseAmount(stored.StableAmount)
	if err != nil {
		return nil, err
	}
	nhb, err := parseAmount(stored.NhbAmount)
	if err != nil {
		return nil, err
	}
	created := int64(stored.CreatedAt)
	return &DepositVoucher{
		InvoiceID:    strings.TrimSpace(stored.InvoiceID),
		Provider:     strings.TrimSpace(stored.Provider),
		StableAsset:  StableAsset(strings.TrimSpace(stored.StableAsset)),
		StableAmount: amount,
		NhbAmount:    nhb,
		Account:      strings.TrimSpace(stored.Account),
		Memo:         strings.TrimSpace(stored.Memo),
		CreatedAt:    created,
	}, nil
}

func toCashOutIntent(stored *storedCashOutIntent) (*CashOutIntent, error) {
	if stored == nil {
		return nil, fmt.Errorf("stable: stored intent nil")
	}
	stableAmount, err := parseAmount(stored.StableAmount)
	if err != nil {
		return nil, err
	}
	nhbAmount, err := parseAmount(stored.NhbAmount)
	if err != nil {
		return nil, err
	}
	return &CashOutIntent{
		IntentID:     strings.TrimSpace(stored.IntentID),
		InvoiceID:    strings.TrimSpace(stored.InvoiceID),
		Account:      strings.TrimSpace(stored.Account),
		StableAsset:  StableAsset(strings.TrimSpace(stored.StableAsset)),
		StableAmount: stableAmount,
		NhbAmount:    nhbAmount,
		CreatedAt:    int64(stored.CreatedAt),
		Status:       CashOutStatus(strings.TrimSpace(stored.Status)),
		EscrowLockID: strings.TrimSpace(stored.EscrowLockID),
		SettledAt:    int64(stored.SettledAt),
	}, nil
}

func toEscrowLock(stored *storedEscrowLock) (*EscrowLock, error) {
	if stored == nil {
		return nil, fmt.Errorf("stable: stored lock nil")
	}
	nhb, err := parseAmount(stored.NhbAmount)
	if err != nil {
		return nil, err
	}
	return &EscrowLock{
		LockID:    strings.TrimSpace(stored.LockID),
		IntentID:  strings.TrimSpace(stored.IntentID),
		Account:   strings.TrimSpace(stored.Account),
		NhbAmount: nhb,
		LockedAt:  int64(stored.LockedAt),
		Burned:    stored.Burned,
		BurnedAt:  int64(stored.BurnedAt),
	}, nil
}

func toPayoutReceipt(stored *storedPayoutReceipt) (*PayoutReceipt, error) {
	if stored == nil {
		return nil, fmt.Errorf("stable: stored receipt nil")
	}
	stableAmount, err := parseAmount(stored.StableAmount)
	if err != nil {
		return nil, err
	}
	nhbAmount, err := parseAmount(stored.NhbAmount)
	if err != nil {
		return nil, err
	}
	return &PayoutReceipt{
		ReceiptID:    strings.TrimSpace(stored.ReceiptID),
		IntentID:     strings.TrimSpace(stored.IntentID),
		StableAsset:  StableAsset(strings.TrimSpace(stored.StableAsset)),
		StableAmount: stableAmount,
		NhbAmount:    nhbAmount,
		TxHash:       strings.TrimSpace(stored.TxHash),
		EvidenceURI:  strings.TrimSpace(stored.EvidenceURI),
		SettledAt:    int64(stored.SettledAt),
	}, nil
}

func toSoftInventory(stored *storedSoftInventory) (*TreasurySoftInventory, error) {
	if stored == nil {
		return nil, fmt.Errorf("stable: stored inventory nil")
	}
	deposits, err := parseAmount(stored.Deposits)
	if err != nil {
		return nil, err
	}
	payouts, err := parseAmount(stored.Payouts)
	if err != nil {
		return nil, err
	}
	balance, err := parseAmount(stored.Balance)
	if err != nil {
		return nil, err
	}
	return &TreasurySoftInventory{
		StableAsset: StableAsset(strings.TrimSpace(stored.StableAsset)),
		Deposits:    deposits,
		Payouts:     payouts,
		Balance:     balance,
		UpdatedAt:   int64(stored.UpdatedAt),
	}, nil
}

func normaliseAsset(asset StableAsset) (StableAsset, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(string(asset)))
	if trimmed == "" {
		return "", fmt.Errorf("stable: asset required")
	}
	switch StableAsset(trimmed) {
	case StableAssetUSDC, StableAssetUSDT:
		return StableAsset(trimmed), nil
	default:
		return "", fmt.Errorf("stable: unsupported asset %s", trimmed)
	}
}

func ensurePositiveAmount(amount *big.Int) (*big.Int, error) {
	if amount == nil {
		return nil, fmt.Errorf("amount required")
	}
	if amount.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	return new(big.Int).Set(amount), nil
}

func parseAmount(value string) (*big.Int, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return big.NewInt(0), nil
	}
	amount, ok := new(big.Int).SetString(trimmed, 10)
	if !ok {
		return nil, fmt.Errorf("invalid integer amount %q", value)
	}
	if amount.Sign() < 0 {
		return nil, fmt.Errorf("amount must not be negative")
	}
	return amount, nil
}

func sanitizeUnix(value int64) uint64 {
	if value < 0 {
		return 0
	}
	return uint64(value)
}
