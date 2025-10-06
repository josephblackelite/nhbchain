package pos

import (
	"errors"
	"fmt"
	"strings"
)

type registryState interface {
	KVGet(key []byte, out interface{}) (bool, error)
	KVPut(key []byte, value interface{}) error
	KVDelete(key []byte) error
}

// Merchant captures the sponsorship state for a merchant account.
type Merchant struct {
	Address string
	Paused  bool
}

// Device records the sponsorship permissions for a merchant device.
type Device struct {
	DeviceID string
	Merchant string
	Revoked  bool
}

// Registry persists merchant and device sponsorship controls for the POS flow.
type Registry struct {
	state registryState
}

// NewRegistry constructs a registry backed by the provided state accessor.
func NewRegistry(state registryState) *Registry {
	return &Registry{state: state}
}

func normalizeMerchant(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return ""
	}
	return strings.ToLower(trimmed)
}

func normalizeDevice(value string) string {
	return strings.TrimSpace(value)
}

func merchantKey(addr string) []byte {
	return []byte(fmt.Sprintf("pos/merchant/%s", addr))
}

func deviceKey(id string) []byte {
	return []byte(fmt.Sprintf("pos/device/%s", id))
}

// Merchant fetches the persisted record for the provided merchant address. A nil
// record with ok=false indicates the merchant has not been onboarded yet.
func (r *Registry) Merchant(addr string) (*Merchant, bool, error) {
	if r == nil || r.state == nil {
		return nil, false, errors.New("pos: registry not initialised")
	}
	normalized := normalizeMerchant(addr)
	if normalized == "" {
		return nil, false, nil
	}
	var stored Merchant
	ok, err := r.state.KVGet(merchantKey(normalized), &stored)
	if err != nil || !ok {
		return nil, ok, err
	}
	stored.Address = normalizeMerchant(stored.Address)
	return &stored, true, nil
}

// UpsertMerchant ensures a merchant record exists with the provided address. A
// newly created merchant defaults to an active sponsorship state.
func (r *Registry) UpsertMerchant(addr string) (*Merchant, error) {
	if r == nil || r.state == nil {
		return nil, errors.New("pos: registry not initialised")
	}
	normalized := normalizeMerchant(addr)
	if normalized == "" {
		return nil, fmt.Errorf("pos: merchant address required")
	}
	existing, _, err := r.Merchant(normalized)
	if err != nil {
		return nil, err
	}
	record := &Merchant{Address: normalized}
	if existing != nil {
		record.Paused = existing.Paused
	}
	if err := r.state.KVPut(merchantKey(normalized), record); err != nil {
		return nil, err
	}
	return record, nil
}

// PauseMerchant flips the sponsorship flag to disabled for the supplied
// merchant. The record is created when missing so emergency pauses do not rely
// on the onboarding step completing successfully.
func (r *Registry) PauseMerchant(addr string) (*Merchant, error) {
	record, err := r.UpsertMerchant(addr)
	if err != nil {
		return nil, err
	}
	if record.Paused {
		return record, nil
	}
	record.Paused = true
	if err := r.state.KVPut(merchantKey(record.Address), record); err != nil {
		return nil, err
	}
	return record, nil
}

// ResumeMerchant re-enables sponsorship for the provided merchant address.
func (r *Registry) ResumeMerchant(addr string) (*Merchant, error) {
	record, err := r.UpsertMerchant(addr)
	if err != nil {
		return nil, err
	}
	if !record.Paused {
		return record, nil
	}
	record.Paused = false
	if err := r.state.KVPut(merchantKey(record.Address), record); err != nil {
		return nil, err
	}
	return record, nil
}

// Device fetches the device record associated with the identifier. A nil record
// with ok=false indicates the device has not been registered.
func (r *Registry) Device(id string) (*Device, bool, error) {
	if r == nil || r.state == nil {
		return nil, false, errors.New("pos: registry not initialised")
	}
	normalized := normalizeDevice(id)
	if normalized == "" {
		return nil, false, nil
	}
	var stored Device
	ok, err := r.state.KVGet(deviceKey(normalized), &stored)
	if err != nil || !ok {
		return nil, ok, err
	}
	stored.DeviceID = normalizeDevice(stored.DeviceID)
	stored.Merchant = normalizeMerchant(stored.Merchant)
	return &stored, true, nil
}

// RegisterDevice associates the device identifier with the merchant. Repeated
// calls overwrite the merchant binding so migrations remain deterministic.
func (r *Registry) RegisterDevice(id, merchant string) (*Device, error) {
	if r == nil || r.state == nil {
		return nil, errors.New("pos: registry not initialised")
	}
	normalizedID := normalizeDevice(id)
	if normalizedID == "" {
		return nil, fmt.Errorf("pos: device id required")
	}
	normalizedMerchant := normalizeMerchant(merchant)
	if normalizedMerchant == "" {
		return nil, fmt.Errorf("pos: merchant address required")
	}
	record := &Device{
		DeviceID: normalizedID,
		Merchant: normalizedMerchant,
	}
	existing, _, err := r.Device(normalizedID)
	if err != nil {
		return nil, err
	}
	if existing != nil {
		record.Revoked = existing.Revoked
	}
	if err := r.state.KVPut(deviceKey(normalizedID), record); err != nil {
		return nil, err
	}
	return record, nil
}

// RevokeDevice marks the device as ineligible for sponsored transactions.
func (r *Registry) RevokeDevice(id string) (*Device, error) {
	record, _, err := r.Device(id)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("pos: device %s not registered", normalizeDevice(id))
	}
	if record.Revoked {
		return record, nil
	}
	record.Revoked = true
	if err := r.state.KVPut(deviceKey(record.DeviceID), record); err != nil {
		return nil, err
	}
	return record, nil
}

// RestoreDevice clears the revocation flag for the provided device identifier.
func (r *Registry) RestoreDevice(id string) (*Device, error) {
	record, _, err := r.Device(id)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("pos: device %s not registered", normalizeDevice(id))
	}
	if !record.Revoked {
		return record, nil
	}
	record.Revoked = false
	if err := r.state.KVPut(deviceKey(record.DeviceID), record); err != nil {
		return nil, err
	}
	return record, nil
}

// DeleteDevice removes the device binding entirely.
func (r *Registry) DeleteDevice(id string) error {
	if r == nil || r.state == nil {
		return errors.New("pos: registry not initialised")
	}
	normalized := normalizeDevice(id)
	if normalized == "" {
		return nil
	}
	return r.state.KVDelete(deviceKey(normalized))
}
