package swap

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/rlp"
)

// Storage abstracts the subset of state manager functionality required by the
// voucher ledger.
type Storage interface {
	KVGet(key []byte, out interface{}) (bool, error)
	KVPut(key []byte, value interface{}) error
	KVAppend(key []byte, value []byte) error
	KVGetList(key []byte, out interface{}) error
}

var (
	voucherRecordPrefix = []byte("swap/voucher/")
	voucherIndexKey     = []byte("swap/voucher/index")
)

// Voucher statuses recorded within the ledger.
const (
	VoucherStatusMinted     = "minted"
	VoucherStatusReconciled = "reconciled"
	VoucherStatusReversed   = "reversed"
)

// VoucherRecord captures the metadata stored for every voucher processed by the
// mint pipeline.
type VoucherRecord struct {
	Provider        string
	ProviderTxID    string
	FiatCurrency    string
	FiatAmount      string
	USD             string
	Rate            string
	Token           string
	MintAmountWei   *big.Int
	Recipient       [20]byte
	Username        string
	Address         string
	QuoteTimestamp  int64
	OracleSource    string
	MinterSignature string
	Status          string
	CreatedAt       int64
}

// Copy returns a deep copy to avoid callers mutating shared pointers.
func (v *VoucherRecord) Copy() *VoucherRecord {
	if v == nil {
		return nil
	}
	clone := *v
	if v.MintAmountWei != nil {
		clone.MintAmountWei = new(big.Int).Set(v.MintAmountWei)
	}
	return &clone
}

type storedVoucherRecord struct {
	Provider        string
	ProviderTxID    string
	FiatCurrency    string
	FiatAmount      string
	USD             string
	Rate            string
	Token           string
	MintAmountWei   string
	Recipient       [20]byte
	Username        string
	Address         string
	QuoteTimestamp  uint64
	OracleSource    string
	MinterSignature string
	Status          string
	CreatedAt       uint64
}

type voucherIndexEntry struct {
	ProviderTxID string
	CreatedAt    uint64
}

// Ledger persists voucher records in the underlying key-value store.
type Ledger struct {
	store Storage
	clock func() time.Time
}

// NewLedger constructs a ledger bound to the provided storage backend.
func NewLedger(store Storage) *Ledger {
	return &Ledger{store: store, clock: time.Now}
}

// SetClock overrides the time source (primarily for deterministic testing).
func (l *Ledger) SetClock(clock func() time.Time) {
	if l == nil || clock == nil {
		return
	}
	l.clock = clock
}

// Put stores the voucher record, enforcing append-only semantics keyed by the
// provider transaction identifier.
func (l *Ledger) Put(record *VoucherRecord) error {
	if l == nil {
		return fmt.Errorf("ledger not initialised")
	}
	if record == nil {
		return fmt.Errorf("ledger: record must not be nil")
	}
	key := voucherKey(record.ProviderTxID)
	if len(key) == len(voucherRecordPrefix) {
		return fmt.Errorf("ledger: providerTxId required")
	}
	var existing storedVoucherRecord
	ok, err := l.store.KVGet(key, &existing)
	if err != nil {
		return err
	}
	if ok {
		return fmt.Errorf("ledger: voucher %s already exists", record.ProviderTxID)
	}
	stored := toStoredVoucher(record)
	if stored.CreatedAt == 0 {
		now := l.clock().UTC().Unix()
		if now < 0 {
			stored.CreatedAt = 0
		} else {
			stored.CreatedAt = uint64(now)
		}
	}
	if stored.Status == "" {
		stored.Status = VoucherStatusMinted
	}
	if err := l.store.KVPut(key, stored); err != nil {
		return err
	}
	if _, err := uint64ToInt64(stored.CreatedAt); err != nil {
		return fmt.Errorf("ledger: created at overflow: %w", err)
	}
	entry := voucherIndexEntry{ProviderTxID: stored.ProviderTxID, CreatedAt: stored.CreatedAt}
	encoded, err := rlp.EncodeToBytes(entry)
	if err != nil {
		return err
	}
	return l.store.KVAppend(voucherIndexKey, encoded)
}

// Exists reports whether a voucher with the supplied provider identifier has been
// recorded in the ledger.
func (l *Ledger) Exists(providerTxID string) (bool, error) {
	if l == nil {
		return false, fmt.Errorf("ledger not initialised")
	}
	key := voucherKey(providerTxID)
	var stored storedVoucherRecord
	ok, err := l.store.KVGet(key, &stored)
	if err != nil {
		return false, err
	}
	return ok, nil
}

