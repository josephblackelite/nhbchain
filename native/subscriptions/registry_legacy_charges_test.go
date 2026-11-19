package subscriptions

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"
)

// PL-R5T-82: a charge history stored under the old key is read through and migrated
// when the registry is told to (the state processor does from a configured height).

// meteredStateInternal is a plain map-backed state, enough for a Registry.
type meteredStateInternal struct{ values map[string][]byte }

func newMeteredStateInternal() *meteredStateInternal {
	return &meteredStateInternal{values: map[string][]byte{}}
}

func (m *meteredStateInternal) HasRole(string, []byte) bool { return false }

func (m *meteredStateInternal) KVGet(key []byte, out interface{}) (bool, error) {
	data, ok := m.values[string(key)]
	if !ok {
		return false, nil
	}
	return true, rlp.DecodeBytes(data, out)
}

func (m *meteredStateInternal) KVPut(key []byte, value interface{}) error {
	data, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	m.values[string(key)] = data
	return nil
}

func (m *meteredStateInternal) KVGetList(key []byte, out interface{}) error {
	data, ok := m.values[string(key)]
	if !ok {
		elem := reflect.ValueOf(out).Elem()
		elem.Set(reflect.MakeSlice(elem.Type(), 0, 0))
		return nil
	}
	return rlp.DecodeBytes(data, out)
}

// snapshot copies the stored bytes, to compare two states or one state over time.
func (m *meteredStateInternal) snapshot() map[string]string {
	out := make(map[string]string, len(m.values))
	for k, v := range m.values {
		out[k] = string(v)
	}
	return out
}

func charge(attempt uint32) Charge {
	return Charge{
		SubscriptionID: 7, PlanID: 1, Asset: AssetNHB, AmountWei: big.NewInt(100), FeeWei: big.NewInt(0),
		Status: ChargeStatusPaid, AttemptNumber: attempt, ChargedAt: 1_000 + uint64(attempt),
	}
}

func newLegacyRegistry(t *testing.T, legacyAttempts int, readThrough bool) (*Registry, *meteredStateInternal) {
	t.Helper()
	st := newMeteredStateInternal()
	r := NewRegistry(st)
	r.SetLegacyChargeHistory(readThrough)
	if legacyAttempts > 0 {
		var old []Charge
		for i := 1; i <= legacyAttempts; i++ {
			old = append(old, charge(uint32(i)))
		}
		if err := st.KVPut(legacyChargeListKey(7), old); err != nil {
			t.Fatalf("seed the old list: %v", err)
		}
	}
	return r, st
}

func attempts(t *testing.T, r *Registry) []uint32 {
	t.Helper()
	list, err := r.ListCharges(7)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	out := []uint32{}
	for _, c := range list {
		out = append(out, c.AttemptNumber)
	}
	return out
}

