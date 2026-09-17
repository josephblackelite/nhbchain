package subscriptions

import (
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"

	nativecommon "nhbchain/native/common"
)

const moduleName = "subscriptions"

// RoleSubscriptionsAdmin lets a support/operations role cancel a
// subscription or deactivate a plan on behalf of a merchant or payer who
// cannot otherwise reach the chain -- mirrors native/loyalty's
// ROLE_LOYALTY_ADMIN precedent exactly.
const RoleSubscriptionsAdmin = "ROLE_SUBSCRIPTIONS_ADMIN"

// registryState is the storage-agnostic dependency Registry needs -- the
// same shape native/loyalty's Registry depends on, so *core/state.Manager
// satisfies it with zero additional wiring.
type registryState interface {
	HasRole(role string, addr []byte) bool
	KVGet(key []byte, out interface{}) (bool, error)
	KVPut(key []byte, value interface{}) error
	KVGetList(key []byte, out interface{}) error
}

// Registry manages persistence and retrieval of Plans, Subscriptions, and
// Charges. Never touches a trie, RLP, or keccak directly -- exactly
// native/loyalty's Registry discipline.
type Registry struct {
	st     registryState
	pauses nativecommon.PauseView
}

// NewRegistry creates a registry backed by the provided state manager.
func NewRegistry(st registryState) *Registry {
	return &Registry{st: st}
}

func (r *Registry) SetPauses(p nativecommon.PauseView) {
	if r == nil {
		return
	}
	r.pauses = p
}

func uint64Key(prefix string, id uint64) []byte {
	buf := make([]byte, len(prefix)+8)
	copy(buf, prefix)
	binary.BigEndian.PutUint64(buf[len(prefix):], id)
	return buf
}

func planKey(id PlanID) []byte { return uint64Key("subscriptions/plan/", uint64(id)) }
func merchantPlanIdxKey(m [20]byte) []byte {
	return append([]byte("subscriptions/merchantplans/"), m[:]...)
}
func subscriptionKey(id SubscriptionID) []byte {
	return uint64Key("subscriptions/sub/", uint64(id))
}
func payerSubIdxKey(p [20]byte) []byte { return append([]byte("subscriptions/payersubs/"), p[:]...) }
func merchantSubIdxKey(m [20]byte) []byte {
	return append([]byte("subscriptions/merchantsubs/"), m[:]...)
}
func chargeListKey(id SubscriptionID) []byte {
	return uint64Key("subscriptions/charges/", uint64(id))
}

// appendUint64Idx performs a full read-modify-write append to a uint64
// index list -- deliberately not the generic KVAppend (which de-dupes
// byte-identical entries): re-adding the same ID would be a genuine bug
// worth surfacing as a duplicate entry, not silently swallowed, matching
// core/state's BuybackAppendAsk/SubscriptionsAppendDue rationale.
func appendUint64Idx(st registryState, key []byte, id uint64) error {
	var existing []uint64
	if err := st.KVGetList(key, &existing); err != nil {
		return err
	}
	existing = append(existing, id)
	return st.KVPut(key, existing)
}

// CreatePlan persists a brand-new Plan. Only the merchant themself or a
// caller holding RoleSubscriptionsAdmin may create a plan on the
// merchant's behalf.
func (r *Registry) CreatePlan(caller [20]byte, p *Plan) error {
	if p == nil {
		return ErrNilPlan
	}
	if err := nativecommon.Guard(r.pauses, moduleName); err != nil {
		return err
	}
	sanitized, err := sanitizePlan(p)
	if err != nil {
		return err
	}
	if caller != sanitized.Merchant && !r.st.HasRole(RoleSubscriptionsAdmin, caller[:]) {
		return ErrUnauthorized
	}
	exists, err := r.st.KVGet(planKey(sanitized.ID), new(Plan))
	if err != nil {
		return err
	}
	if exists {
		return ErrPlanExists
	}
	if err := r.st.KVPut(planKey(sanitized.ID), sanitized); err != nil {
		return err
	}
	if err := appendUint64Idx(r.st, merchantPlanIdxKey(sanitized.Merchant), uint64(sanitized.ID)); err != nil {
		return err
	}
	return nil
}