// Get retrieves a voucher record by provider transaction identifier.
func (l *Ledger) Get(providerTxID string) (*VoucherRecord, bool, error) {
	if l == nil {
		return nil, false, fmt.Errorf("ledger not initialised")
	}
	key := voucherKey(providerTxID)
	var stored storedVoucherRecord
	ok, err := l.store.KVGet(key, &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	record, err := fromStoredVoucher(&stored)
	if err != nil {
		return nil, false, err
	}
	return record, true, nil
}

// List returns a paginated list of voucher records within the supplied timestamp
// range. Both bounds are inclusive. The cursor is the provider transaction ID of
// the last item from the previous page.
func (l *Ledger) List(startTs, endTs int64, cursor string, limit int) ([]*VoucherRecord, string, error) {
	if l == nil {
		return nil, "", fmt.Errorf("ledger not initialised")
	}
	if limit <= 0 {
		limit = 50
	}
	entries, err := l.loadIndex()
	if err != nil {
		return nil, "", err
	}
	filtered := make([]voucherIndexEntry, 0, len(entries))
	for _, entry := range entries {
		createdAt, err := uint64ToInt64(entry.CreatedAt)
		if err != nil {
			return nil, "", fmt.Errorf("ledger: index entry overflow: %w", err)
		}
		if startTs != 0 && createdAt < startTs {
			continue
		}
		if endTs != 0 && createdAt > endTs {
			continue
		}
		filtered = append(filtered, entry)
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].CreatedAt == filtered[j].CreatedAt {
			return filtered[i].ProviderTxID < filtered[j].ProviderTxID
		}
		return filtered[i].CreatedAt < filtered[j].CreatedAt
	})
	startIdx := 0
	cursorID := strings.TrimSpace(cursor)
	if cursorID != "" {
		for i, entry := range filtered {
			if entry.ProviderTxID == cursorID {
				startIdx = i + 1
				break
			}
		}
	}
	nextCursor := ""
	records := make([]*VoucherRecord, 0, min(limit, len(filtered)-startIdx))
	for i := startIdx; i < len(filtered) && len(records) < limit; i++ {
		entry := filtered[i]
		record, ok, err := l.Get(entry.ProviderTxID)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			continue
		}
		records = append(records, record)
		nextCursor = entry.ProviderTxID
	}
	if startIdx+len(records) >= len(filtered) {
		nextCursor = ""
	}
	return records, nextCursor, nil
}