// Off: the history under the old key is invisible and the numbering restarts: the
// defect the setting exists for.
func TestChargeHistoryOffNeverLooksAtTheOldKey(t *testing.T) {
	r, st := newLegacyRegistry(t, 3, false)
	if n, err := r.ChargeCount(7); err != nil || n != 0 {
		t.Fatalf("count = %d, %v; want 0", n, err)
	}
	if got := attempts(t, r); len(got) != 0 {
		t.Fatalf("listed %v, want nothing", got)
	}
	if err := r.AppendCharge(7, charge(1)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if n, _ := r.ChargeCount(7); n != 1 {
		t.Fatalf("count after one append = %d, want 1 (the numbering restarted)", n)
	}
	if _, ok := st.values[string(legacyChargeListKey(7))]; !ok {
		t.Fatalf("the old list was touched")
	}
}

func TestChargeHistoryOnReadsTheOldListWithoutWritingAnything(t *testing.T) {
	r, st := newLegacyRegistry(t, 3, true)
	before := st.snapshot()
	if n, err := r.ChargeCount(7); err != nil || n != 3 {
		t.Fatalf("count = %d, %v; want 3", n, err)
	}
	if got := attempts(t, r); !reflect.DeepEqual(got, []uint32{1, 2, 3}) {
		t.Fatalf("listed %v, want [1 2 3]", got)
	}
	if !reflect.DeepEqual(before, st.snapshot()) {
		t.Fatalf("a read wrote to the state")
	}
}

func TestChargeHistoryOnMigratesOnTheFirstAppendAndContinuesTheNumbering(t *testing.T) {
	r, st := newLegacyRegistry(t, 3, true)
	if err := r.AppendCharge(7, charge(4)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if n, _ := r.ChargeCount(7); n != 4 {
		t.Fatalf("count after the migrating append = %d, want 4", n)
	}
	if got := attempts(t, r); !reflect.DeepEqual(got, []uint32{1, 2, 3, 4}) {
		t.Fatalf("listed %v, want [1 2 3 4]", got)
	}
	for attempt := uint64(1); attempt <= 4; attempt++ {
		if _, ok := st.values[string(chargeKey(7, attempt))]; !ok {
			t.Fatalf("attempt %d has no key of its own", attempt)
		}
	}
	// Migration runs once: the old list is no longer read once the count exists.
	if err := st.KVPut(legacyChargeListKey(7), []Charge{charge(99)}); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	if err := r.AppendCharge(7, charge(5)); err != nil {
		t.Fatalf("second append: %v", err)
	}
	if got := attempts(t, r); !reflect.DeepEqual(got, []uint32{1, 2, 3, 4, 5}) {
		t.Fatalf("listed %v after the old list changed, want [1 2 3 4 5]", got)
	}
}

// With nothing under the old key the setting changes nothing, byte for byte.
func TestChargeHistoryOnWithNoOldKeyLeavesTheStateIdentical(t *testing.T) {
	off, offState := newLegacyRegistry(t, 0, false)
	on, onState := newLegacyRegistry(t, 0, true)
	for _, r := range []*Registry{off, on} {
		if n, err := r.ChargeCount(7); err != nil || n != 0 {
			t.Fatalf("count = %d, %v", n, err)
		}
		for i := uint32(1); i <= 3; i++ {
			if err := r.AppendCharge(7, charge(i)); err != nil {
				t.Fatalf("append %d: %v", i, err)
			}
		}
	}
	if !reflect.DeepEqual(offState.snapshot(), onState.snapshot()) {
		t.Fatalf("the state differs between the settings with no old key")
	}
}

// An old list that is present but empty is no history.
func TestChargeHistoryOnWithAnEmptyOldListIsNoHistory(t *testing.T) {
	r, st := newLegacyRegistry(t, 0, true)
	if err := st.KVPut(legacyChargeListKey(7), []Charge{}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n, _ := r.ChargeCount(7); n != 0 {
		t.Fatalf("count = %d, want 0", n)
	}
	if err := r.AppendCharge(7, charge(1)); err != nil {
		t.Fatalf("append: %v", err)
	}
	if got := attempts(t, r); !reflect.DeepEqual(got, []uint32{1}) {
		t.Fatalf("listed %v, want [1]", got)
	}
}

// Another subscription's old list is not read.
func TestChargeHistoryOnReadsOnlyTheSubscriptionsOwnOldList(t *testing.T) {
	r, st := newLegacyRegistry(t, 0, true)
	if err := st.KVPut(legacyChargeListKey(8), []Charge{charge(1), charge(2)}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if n, _ := r.ChargeCount(7); n != 0 {
		t.Fatalf("subscription 7 counted %d from subscription 8's list", n)
	}
	if n, _ := r.ChargeCount(8); n != 2 {
		t.Fatalf("subscription 8 counts %d, want 2", n)
	}
}

// The old key is pinned independently of the code that reads it.
func TestLegacyChargeListKeyIsThePublishedLayout(t *testing.T) {
	want := append([]byte("subscriptions/charges/"), 0, 0, 0, 0, 0, 0, 0, 7)
	if got := legacyChargeListKey(7); string(got) != string(want) {
		t.Fatalf("legacyChargeListKey(7) = %q, want %q", got, want)
	}
}
