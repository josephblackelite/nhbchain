package state

import (
	"fmt"
	"math/big"
)

var lendingRefPriceKey = []byte("lending/refprice/last")

// LendingRefPriceRecord is the most recently verified ZNHB/NHB reference
// price accepted into the lending oracle (core/lending_tx.go's
// applyLendingRefPriceTransaction), independent of any single market --
// broadcast into every configured market's Market.OracleMedianWei at
// AppliedBlock. Timestamp is the signers' own attestation time and is kept
// strictly increasing across accepted submissions so a validly-signed but
// stale bundle can never be replayed to push a fresher price backwards
// (core/lending_tx.go checks this before accepting a new submission).
type LendingRefPriceRecord struct {
	RateNum      *big.Int
	RateDenom    *big.Int
	Timestamp    uint64
	Signers      [][20]byte
	AppliedBlock uint64
	MarketCount  uint64
}

// LendingRefPriceLast returns the most recently accepted lending reference
// price, if any submission has ever been recorded.
func (m *Manager) LendingRefPriceLast() (*LendingRefPriceRecord, bool, error) {
	var rec LendingRefPriceRecord
	ok, err := m.KVGet(lendingRefPriceKey, &rec)
	if err != nil {
		return nil, false, fmt.Errorf("lending: load last reference price: %w", err)
	}
	if !ok {
		return nil, false, nil
	}
	return &rec, true, nil
}

// LendingSetRefPriceLast persists the most recently accepted lending
// reference price.
func (m *Manager) LendingSetRefPriceLast(rec LendingRefPriceRecord) error {
	return m.KVPut(lendingRefPriceKey, rec)
}
