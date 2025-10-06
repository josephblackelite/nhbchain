package state

import (
	"fmt"
	"math/big"
	"strings"
	"time"
)

// PaymasterDayFormat defines the canonical layout for paymaster epoch days.
const PaymasterDayFormat = "2006-01-02"

var (
	paymasterDeviceDayPrefix   = []byte("paymaster/counter/device/")
	paymasterMerchantDayPrefix = []byte("paymaster/counter/merchant/")
	paymasterGlobalDayPrefix   = []byte("paymaster/counter/global/")
)

// PaymasterDeviceDay captures the per-device sponsorship usage metrics for a single UTC day.
type PaymasterDeviceDay struct {
	Merchant   string
	DeviceID   string
	Day        string
	TxCount    uint64
	BudgetWei  *big.Int
	ChargedWei *big.Int
}

// Clone returns a deep copy of the device day record.
func (p *PaymasterDeviceDay) Clone() *PaymasterDeviceDay {
	if p == nil {
		return nil
	}
	clone := &PaymasterDeviceDay{
		Merchant: NormalizePaymasterMerchant(p.Merchant),
		DeviceID: NormalizePaymasterDevice(p.DeviceID),
		Day:      NormalizePaymasterDay(p.Day),
		TxCount:  p.TxCount,
	}
	if p.BudgetWei != nil {
		clone.BudgetWei = new(big.Int).Set(p.BudgetWei)
	}
	if p.ChargedWei != nil {
		clone.ChargedWei = new(big.Int).Set(p.ChargedWei)
	}
	ensurePaymasterDeviceDefaults(clone)
	return clone
}

// PaymasterMerchantDay captures the per-merchant sponsorship usage metrics for a single UTC day.
type PaymasterMerchantDay struct {
	Merchant   string
	Day        string
	TxCount    uint64
	BudgetWei  *big.Int
	ChargedWei *big.Int
}

// Clone returns a deep copy of the merchant day record.
func (p *PaymasterMerchantDay) Clone() *PaymasterMerchantDay {
	if p == nil {
		return nil
	}
	clone := &PaymasterMerchantDay{
		Merchant: NormalizePaymasterMerchant(p.Merchant),
		Day:      NormalizePaymasterDay(p.Day),
		TxCount:  p.TxCount,
	}
	if p.BudgetWei != nil {
		clone.BudgetWei = new(big.Int).Set(p.BudgetWei)
	}
	if p.ChargedWei != nil {
		clone.ChargedWei = new(big.Int).Set(p.ChargedWei)
	}
	ensurePaymasterMerchantDefaults(clone)
	return clone
}

// PaymasterGlobalDay captures the global sponsorship usage for a single UTC day.
type PaymasterGlobalDay struct {
	Day        string
	TxCount    uint64
	BudgetWei  *big.Int
	ChargedWei *big.Int
}

// Clone returns a deep copy of the global day record.
func (p *PaymasterGlobalDay) Clone() *PaymasterGlobalDay {
	if p == nil {
		return nil
	}
	clone := &PaymasterGlobalDay{
		Day:     NormalizePaymasterDay(p.Day),
		TxCount: p.TxCount,
	}
	if p.BudgetWei != nil {
		clone.BudgetWei = new(big.Int).Set(p.BudgetWei)
	}
	if p.ChargedWei != nil {
		clone.ChargedWei = new(big.Int).Set(p.ChargedWei)
	}
	ensurePaymasterGlobalDefaults(clone)
	return clone
}

// NormalizePaymasterMerchant returns the canonical representation for the merchant identifier.
func NormalizePaymasterMerchant(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return strings.ToLower(trimmed)
}

// NormalizePaymasterDevice returns the canonical representation for the device identifier.
func NormalizePaymasterDevice(value string) string {
	return strings.TrimSpace(value)
}

// NormalizePaymasterDay normalises the day key to the canonical paymaster day format.
func NormalizePaymasterDay(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	if parsed, err := time.Parse(PaymasterDayFormat, trimmed); err == nil {
		return parsed.UTC().Format(PaymasterDayFormat)
	}
	return trimmed
}

func ensurePaymasterDeviceDefaults(record *PaymasterDeviceDay) {
	if record == nil {
		return
	}
	if record.BudgetWei == nil {
		record.BudgetWei = big.NewInt(0)
	}
	if record.ChargedWei == nil {
		record.ChargedWei = big.NewInt(0)
	}
	record.Merchant = NormalizePaymasterMerchant(record.Merchant)
	record.DeviceID = NormalizePaymasterDevice(record.DeviceID)
	record.Day = NormalizePaymasterDay(record.Day)
}

func ensurePaymasterMerchantDefaults(record *PaymasterMerchantDay) {
	if record == nil {
		return
	}
	if record.BudgetWei == nil {
		record.BudgetWei = big.NewInt(0)
	}
	if record.ChargedWei == nil {
		record.ChargedWei = big.NewInt(0)
	}
	record.Merchant = NormalizePaymasterMerchant(record.Merchant)
	record.Day = NormalizePaymasterDay(record.Day)
}

