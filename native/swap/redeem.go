package swap

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/rlp"
)

var (
	burnReceiptPrefix = []byte("swap/burn/")
	burnIndexKey      = []byte("swap/burn/index")
)

// BurnReceipt captures a burn-for-redeem record emitted by the off-ramp.
// ReceiptID is the primary key and typically corresponds to the custody burn
// transaction hash or an operator supplied identifier.
type BurnReceipt struct {
	ReceiptID       string
	ProviderTxID    string
	Token           string
	AmountWei       *big.Int
	Burner          [20]byte
	RedeemReference string
	BurnTxHash      string
	TreasuryTxID    string
	VoucherIDs      []string
	ObservedAt      int64
	Notes           string
}

// Copy returns a deep copy of the receipt for defensive use by callers.
func (r *BurnReceipt) Copy() *BurnReceipt {
	if r == nil {
		return nil
	}
	clone := *r
	if r.AmountWei != nil {
		clone.AmountWei = new(big.Int).Set(r.AmountWei)
	}
	clone.VoucherIDs = append([]string{}, r.VoucherIDs...)
	return &clone
}

type storedBurnReceipt struct {
	ReceiptID       string
	ProviderTxID    string
	Token           string
	AmountWei       string
	Burner          [20]byte
	RedeemReference string
	BurnTxHash      string
	TreasuryTxID    string
	VoucherIDs      []string
	ObservedAt      uint64
	Notes           string
}

type burnIndexEntry struct {
	ReceiptID string
	Observed  uint64
}

// BurnLedger manages burn receipts within storage.
type BurnLedger struct {
	store Storage
	clock func() time.Time
}

// NewBurnLedger constructs a burn ledger bound to the provided storage backend.
func NewBurnLedger(store Storage) *BurnLedger {
	return &BurnLedger{store: store, clock: time.Now}
}

// SetClock overrides the wall-clock used for timestamping receipts.
func (l *BurnLedger) SetClock(clock func() time.Time) {
	if l == nil || clock == nil {
		return
	}
	l.clock = clock
}

// Put persists the burn receipt, enforcing unique receipt identifiers.
func (l *BurnLedger) Put(receipt *BurnReceipt) error {
	if l == nil {
		return fmt.Errorf("burn ledger not initialised")
	}
	if receipt == nil {
		return fmt.Errorf("burn ledger: receipt must not be nil")
	}
	key := burnKey(receipt.ReceiptID)
	if len(key) == len(burnReceiptPrefix) {
		return fmt.Errorf("burn ledger: receiptId required")
	}
	var existing storedBurnReceipt
	ok, err := l.store.KVGet(key, &existing)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("burn ledger: receipt %s already exists", strings.TrimSpace(receipt.ReceiptID))
	}
	stored := toStoredBurn(receipt)
	if stored.ObservedAt == 0 {
		now := l.clock().UTC().Unix()
		if now < 0 {
			stored.ObservedAt = 0
		} else {
			stored.ObservedAt = uint64(now)
		}
	}
	if err := l.store.KVPut(key, stored); err != nil {
		return err
	}
	entry := burnIndexEntry{ReceiptID: stored.ReceiptID, Observed: stored.ObservedAt}
	encoded, err := rlp.EncodeToBytes(entry)
	if err != nil {
		return err
	}
	return l.store.KVAppend(burnIndexKey, encoded)
}

// Get retrieves a burn receipt by identifier.
func (l *BurnLedger) Get(receiptID string) (*BurnReceipt, bool, error) {
	if l == nil {
		return nil, false, fmt.Errorf("burn ledger not initialised")
	}
	key := burnKey(receiptID)
	var stored storedBurnReceipt
	ok, err := l.store.KVGet(key, &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	receipt, err := fromStoredBurn(&stored)
	if err != nil {
		return nil, false, err
	}
	return receipt, true, nil
}

// List returns burn receipts within the supplied inclusive time range.
func (l *BurnLedger) List(startTs, endTs int64, cursor string, limit int) ([]*BurnReceipt, string, error) {
	if l == nil {
		return nil, "", fmt.Errorf("burn ledger not initialised")
	}
	if limit <= 0 {
		limit = 50
	}
	entries, err := l.loadIndex()
	if err != nil {
		return nil, "", err
	}
	filtered := make([]burnIndexEntry, 0, len(entries))
	for _, entry := range entries {
		observed, err := uint64ToInt64(entry.Observed)
		if err != nil {
			return nil, "", fmt.Errorf("burn ledger: index overflow: %w", err)
		}
		if startTs != 0 && observed < startTs {
			continue
		}
		if endTs != 0 && observed > endTs {
			continue
		}
		filtered = append(filtered, entry)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].Observed == filtered[j].Observed {
			return filtered[i].ReceiptID < filtered[j].ReceiptID
		}
		return filtered[i].Observed < filtered[j].Observed
	})
	startIdx := 0
	trimmedCursor := strings.TrimSpace(cursor)
	if trimmedCursor != "" {
		for i, entry := range filtered {
			if entry.ReceiptID == trimmedCursor {
				startIdx = i + 1
				break
			}
		}
	}
	receipts := make([]*BurnReceipt, 0, minInt(limit, len(filtered)-startIdx))
	nextCursor := ""
	for i := startIdx; i < len(filtered) && len(receipts) < limit; i++ {
		entry := filtered[i]
		receipt, ok, err := l.Get(entry.ReceiptID)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		receipts = append(receipts, receipt)
		nextCursor = entry.ReceiptID
	}
	if startIdx+len(receipts) >= len(filtered) {
		nextCursor = ""
	}
	return receipts, nextCursor, nil
}

