package state

import (
	"fmt"
	"math/big"
)

// saleReferenceKey is the one slot that holds the latest outside reference price the
// treasury sale follows (ledger PL-BC-D). It is overwritten by every accepted reference
// and never deleted, unlike the per-epoch buyback record it is copied from, which
// settlement removes.
var saleReferenceKey = []byte("znhb/sale/ref")

// SaleReferenceRecord is the latest reference price accepted while the sale-follows-market
// rule was in force: the exact NHB-per-whole-ZNHB rate as a fraction, the epoch it was
// accepted in, and the signed time it was struck at (not the block time that carried it,
// so a stale number cannot look fresh).
type SaleReferenceRecord struct {
	Epoch       uint64
	RateNum     *big.Int
	RateDenom   *big.Int
	TimestampAt uint64
}

// SaleReference returns the latest recorded reference price, if there is one.
func (m *Manager) SaleReference() (*SaleReferenceRecord, bool, error) {
	var rec SaleReferenceRecord
	ok, err := m.KVGet(saleReferenceKey, &rec)
	if err != nil {
		return nil, false, fmt.Errorf("sale: load reference price: %w", err)
	}
	if !ok {
		return nil, false, nil
	}
	if rec.RateNum == nil || rec.RateDenom == nil || rec.RateNum.Sign() <= 0 || rec.RateDenom.Sign() <= 0 {
		return nil, false, fmt.Errorf("sale: stored reference price is not a positive fraction")
	}
	return &rec, true, nil
}

// SetSaleReference records the latest reference price. The caller has verified it.
func (m *Manager) SetSaleReference(rec SaleReferenceRecord) error {
	if rec.RateNum == nil || rec.RateDenom == nil || rec.RateNum.Sign() <= 0 || rec.RateDenom.Sign() <= 0 {
		return fmt.Errorf("sale: reference price must be a positive fraction")
	}
	return m.KVPut(saleReferenceKey, rec)
}
