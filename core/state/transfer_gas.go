package state

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const (
	TransferGasWindowLifetime = "lifetime"
	TransferGasWindowMonthly  = "monthly"
)

type storedTransferGasSpend struct {
	Window string
	Spent  *big.Int
}

// TransferGasSpendStatus captures the tracked NHB spend used to determine
// whether a wallet is still eligible for transfer gas sponsorship.
type TransferGasSpendStatus struct {
	Wallet     [20]byte
	Window     string
	WindowKey  string
	Spent      *big.Int
	FreeLimit  *big.Int
	Remaining  *big.Int
	Eligible   bool
	NextReset  time.Time
	RecordedAt time.Time
}

func normalizeTransferGasWindow(window string) string {
	switch strings.ToLower(strings.TrimSpace(window)) {
	case TransferGasWindowMonthly:
		return TransferGasWindowMonthly
	default:
		return TransferGasWindowLifetime
	}
}

func transferGasWindowKey(window string, now time.Time) string {
	switch normalizeTransferGasWindow(window) {
	case TransferGasWindowMonthly:
		return monthKey(now)
	default:
		return TransferGasWindowLifetime
	}
}

func transferGasNextReset(window string, now time.Time) time.Time {
	switch normalizeTransferGasWindow(window) {
	case TransferGasWindowMonthly:
		start := monthStartUTC(now)
		if start.IsZero() {
			return time.Time{}
		}
		return start.AddDate(0, 1, 0)
	default:
		return time.Time{}
	}
}

func normalizeTransferGasAsset(asset string) string {
	trimmed := strings.ToUpper(strings.TrimSpace(asset))
	if trimmed == "" {
		return "NHB"
	}
	return trimmed
}

// transferGasSpendKey is asset-scoped (docs/issue30.md item 7 ZNHB
// equivalent) so NHB and ZNHB free-tier spend are tracked independently --
// they are different tokens with different (pegged vs. floating) value, not
// fungible for this purpose.
func transferGasSpendKey(wallet [20]byte, window, windowKey, asset string) []byte {
	normalizedWindow := normalizeTransferGasWindow(window)
	normalizedAsset := normalizeTransferGasAsset(asset)
	trimmedKey := strings.TrimSpace(windowKey)
	hexAddr := hex.EncodeToString(wallet[:])
	buf := make([]byte, len(transferGasSpendPrefix)+len(normalizedAsset)+1+len(normalizedWindow)+1+len(trimmedKey)+1+len(hexAddr))
	copy(buf, transferGasSpendPrefix)
	offset := len(transferGasSpendPrefix)
	copy(buf[offset:], normalizedAsset)
	offset += len(normalizedAsset)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], normalizedWindow)
	offset += len(normalizedWindow)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], trimmedKey)
	offset += len(trimmedKey)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], hexAddr)
	return buf
}

func cloneBig(value *big.Int) *big.Int {
	if value == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(value)
}

// TransferGasSpendStatus returns the currently tracked spend snapshot for the
// wallet under the supplied sponsorship window, scoped to asset ("NHB" or
// "ZNHB" -- see docs/issue30.md item 7's ZNHB equivalent). NHB and ZNHB spend
// are tracked independently since they are different tokens.
func (m *Manager) TransferGasSpendStatus(wallet [20]byte, window string, now time.Time, freeLimit *big.Int, asset string) (TransferGasSpendStatus, error) {
	if m == nil {
		return TransferGasSpendStatus{}, fmt.Errorf("fees: state manager not initialised")
	}
	normalizedWindow := normalizeTransferGasWindow(window)
	windowKey := transferGasWindowKey(normalizedWindow, now)
	status := TransferGasSpendStatus{
		Wallet:    wallet,
		Window:    normalizedWindow,
		WindowKey: windowKey,
		Spent:     big.NewInt(0),
		FreeLimit: cloneBig(freeLimit),
		NextReset: transferGasNextReset(normalizedWindow, now),
	}
	var stored storedTransferGasSpend
	ok, err := m.KVGet(transferGasSpendKey(wallet, normalizedWindow, windowKey, asset), &stored)
	if err != nil {
		return TransferGasSpendStatus{}, fmt.Errorf("fees: load transfer gas spend: %w", err)
	}
	if ok && stored.Spent != nil {
		status.Spent = new(big.Int).Set(stored.Spent)
	}
	status.Remaining = big.NewInt(0)
	if status.FreeLimit.Sign() > 0 && status.Spent.Cmp(status.FreeLimit) < 0 {
		status.Remaining = new(big.Int).Sub(status.FreeLimit, status.Spent)
		status.Eligible = true
	}
	return status, nil
}

// TransferGasSpendAdd records additional spend for the wallet in the active
// sponsorship window and returns the updated snapshot, scoped to asset.
func (m *Manager) TransferGasSpendAdd(wallet [20]byte, window string, now time.Time, amount, freeLimit *big.Int, asset string) (TransferGasSpendStatus, error) {
	status, err := m.TransferGasSpendStatus(wallet, window, now, freeLimit, asset)
	if err != nil {
		return TransferGasSpendStatus{}, err
	}
	if amount != nil && amount.Sign() > 0 {
		status.Spent.Add(status.Spent, amount)
	}
	stored := storedTransferGasSpend{
		Window: status.WindowKey,
		Spent:  new(big.Int).Set(status.Spent),
	}
	if err := m.KVPut(transferGasSpendKey(wallet, status.Window, status.WindowKey, asset), &stored); err != nil {
		return TransferGasSpendStatus{}, fmt.Errorf("fees: store transfer gas spend: %w", err)
	}
	status.RecordedAt = now.UTC()
	status.Remaining = big.NewInt(0)
	status.Eligible = false
	if status.FreeLimit.Sign() > 0 && status.Spent.Cmp(status.FreeLimit) < 0 {
		status.Remaining = new(big.Int).Sub(status.FreeLimit, status.Spent)
		status.Eligible = true
	}
	return status, nil
}
