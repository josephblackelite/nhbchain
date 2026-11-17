package subscriptions_test

import (
	"math/big"
	"reflect"
	"testing"

	"github.com/ethereum/go-ethereum/rlp"

	subscriptions "nhbchain/native/subscriptions"
)

// meteredState is the state a Registry needs, kept in a map, that counts the
// encoded bytes every operation reads and writes.
type meteredState struct {
	values       map[string][]byte
	bytesRead    int
	bytesWritten int
	operations   int
}

func newMeteredState() *meteredState { return &meteredState{values: map[string][]byte{}} }

func (m *meteredState) HasRole(string, []byte) bool { return false }

func (m *meteredState) KVGet(key []byte, out interface{}) (bool, error) {
	m.operations++
	data, ok := m.values[string(key)]
	if !ok {
		return false, nil
	}
	m.bytesRead += len(data)
	return true, rlp.DecodeBytes(data, out)
}

func (m *meteredState) KVPut(key []byte, value interface{}) error {
	m.operations++
	data, err := rlp.EncodeToBytes(value)
	if err != nil {
		return err
	}
	m.bytesWritten += len(data)
	m.values[string(key)] = data
	return nil
}

func (m *meteredState) KVGetList(key []byte, out interface{}) error {
	m.operations++
	data, ok := m.values[string(key)]
	if !ok {
		elem := reflect.ValueOf(out).Elem()
		elem.Set(reflect.MakeSlice(elem.Type(), 0, 0))
		return nil
	}
	m.bytesRead += len(data)
	return rlp.DecodeBytes(data, out)
}

// work returns what the state has read, written and been asked to do since the
// last call.
func (m *meteredState) work() (read, written, operations int) {
	read, written, operations = m.bytesRead, m.bytesWritten, m.operations
	m.bytesRead, m.bytesWritten, m.operations = 0, 0, 0
	return read, written, operations
}

func testCharge(id subscriptions.SubscriptionID, attempt uint32) subscriptions.Charge {
	return subscriptions.Charge{
		SubscriptionID: id,
		PlanID:         1,
		Payer:          [20]byte{0x01},
		Merchant:       [20]byte{0x02},
		Asset:          subscriptions.AssetNHB,
		AmountWei:      big.NewInt(1_000_000_000_000_000_000),
		FeeWei:         big.NewInt(10_000_000_000_000_000),
		Status:         subscriptions.ChargeStatusPaid,
		AttemptNumber:  attempt,
		ChargedAt:      1_800_000_000 + uint64(attempt)*86_400,
	}
}

// Every charge attempt is recorded, and the history grows with one attempt per
// day at the shortest interval. Recording an attempt used to read the whole
// list of the subscription's earlier attempts and write it back with one more,
// so the work of a charge grew with the age of the subscription. It must be
// the same for the thousandth attempt as for the first.
func TestRegistryAppendCharge_WorkDoesNotGrowWithTheHistory(t *testing.T) {
	state := newMeteredState()
	registry := subscriptions.NewRegistry(state)
	const id = subscriptions.SubscriptionID(7)

	const attempts = 1000
	var firstRead, firstWritten, firstOps int
	for attempt := 1; attempt <= attempts; attempt++ {
		state.work()
		if err := registry.AppendCharge(id, testCharge(id, uint32(attempt))); err != nil {
			t.Fatalf("append charge %d: %v", attempt, err)
		}
		read, written, ops := state.work()
		if attempt == 1 {
			firstRead, firstWritten, firstOps = read, written, ops
			continue
		}
		if read > firstRead+16 || written > firstWritten+16 || ops > firstOps {
			t.Fatalf("attempt %d read %d bytes and wrote %d in %d operations, the first read %d and wrote %d in %d: the work grows with the history",
				attempt, read, written, ops, firstRead, firstWritten, firstOps)
		}
	}

	// The attempt number of the next charge does not need the history either.
	state.work()
	count, err := registry.ChargeCount(id)
	if err != nil || count != attempts {
		t.Fatalf("ChargeCount = %d, %v; want %d", count, err, attempts)
	}
	if read, written, ops := state.work(); read > 16 || written != 0 || ops != 1 {
		t.Fatalf("ChargeCount read %d bytes, wrote %d, in %d operations; want one small read", read, written, ops)
	}
}

// The history still reads back whole, in order, per subscription.
func TestRegistryListCharges_ReturnsEachHistoryInOrder(t *testing.T) {
	registry := subscriptions.NewRegistry(newMeteredState())
	const first, second = subscriptions.SubscriptionID(1), subscriptions.SubscriptionID(2)

	if charges, err := registry.ListCharges(first); err != nil || charges == nil || len(charges) != 0 {
		t.Fatalf("a subscription with no attempts must list an empty, non-nil history: %v, %v", charges, err)
	}
	if count, err := registry.ChargeCount(first); err != nil || count != 0 {
		t.Fatalf("ChargeCount of a subscription with no attempts = %d, %v; want 0", count, err)
	}

	for attempt := uint32(1); attempt <= 5; attempt++ {
		if err := registry.AppendCharge(first, testCharge(first, attempt)); err != nil {
			t.Fatalf("append: %v", err)
		}
		if attempt <= 2 {
			if err := registry.AppendCharge(second, testCharge(second, attempt)); err != nil {
				t.Fatalf("append: %v", err)
			}
		}
	}

	for _, tc := range []struct {
		id   subscriptions.SubscriptionID
		want int
	}{{first, 5}, {second, 2}} {
		charges, err := registry.ListCharges(tc.id)
		if err != nil || len(charges) != tc.want {
			t.Fatalf("subscription %d lists %d attempts (%v), want %d", tc.id, len(charges), err, tc.want)
		}
		for i, charge := range charges {
			if charge.SubscriptionID != tc.id || charge.AttemptNumber != uint32(i+1) {
				t.Fatalf("subscription %d attempt %d is %+v: out of order or another subscription's", tc.id, i+1, charge)
			}
		}
		if count, err := registry.ChargeCount(tc.id); err != nil || count != uint64(tc.want) {
			t.Fatalf("ChargeCount(%d) = %d, %v; want %d", tc.id, count, err, tc.want)
		}
	}
}
