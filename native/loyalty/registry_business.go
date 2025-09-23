package loyalty

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"
)

var zeroBusinessID BusinessID

func (r *Registry) RegisterBusiness(owner [20]byte, name string) (BusinessID, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return zeroBusinessID, fmt.Errorf("%w: name required", ErrInvalidBusiness)
	}
	id, err := r.nextBusinessID()
	if err != nil {
		return zeroBusinessID, err
	}
	business := &Business{
		ID:        id,
		Owner:     owner,
		Name:      trimmed,
		Merchants: make([][20]byte, 0),
	}
	if err := r.st.KVPut(businessKey(id), business); err != nil {
		return zeroBusinessID, err
	}
	if err := r.st.KVAppend(businessOwnerKey(owner), id[:]); err != nil {
		return zeroBusinessID, err
	}
	return id, nil
}

func (r *Registry) SetPaymaster(id BusinessID, caller [20]byte, newPaymaster [20]byte) error {
	business, ok := r.getBusiness(id)
	if !ok {
		return ErrBusinessNotFound
	}
	if caller != business.Owner && !r.st.HasRole(roleLoyaltyAdmin, caller[:]) {
		return ErrUnauthorized
	}
	if business.Paymaster == newPaymaster {
		return nil
	}
	oldPaymaster := business.Paymaster
	ownerKey := ownerPaymasterKey(business.Owner)
	var active BusinessID
	hasActive, err := r.st.KVGet(ownerKey, &active)
	if err != nil {
		return err
	}
	if !isZeroAddress(newPaymaster) {
		if hasActive && active != zeroBusinessID && active != business.ID {
			other, exists := r.getBusiness(active)
			if exists && !isZeroAddress(other.Paymaster) {
				return ErrPaymasterConflict
			}
		}
		if err := r.st.KVPut(ownerKey, business.ID); err != nil {
			return err
		}
	} else {
		if hasActive && active == business.ID {
			if err := r.st.KVPut(ownerKey, zeroBusinessID); err != nil {
				return err
			}
		}
	}
	business.Paymaster = newPaymaster
	if err := r.st.KVPut(businessKey(business.ID), business); err != nil {
		return err
	}
	r.emit(newPaymasterRotatedEvent(business, caller, oldPaymaster, newPaymaster))
	return nil
}

func (r *Registry) AddMerchantAddress(id BusinessID, addr [20]byte) error {
	business, ok := r.getBusiness(id)
	if !ok {
		return ErrBusinessNotFound
	}
	existing, assigned := r.IsMerchant(addr)
	if assigned && existing != business.ID {
		return ErrMerchantAssigned
	}
	present := false
	for _, merchant := range business.Merchants {
		if merchant == addr {
			present = true
			break
		}
	}
	if !present {
		business.Merchants = append(business.Merchants, addr)
		sort.Slice(business.Merchants, func(i, j int) bool {
			return bytes.Compare(business.Merchants[i][:], business.Merchants[j][:]) < 0
		})
	}
	if err := r.st.KVPut(businessKey(business.ID), business); err != nil {
		return err
	}
	return r.st.KVPut(merchantBusinessIndexKey(addr), business.ID)
}

func (r *Registry) RemoveMerchantAddress(id BusinessID, addr [20]byte) error {
	business, ok := r.getBusiness(id)
	if !ok {
		return ErrBusinessNotFound
	}
	existing, assigned := r.IsMerchant(addr)
	if !assigned || existing != business.ID {
		return ErrMerchantNotFound
	}
	updated := make([][20]byte, 0, len(business.Merchants))
	for _, merchant := range business.Merchants {
		if merchant != addr {
			updated = append(updated, merchant)
		}
	}
	business.Merchants = updated
	if err := r.st.KVPut(businessKey(business.ID), business); err != nil {
		return err
	}
	return r.st.KVPut(merchantBusinessIndexKey(addr), zeroBusinessID)
}

func (r *Registry) PrimaryPaymaster(owner [20]byte) ([20]byte, bool) {
	var zeroAddr [20]byte
	var id BusinessID
	exists, err := r.st.KVGet(ownerPaymasterKey(owner), &id)
	if err == nil && exists && id != zeroBusinessID {
		if business, ok := r.getBusiness(id); ok && !isZeroAddress(business.Paymaster) {
			return business.Paymaster, true
		}
	}
	ids, err := r.listBusinessesByOwner(owner)
	if err != nil {
		return zeroAddr, false
	}
	for _, bizID := range ids {
		if business, ok := r.getBusiness(bizID); ok && !isZeroAddress(business.Paymaster) {
			return business.Paymaster, true
		}
	}
	return zeroAddr, false
}

func (r *Registry) IsMerchant(addr [20]byte) (BusinessID, bool) {
	var id BusinessID
	exists, err := r.st.KVGet(merchantBusinessIndexKey(addr), &id)
	if err != nil || !exists || id == zeroBusinessID {
		return zeroBusinessID, false
	}
	return id, true
}

func (r *Registry) getBusiness(id BusinessID) (*Business, bool) {
	business := new(Business)
	exists, err := r.st.KVGet(businessKey(id), business)
	if err != nil || !exists {
		return nil, false
	}
	return business, true
}

func (r *Registry) listBusinessesByOwner(owner [20]byte) ([]BusinessID, error) {
	var raw [][]byte
	if err := r.st.KVGetList(businessOwnerKey(owner), &raw); err != nil {
		return nil, err
	}
	ids := make([]BusinessID, 0, len(raw))
	for _, entry := range raw {
		var id BusinessID
		copy(id[:], entry)
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	return ids, nil
}

func (r *Registry) nextBusinessID() (BusinessID, error) {
	key := businessCounterKey()
	var counter uint64
	_, err := r.st.KVGet(key, &counter)
	if err != nil {
		return zeroBusinessID, err
	}
	counter++
	if err := r.st.KVPut(key, counter); err != nil {
		return zeroBusinessID, err
	}
	var id BusinessID
	binary.BigEndian.PutUint64(id[len(id)-8:], counter)
	return id, nil
}

func isZeroAddress(addr [20]byte) bool {
	var zero [20]byte
	return addr == zero
}
