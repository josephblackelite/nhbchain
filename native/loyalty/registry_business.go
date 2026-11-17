package loyalty

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"strings"

	nativecommon "nhbchain/native/common"
)

var zeroBusinessID BusinessID

func (r *Registry) RegisterBusiness(owner [20]byte, name string) (BusinessID, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return zeroBusinessID, fmt.Errorf("%w: name required", ErrInvalidBusiness)
	}
	if err := nativecommon.Guard(r.pauses, moduleName); err != nil {
		return zeroBusinessID, err
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

// SetPaymaster assigns the wallet that funds a business's program rewards.
// Every reward is debited from the paymaster's own balance, and the named
// wallet never signs the assignment, so two rules keep a wallet from being
// committed without its say:
//
//   - A business owner may name only its own wallet (or clear the paymaster).
//     Naming any other wallet is refused with ErrPaymasterConsentRequired,
//     which can never succeed for that signer.
//   - A loyalty admin may name another wallet only if that wallet is the
//     business owner or has recorded its own opt-in for this business, by
//     calling SetPaymaster with its own address as the new paymaster. Without
//     the opt-in the call is refused with ErrPaymasterConsent, which a later
//     opt-in by the named wallet turns into a success. The opt-in is
//     per business and is consumed by the assignment it authorizes.
func (r *Registry) SetPaymaster(id BusinessID, caller [20]byte, newPaymaster [20]byte) error {
	if err := nativecommon.Guard(r.pauses, moduleName); err != nil {
		return err
	}
	business, ok := r.getBusiness(id)
	if !ok {
		return ErrBusinessNotFound
	}
	if caller != business.Owner && !r.st.HasRole(roleLoyaltyAdmin, caller[:]) {
		if !isZeroAddress(newPaymaster) && newPaymaster == caller {
			return r.st.KVPut(paymasterConsentKey(business.ID, caller), uint64(1))
		}
		return ErrUnauthorized
	}
	// Naming an address commits that address's funds. Without this rule any
	// business owner could point a program at somebody else's wallet -- an
	// ordinary user's, or one that holds protocol funds -- and have each
	// reward paid out of it to a spender of their choosing. An owner can
	// therefore commit only its own wallet; only the role trusted with
	// assigning wallets may name another one, and then only with that wallet's
	// recorded opt-in (checked below). Checked before the no-op case so
	// re-affirming the current paymaster is still a request about that wallet.
	if !isZeroAddress(newPaymaster) && newPaymaster != caller && !r.st.HasRole(roleLoyaltyAdmin, caller[:]) {
		return ErrPaymasterConsentRequired
	}
	if business.Paymaster == newPaymaster {
		return nil
	}
	consentKey := paymasterConsentKey(business.ID, newPaymaster)
	consumeConsent := false
	if !isZeroAddress(newPaymaster) && newPaymaster != business.Owner && newPaymaster != caller {
		var consent uint64
		found, err := r.st.KVGet(consentKey, &consent)
		if err != nil {
			return err
		}
		if !found || consent != 1 {
			return ErrPaymasterConsent
		}
		consumeConsent = true
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
	if consumeConsent {
		if err := r.st.KVPut(consentKey, uint64(0)); err != nil {
			return err
		}
	}
	r.emit(newPaymasterRotatedEvent(business, caller, oldPaymaster, newPaymaster))
	return nil
}

func (r *Registry) AddMerchantAddress(id BusinessID, addr [20]byte) error {
	if err := nativecommon.Guard(r.pauses, moduleName); err != nil {
		return err
	}
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
	if err := nativecommon.Guard(r.pauses, moduleName); err != nil {
		return err
	}
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
