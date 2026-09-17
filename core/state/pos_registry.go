package state

import (
	"fmt"
	"strings"

	"nhbchain/native/pos"
)

var (
	posMerchantPrefix = []byte("pos/merchant/")
	posDevicePrefix   = []byte("pos/device/")
)

func posMerchantKey(addr string) []byte {
	return append([]byte(nil), append(posMerchantPrefix, []byte(addr)...)...)
}

func posDeviceKey(id string) []byte {
	return append([]byte(nil), append(posDevicePrefix, []byte(id)...)...)
}

// POSGetMerchant loads the merchant sponsorship record associated with the
// provided address. A missing record returns (nil, false, nil).
func (m *Manager) POSGetMerchant(addr string) (*pos.Merchant, bool, error) {
	if m == nil {
		return nil, false, fmt.Errorf("pos: state manager not initialised")
	}
	normalized := NormalizePaymasterMerchant(addr)
	if normalized == "" {
		return nil, false, nil
	}
	var stored pos.Merchant
	ok, err := m.KVGet(posMerchantKey(normalized), &stored)
	if err != nil || !ok {
		return nil, ok, err
	}
	stored.Address = NormalizePaymasterMerchant(stored.Address)
	stored.ChainID = strings.TrimSpace(stored.ChainID)
	return &stored, true, nil
}

// POSPutMerchant persists the provided merchant sponsorship record.
func (m *Manager) POSPutMerchant(record *pos.Merchant) error {
	if m == nil {
		return fmt.Errorf("pos: state manager not initialised")
	}
	if record == nil {
		return fmt.Errorf("pos: merchant record required")
	}
	normalized := NormalizePaymasterMerchant(record.Address)
	if normalized == "" {
		return fmt.Errorf("pos: merchant address required")
	}
	stored := &pos.Merchant{
		Address:   normalized,
		Paused:    record.Paused,
		Nonce:     record.Nonce,
		ExpiresAt: record.ExpiresAt,
		ChainID:   strings.TrimSpace(record.ChainID),
	}
	return m.KVPut(posMerchantKey(normalized), stored)
}

// POSDeleteMerchant removes the merchant record from storage.
func (m *Manager) POSDeleteMerchant(addr string) error {
	if m == nil {
		return fmt.Errorf("pos: state manager not initialised")
	}
	normalized := NormalizePaymasterMerchant(addr)
	if normalized == "" {
		return nil
	}
	return m.KVDelete(posMerchantKey(normalized))
}

// POSGetDevice loads the device sponsorship record associated with the
// identifier. A missing record returns (nil, false, nil).
func (m *Manager) POSGetDevice(id string) (*pos.Device, bool, error) {
	if m == nil {
		return nil, false, fmt.Errorf("pos: state manager not initialised")
	}
	normalized := NormalizePaymasterDevice(id)
	if normalized == "" {
		return nil, false, nil
	}
	var stored pos.Device
	ok, err := m.KVGet(posDeviceKey(normalized), &stored)
	if err != nil || !ok {
		return nil, ok, err
	}
	stored.DeviceID = NormalizePaymasterDevice(stored.DeviceID)
	stored.Merchant = NormalizePaymasterMerchant(stored.Merchant)
	stored.ChainID = strings.TrimSpace(stored.ChainID)
	return &stored, true, nil
}

// POSPutDevice persists the device sponsorship record.
func (m *Manager) POSPutDevice(record *pos.Device) error {
	if m == nil {
		return fmt.Errorf("pos: state manager not initialised")
	}
	if record == nil {
		return fmt.Errorf("pos: device record required")
	}
	normalizedID := NormalizePaymasterDevice(record.DeviceID)
	if normalizedID == "" {
		return fmt.Errorf("pos: device id required")
	}
	normalizedMerchant := NormalizePaymasterMerchant(record.Merchant)
	stored := &pos.Device{
		DeviceID:  normalizedID,
		Merchant:  normalizedMerchant,
		Revoked:   record.Revoked,
		Nonce:     record.Nonce,
		ExpiresAt: record.ExpiresAt,
		ChainID:   strings.TrimSpace(record.ChainID),
	}
	return m.KVPut(posDeviceKey(normalizedID), stored)
}

// POSDeleteDevice removes the device record from storage.
func (m *Manager) POSDeleteDevice(id string) error {
	if m == nil {
		return fmt.Errorf("pos: state manager not initialised")
	}
	normalized := NormalizePaymasterDevice(id)
	if normalized == "" {
		return nil
	}
	return m.KVDelete(posDeviceKey(normalized))
}