// UpdatePlan mutates only a Plan's Name and Active flag -- pricing terms
// (PriceWei/Asset/IntervalSeconds/TrialPeriodSeconds) are immutable once
// created (see Plan's doc comment for why).
func (r *Registry) UpdatePlan(caller [20]byte, id PlanID, name string, active bool) (*Plan, error) {
	if err := nativecommon.Guard(r.pauses, moduleName); err != nil {
		return nil, err
	}
	existing := new(Plan)
	found, err := r.st.KVGet(planKey(id), existing)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrPlanNotFound
	}
	if caller != existing.Merchant && !r.st.HasRole(RoleSubscriptionsAdmin, caller[:]) {
		return nil, ErrUnauthorized
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalidPlan)
	}
	existing.Name = trimmed
	existing.Active = active
	if err := r.st.KVPut(planKey(existing.ID), existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// GetPlan retrieves a plan by its identifier.
func (r *Registry) GetPlan(id PlanID) (*Plan, bool) {
	out := new(Plan)
	ok, err := r.st.KVGet(planKey(id), out)
	if err != nil || !ok {
		return nil, false
	}
	return out, true
}

// ListPlansByMerchant returns every plan ID a merchant has created, in
// creation order.
func (r *Registry) ListPlansByMerchant(merchant [20]byte) ([]PlanID, error) {
	var raw []uint64
	if err := r.st.KVGetList(merchantPlanIdxKey(merchant), &raw); err != nil {
		return nil, err
	}
	ids := make([]PlanID, len(raw))
	for i := range raw {
		ids[i] = PlanID(raw[i])
	}
	return ids, nil
}

// CreateSubscription persists a brand-new standing authorization. The
// caller must be the payer themself -- the transaction's own recovered
// signer is the mandate, so no admin override exists for creation (unlike
// Cancel, which does allow an admin/merchant override for cleanup).
func (r *Registry) CreateSubscription(s *Subscription) error {
	if s == nil {
		return ErrNilSubscription
	}
	if err := nativecommon.Guard(r.pauses, moduleName); err != nil {
		return err
	}
	exists, err := r.st.KVGet(subscriptionKey(s.ID), new(Subscription))
	if err != nil {
		return err
	}
	if exists {
		return ErrSubscriptionExists
	}
	if err := r.st.KVPut(subscriptionKey(s.ID), s); err != nil {
		return err
	}
	if err := appendUint64Idx(r.st, payerSubIdxKey(s.Payer), uint64(s.ID)); err != nil {
		return err
	}
	if err := appendUint64Idx(r.st, merchantSubIdxKey(s.Merchant), uint64(s.ID)); err != nil {
		return err
	}
	return nil
}

// GetSubscription retrieves a subscription by its identifier.
func (r *Registry) GetSubscription(id SubscriptionID) (*Subscription, bool) {
	out := new(Subscription)
	ok, err := r.st.KVGet(subscriptionKey(id), out)
	if err != nil || !ok {
		return nil, false
	}
	return out, true
}

// PutSubscription persists a mutated Subscription record (status
// transitions, next-charge advancement, etc). Callers --
// core/subscriptions_tx.go and core/subscriptions_settlement.go -- own the
// mutation logic; the registry only persists.
func (r *Registry) PutSubscription(s *Subscription) error {
	if s == nil {
		return ErrNilSubscription
	}
	return r.st.KVPut(subscriptionKey(s.ID), s)
}

// CancelSubscription transitions a subscription to Cancelled. The payer,
// the plan's merchant, or a RoleSubscriptionsAdmin caller may cancel.
func (r *Registry) CancelSubscription(caller [20]byte, id SubscriptionID, cancelledAt uint64) (*Subscription, error) {
	if err := nativecommon.Guard(r.pauses, moduleName); err != nil {
		return nil, err
	}
	existing := new(Subscription)
	found, err := r.st.KVGet(subscriptionKey(id), existing)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, ErrSubscriptionNotFound
	}
	if caller != existing.Payer && caller != existing.Merchant && !r.st.HasRole(RoleSubscriptionsAdmin, caller[:]) {
		return nil, ErrUnauthorized
	}
	if existing.Status == SubscriptionStatusCancelled || existing.Status == SubscriptionStatusSuspended {
		return nil, ErrAlreadyCancelled
	}
	existing.Status = SubscriptionStatusCancelled
	existing.CancelledAt = cancelledAt
	if err := r.st.KVPut(subscriptionKey(existing.ID), existing); err != nil {
		return nil, err
	}
	return existing, nil
}

