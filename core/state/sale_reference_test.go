package state

import (
	"math/big"
	"testing"
)

func TestSaleReference_RoundTripsAndStartsEmpty(t *testing.T) {
	m := newTestManager(t)
	if _, found, err := m.SaleReference(); err != nil || found {
		t.Fatalf("fresh state: found=%v err=%v, want none", found, err)
	}
	want := SaleReferenceRecord{Epoch: 7, RateNum: big.NewInt(25), RateDenom: big.NewInt(10), TimestampAt: 1_790_000_000}
	if err := m.SetSaleReference(want); err != nil {
		t.Fatalf("set: %v", err)
	}
	got, found, err := m.SaleReference()
	if err != nil || !found {
		t.Fatalf("get: found=%v err=%v", found, err)
	}
	if got.Epoch != 7 || got.RateNum.Cmp(big.NewInt(25)) != 0 || got.RateDenom.Cmp(big.NewInt(10)) != 0 || got.TimestampAt != 1_790_000_000 {
		t.Fatalf("round trip changed the record: %+v", got)
	}
	// a second reference overwrites the first: there is one slot, not a list
	if err := m.SetSaleReference(SaleReferenceRecord{Epoch: 8, RateNum: big.NewInt(3), RateDenom: big.NewInt(1), TimestampAt: 1_790_000_100}); err != nil {
		t.Fatalf("set second: %v", err)
	}
	got, _, _ = m.SaleReference()
	if got.Epoch != 8 || got.RateNum.Cmp(big.NewInt(3)) != 0 {
		t.Fatalf("the second reference did not replace the first: %+v", got)
	}
}

func TestSaleReference_RefusesANonPositiveFractionOnWrite(t *testing.T) {
	m := newTestManager(t)
	for name, rec := range map[string]SaleReferenceRecord{
		"nil numerator":    {RateDenom: big.NewInt(1)},
		"nil denominator":  {RateNum: big.NewInt(1)},
		"zero numerator":   {RateNum: big.NewInt(0), RateDenom: big.NewInt(1)},
		"zero denominator": {RateNum: big.NewInt(1), RateDenom: big.NewInt(0)},
		"negative rate":    {RateNum: big.NewInt(-1), RateDenom: big.NewInt(1)},
		"negative divisor": {RateNum: big.NewInt(1), RateDenom: big.NewInt(-1)},
	} {
		if err := m.SetSaleReference(rec); err == nil {
			t.Errorf("%s: a reference that is not a positive fraction was stored", name)
		}
	}
	if _, found, _ := m.SaleReference(); found {
		t.Fatalf("a refused write left a record")
	}
}

// A stored value that is not a positive fraction must never be read as a price: the read returns an error, so
// the sale refuses instead of dividing by zero or pricing at zero.
func TestSaleReference_RefusesACorruptStoredFractionOnRead(t *testing.T) {
	for name, rec := range map[string]SaleReferenceRecord{
		"zero denominator": {Epoch: 1, RateNum: big.NewInt(1), RateDenom: big.NewInt(0), TimestampAt: 1},
		"zero numerator":   {Epoch: 1, RateNum: big.NewInt(0), RateDenom: big.NewInt(1), TimestampAt: 1},
	} {
		m := newTestManager(t)
		if err := m.KVPut(saleReferenceKey, rec); err != nil { // bypasses the checked writer
			t.Fatalf("%s: seed: %v", name, err)
		}
		if _, found, err := m.SaleReference(); err == nil || found {
			t.Errorf("%s: read found=%v err=%v, want an error", name, found, err)
		}
	}
}