func (l *BurnLedger) loadIndex() ([]burnIndexEntry, error) {
	var raw [][]byte
	if err := l.store.KVGetList(burnIndexKey, &raw); err != nil {
		return nil, err
	}
	entries := make([]burnIndexEntry, 0, len(raw))
	for _, encoded := range raw {
		if len(encoded) == 0 {
			continue
		}
		var entry burnIndexEntry
		if err := rlp.DecodeBytes(encoded, &entry); err != nil {
			return nil, err
		}
		if strings.TrimSpace(entry.ReceiptID) == "" {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func burnKey(receiptID string) []byte {
	trimmed := strings.TrimSpace(receiptID)
	buf := make([]byte, len(burnReceiptPrefix)+len(trimmed))
	copy(buf, burnReceiptPrefix)
	copy(buf[len(burnReceiptPrefix):], trimmed)
	return buf
}

func toStoredBurn(receipt *BurnReceipt) storedBurnReceipt {
	stored := storedBurnReceipt{}
	if receipt == nil {
		return stored
	}
	stored.ReceiptID = strings.TrimSpace(receipt.ReceiptID)
	stored.ProviderTxID = strings.TrimSpace(receipt.ProviderTxID)
	stored.Token = strings.TrimSpace(receipt.Token)
	if receipt.AmountWei != nil {
		stored.AmountWei = receipt.AmountWei.String()
	}
	stored.Burner = receipt.Burner
	stored.RedeemReference = strings.TrimSpace(receipt.RedeemReference)
	stored.BurnTxHash = strings.TrimSpace(receipt.BurnTxHash)
	stored.TreasuryTxID = strings.TrimSpace(receipt.TreasuryTxID)
	stored.Notes = strings.TrimSpace(receipt.Notes)
	stored.VoucherIDs = append([]string{}, receipt.VoucherIDs...)
	if receipt.ObservedAt > 0 {
		stored.ObservedAt = uint64(receipt.ObservedAt)
	}
	return stored
}

func fromStoredBurn(stored *storedBurnReceipt) (*BurnReceipt, error) {
	if stored == nil {
		return nil, fmt.Errorf("burn ledger: nil stored receipt")
	}
	observed, err := uint64ToInt64(stored.ObservedAt)
	if err != nil {
		return nil, fmt.Errorf("burn ledger: observedAt overflow: %w", err)
	}
	receipt := &BurnReceipt{
		ReceiptID:       stored.ReceiptID,
		ProviderTxID:    stored.ProviderTxID,
		Token:           stored.Token,
		Burner:          stored.Burner,
		RedeemReference: stored.RedeemReference,
		BurnTxHash:      stored.BurnTxHash,
		TreasuryTxID:    stored.TreasuryTxID,
		VoucherIDs:      append([]string{}, stored.VoucherIDs...),
		ObservedAt:      observed,
		Notes:           stored.Notes,
	}
	if strings.TrimSpace(stored.AmountWei) != "" {
		amount, ok := new(big.Int).SetString(strings.TrimSpace(stored.AmountWei), 10)
		if !ok {
			return nil, fmt.Errorf("burn ledger: invalid amount %q", stored.AmountWei)
		}
		receipt.AmountWei = amount
	} else {
		receipt.AmountWei = big.NewInt(0)
	}
	return receipt, nil
}

// BurnReceiptCSVHeader exposes the canonical CSV header for burn receipts.
var BurnReceiptCSVHeader = []string{"receiptId", "providerTxId", "token", "amountWei", "burner", "redeemRef", "burnTx", "treasuryTx", "vouchers", "observedAt", "notes"}

// ExportCSV renders burn receipts matching the supplied window as base64 CSV.
func (l *BurnLedger) ExportCSV(startTs, endTs int64) (string, int, error) {
	if l == nil {
		return "", 0, fmt.Errorf("burn ledger not initialised")
	}
	receipts, _, err := l.List(startTs, endTs, "", 0)
	if err != nil {
		return "", 0, err
	}
	builder := &strings.Builder{}
	builder.WriteString(strings.Join(BurnReceiptCSVHeader, ","))
	builder.WriteString("\n")
	for _, receipt := range receipts {
		row := []string{
			strings.TrimSpace(receipt.ReceiptID),
			strings.TrimSpace(receipt.ProviderTxID),
			strings.TrimSpace(receipt.Token),
			mintAmountToString(receipt.AmountWei),
			hex.EncodeToString(receipt.Burner[:]),
			strings.TrimSpace(receipt.RedeemReference),
			strings.TrimSpace(receipt.BurnTxHash),
			strings.TrimSpace(receipt.TreasuryTxID),
			strings.Join(receipt.VoucherIDs, ";"),
			strconv.FormatInt(max64(receipt.ObservedAt, 0), 10),
			strings.TrimSpace(receipt.Notes),
		}
		builder.WriteString(strings.Join(row, ","))
		builder.WriteString("\n")
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(builder.String()))
	return encoded, len(receipts), nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