func ensurePaymasterGlobalDefaults(record *PaymasterGlobalDay) {
	if record == nil {
		return
	}
	if record.BudgetWei == nil {
		record.BudgetWei = big.NewInt(0)
	}
	if record.ChargedWei == nil {
		record.ChargedWei = big.NewInt(0)
	}
	record.Day = NormalizePaymasterDay(record.Day)
}

func paymasterDeviceDayKey(merchant, device, day string) []byte {
	merchantKey := NormalizePaymasterMerchant(merchant)
	deviceKey := NormalizePaymasterDevice(device)
	dayKey := NormalizePaymasterDay(day)
	buf := make([]byte, len(paymasterDeviceDayPrefix)+len(dayKey)+1+len(merchantKey)+1+len(deviceKey))
	copy(buf, paymasterDeviceDayPrefix)
	offset := len(paymasterDeviceDayPrefix)
	copy(buf[offset:], dayKey)
	offset += len(dayKey)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], merchantKey)
	offset += len(merchantKey)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], deviceKey)
	return buf
}

func paymasterMerchantDayKey(merchant, day string) []byte {
	merchantKey := NormalizePaymasterMerchant(merchant)
	dayKey := NormalizePaymasterDay(day)
	buf := make([]byte, len(paymasterMerchantDayPrefix)+len(dayKey)+1+len(merchantKey))
	copy(buf, paymasterMerchantDayPrefix)
	offset := len(paymasterMerchantDayPrefix)
	copy(buf[offset:], dayKey)
	offset += len(dayKey)
	buf[offset] = '/'
	offset++
	copy(buf[offset:], merchantKey)
	return buf
}

func paymasterGlobalDayKey(day string) []byte {
	dayKey := NormalizePaymasterDay(day)
	buf := make([]byte, len(paymasterGlobalDayPrefix)+len(dayKey))
	copy(buf, paymasterGlobalDayPrefix)
	copy(buf[len(paymasterGlobalDayPrefix):], dayKey)
	return buf
}

// PaymasterGetDeviceDay retrieves the device usage record for the given merchant and day.
func (m *Manager) PaymasterGetDeviceDay(merchant, device, day string) (*PaymasterDeviceDay, bool, error) {
	if m == nil {
		return nil, false, fmt.Errorf("state manager not initialised")
	}
	key := paymasterDeviceDayKey(merchant, device, day)
	var stored PaymasterDeviceDay
	ok, err := m.KVGet(key, &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	ensurePaymasterDeviceDefaults(&stored)
	return stored.Clone(), true, nil
}

// PaymasterPutDeviceDay stores the device usage record for the provided merchant and day.
func (m *Manager) PaymasterPutDeviceDay(record *PaymasterDeviceDay) error {
	if m == nil {
		return fmt.Errorf("state manager not initialised")
	}
	if record == nil {
		return fmt.Errorf("paymaster device record must not be nil")
	}
	ensurePaymasterDeviceDefaults(record)
	return m.KVPut(paymasterDeviceDayKey(record.Merchant, record.DeviceID, record.Day), record)
}

// PaymasterGetMerchantDay retrieves the merchant usage record for the provided day.
func (m *Manager) PaymasterGetMerchantDay(merchant, day string) (*PaymasterMerchantDay, bool, error) {
	if m == nil {
		return nil, false, fmt.Errorf("state manager not initialised")
	}
	key := paymasterMerchantDayKey(merchant, day)
	var stored PaymasterMerchantDay
	ok, err := m.KVGet(key, &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	ensurePaymasterMerchantDefaults(&stored)
	return stored.Clone(), true, nil
}

// PaymasterPutMerchantDay stores the merchant usage record for the provided day.
func (m *Manager) PaymasterPutMerchantDay(record *PaymasterMerchantDay) error {
	if m == nil {
		return fmt.Errorf("state manager not initialised")
	}
	if record == nil {
		return fmt.Errorf("paymaster merchant record must not be nil")
	}
	ensurePaymasterMerchantDefaults(record)
	return m.KVPut(paymasterMerchantDayKey(record.Merchant, record.Day), record)
}

// PaymasterGetGlobalDay retrieves the global usage record for the provided day.
func (m *Manager) PaymasterGetGlobalDay(day string) (*PaymasterGlobalDay, bool, error) {
	if m == nil {
		return nil, false, fmt.Errorf("state manager not initialised")
	}
	key := paymasterGlobalDayKey(day)
	var stored PaymasterGlobalDay
	ok, err := m.KVGet(key, &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	ensurePaymasterGlobalDefaults(&stored)
	return stored.Clone(), true, nil
}

// PaymasterPutGlobalDay stores the global usage record for the provided day.
func (m *Manager) PaymasterPutGlobalDay(record *PaymasterGlobalDay) error {
	if m == nil {
		return fmt.Errorf("state manager not initialised")
	}
	if record == nil {
		return fmt.Errorf("paymaster global record must not be nil")
	}
	ensurePaymasterGlobalDefaults(record)
	return m.KVPut(paymasterGlobalDayKey(record.Day), record)
}