// ListSubscriptionsByPayer returns every subscription ID a payer holds, in
// creation order.
func (r *Registry) ListSubscriptionsByPayer(payer [20]byte) ([]SubscriptionID, error) {
	var raw []uint64
	if err := r.st.KVGetList(payerSubIdxKey(payer), &raw); err != nil {
		return nil, err
	}
	ids := make([]SubscriptionID, len(raw))
	for i := range raw {
		ids[i] = SubscriptionID(raw[i])
	}
	return ids, nil
}

// ListSubscriptionsByMerchant returns every subscription ID against any of
// a merchant's plans, in creation order.
func (r *Registry) ListSubscriptionsByMerchant(merchant [20]byte) ([]SubscriptionID, error) {
	var raw []uint64
	if err := r.st.KVGetList(merchantSubIdxKey(merchant), &raw); err != nil {
		return nil, err
	}
	ids := make([]SubscriptionID, len(raw))
	for i := range raw {
		ids[i] = SubscriptionID(raw[i])
	}
	return ids, nil
}

// AppendCharge records a new settlement-attempt audit record for a
// subscription. Deliberately a full read-modify-write via KVPut, mirroring
// core/state's BuybackAppendAsk: two charges can be structurally similar
// (same amount/status) and must never be deduplicated -- only
// AttemptNumber distinguishes them.
func (r *Registry) AppendCharge(subscriptionID SubscriptionID, charge Charge) error {
	existing, err := r.ListCharges(subscriptionID)
	if err != nil {
		return err
	}
	existing = append(existing, charge)
	return r.st.KVPut(chargeListKey(subscriptionID), existing)
}

// ListCharges returns every charge attempt recorded for a subscription, in
// chronological (attempt) order. Returns an empty, non-nil slice if none
// exist.
func (r *Registry) ListCharges(subscriptionID SubscriptionID) ([]Charge, error) {
	var charges []Charge
	if err := r.st.KVGetList(chargeListKey(subscriptionID), &charges); err != nil {
		return nil, fmt.Errorf("subscriptions: load charges for %d: %w", subscriptionID, err)
	}
	return charges, nil
}

func sanitizePlan(p *Plan) (*Plan, error) {
	copyPlan := *p
	copyPlan.Name = strings.TrimSpace(copyPlan.Name)
	if copyPlan.Name == "" {
		return nil, fmt.Errorf("%w: name required", ErrInvalidPlan)
	}
	if copyPlan.PriceWei == nil || copyPlan.PriceWei.Sign() <= 0 {
		return nil, fmt.Errorf("%w: priceWei must be positive", ErrInvalidPlan)
	}
	switch copyPlan.Asset {
	case AssetNHB, AssetZNHB:
	default:
		return nil, fmt.Errorf("%w: asset must be NHB or ZNHB", ErrInvalidPlan)
	}
	if copyPlan.IntervalSeconds == 0 {
		return nil, fmt.Errorf("%w: intervalSeconds must be positive", ErrInvalidPlan)
	}
	copyPlan.PriceWei = cloneBigInt(copyPlan.PriceWei)
	return &copyPlan, nil
}

func cloneBigInt(v *big.Int) *big.Int {
	if v == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(v)
}