// ExportCSV generates a deterministic CSV export covering the provided timestamp
// window. The CSV is returned as a base64 encoded string alongside the entry count
// and total minted amount in wei.
func (l *Ledger) ExportCSV(startTs, endTs int64) (string, int, *big.Int, error) {
	if l == nil {
		return "", 0, nil, fmt.Errorf("ledger not initialised")
	}
	entries, _, err := l.List(startTs, endTs, "", 0)
	if err != nil {
		return "", 0, nil, err
	}
	buf := &bytes.Buffer{}
	writer := csv.NewWriter(buf)
	header := []string{"providerTxId", "provider", "fiatCurrency", "fiatAmount", "usd", "rate", "token", "mintAmountWei", "recipient", "username", "address", "quoteTs", "source", "minterSig", "status", "createdAt"}
	if err := writer.Write(header); err != nil {
		return "", 0, nil, err
	}
	total := big.NewInt(0)
	for _, record := range entries {
		if record.MintAmountWei != nil {
			total = new(big.Int).Add(total, record.MintAmountWei)
		}
		row := []string{
			record.ProviderTxID,
			record.Provider,
			record.FiatCurrency,
			record.FiatAmount,
			record.USD,
			record.Rate,
			record.Token,
			mintAmountToString(record.MintAmountWei),
			hex.EncodeToString(record.Recipient[:]),
			record.Username,
			record.Address,
			strconv.FormatInt(record.QuoteTimestamp, 10),
			record.OracleSource,
			record.MinterSignature,
			record.Status,
			strconv.FormatInt(record.CreatedAt, 10),
		}
		if err := writer.Write(row); err != nil {
			return "", 0, nil, err
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", 0, nil, err
	}
	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	return encoded, len(entries), total, nil
}

// MarkReconciled updates the status of the supplied vouchers to "reconciled".
func (l *Ledger) MarkReconciled(ids []string) error {
	if l == nil {
		return fmt.Errorf("ledger not initialised")
	}
	for _, id := range ids {
		key := voucherKey(id)
		var stored storedVoucherRecord
		ok, err := l.store.KVGet(key, &stored)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		stored.Status = VoucherStatusReconciled
		if err := l.store.KVPut(key, stored); err != nil {
			return err
		}
	}
	return nil
}

func (l *Ledger) loadIndex() ([]voucherIndexEntry, error) {
	var raw [][]byte
	if err := l.store.KVGetList(voucherIndexKey, &raw); err != nil {
		return nil, err
	}
	entries := make([]voucherIndexEntry, 0, len(raw))
	for _, encoded := range raw {
		if len(encoded) == 0 {
			continue
		}
		var entry voucherIndexEntry
		if err := rlp.DecodeBytes(encoded, &entry); err != nil {
			return nil, err
		}
		if strings.TrimSpace(entry.ProviderTxID) == "" {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func voucherKey(providerTxID string) []byte {
	trimmed := strings.TrimSpace(providerTxID)
	buf := make([]byte, len(voucherRecordPrefix)+len(trimmed))
	copy(buf, voucherRecordPrefix)
	copy(buf[len(voucherRecordPrefix):], trimmed)
	return buf
}

func toStoredVoucher(record *VoucherRecord) storedVoucherRecord {
	stored := storedVoucherRecord{}
	if record == nil {
		return stored
	}
	stored.Provider = strings.TrimSpace(record.Provider)
	stored.ProviderTxID = strings.TrimSpace(record.ProviderTxID)
	stored.FiatCurrency = strings.TrimSpace(record.FiatCurrency)
	stored.FiatAmount = strings.TrimSpace(record.FiatAmount)
	stored.USD = strings.TrimSpace(record.USD)
	stored.Rate = strings.TrimSpace(record.Rate)
	stored.Token = strings.TrimSpace(record.Token)
	if record.MintAmountWei != nil {
		stored.MintAmountWei = record.MintAmountWei.String()
	}
	stored.Recipient = record.Recipient
	stored.Username = strings.TrimSpace(record.Username)
	stored.Address = strings.TrimSpace(record.Address)
	if record.QuoteTimestamp < 0 {
		stored.QuoteTimestamp = 0
	} else {
		stored.QuoteTimestamp = uint64(record.QuoteTimestamp)
	}
	stored.OracleSource = strings.TrimSpace(record.OracleSource)
	stored.MinterSignature = strings.TrimSpace(record.MinterSignature)
	stored.Status = strings.TrimSpace(record.Status)
	if record.CreatedAt < 0 {
		stored.CreatedAt = 0
	} else {
		stored.CreatedAt = uint64(record.CreatedAt)
	}
	return stored
}

func fromStoredVoucher(stored *storedVoucherRecord) (*VoucherRecord, error) {
	if stored == nil {
		return nil, fmt.Errorf("ledger: nil stored record")
	}
	quoteTimestamp, err := uint64ToInt64(stored.QuoteTimestamp)
	if err != nil {
		return nil, fmt.Errorf("ledger: quote timestamp overflow: %w", err)
	}
	createdAt, err := uint64ToInt64(stored.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("ledger: created at overflow: %w", err)
	}
	record := &VoucherRecord{
		Provider:        stored.Provider,
		ProviderTxID:    stored.ProviderTxID,
		FiatCurrency:    stored.FiatCurrency,
		FiatAmount:      stored.FiatAmount,
		USD:             stored.USD,
		Rate:            stored.Rate,
		Token:           stored.Token,
		Recipient:       stored.Recipient,
		Username:        stored.Username,
		Address:         stored.Address,
		QuoteTimestamp:  quoteTimestamp,
		OracleSource:    stored.OracleSource,
		MinterSignature: stored.MinterSignature,
		Status:          stored.Status,
		CreatedAt:       createdAt,
	}
	if strings.TrimSpace(stored.MintAmountWei) != "" {
		amount, ok := new(big.Int).SetString(strings.TrimSpace(stored.MintAmountWei), 10)
		if !ok {
			return nil, fmt.Errorf("ledger: invalid amount %q", stored.MintAmountWei)
		}
		record.MintAmountWei = amount
	} else {
		record.MintAmountWei = big.NewInt(0)
	}
	return record, nil
}

func uint64ToInt64(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("value %d exceeds int64 range", value)
	}
	return int64(value), nil
}

func mintAmountToString(amount *big.Int) string {
	if amount == nil {
		return "0"
	}
	return amount.String()
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
