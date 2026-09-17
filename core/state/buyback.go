package state

import (
	"encoding/binary"
	"fmt"
	"math/big"
)

var (
	buybackAskListPrefix  = []byte("znhb/buyback/asks/")
	buybackRefPricePrefix = []byte("znhb/buyback/refprice/")
)

// BuybackAskRecord is a single pending market ask awaiting the next epoch's
// treasury buyback settlement (core/buyback_settlement.go). The seller's
// ZNHB is escrowed at submission time (core/buyback_tx.go's
// applyBuybackAsk), not at settlement.
type BuybackAskRecord struct {
	Seller    [20]byte
	AmountWei *big.Int
}

func buybackAskListKey(epoch uint64) []byte {
	buf := make([]byte, len(buybackAskListPrefix)+8)
	copy(buf, buybackAskListPrefix)
	binary.BigEndian.PutUint64(buf[len(buybackAskListPrefix):], epoch)
	return buf
}

// BuybackAsksForEpoch returns every pending ask recorded for the given
// epoch, in submission order. Returns an empty, non-nil slice if none exist.
func (m *Manager) BuybackAsksForEpoch(epoch uint64) ([]BuybackAskRecord, error) {
	var asks []BuybackAskRecord
	if err := m.KVGetList(buybackAskListKey(epoch), &asks); err != nil {
		return nil, fmt.Errorf("buyback: load asks for epoch %d: %w", epoch, err)
	}
	return asks, nil
}

// BuybackAppendAsk records a new pending ask for the given epoch. Unlike
// KVAppend, this never deduplicates -- two identical {seller, amount} asks
// from the same seller in the same epoch are both real, independent
// commitments, each with its own ZNHB already escrowed.
func (m *Manager) BuybackAppendAsk(epoch uint64, ask BuybackAskRecord) error {
	existing, err := m.BuybackAsksForEpoch(epoch)
	if err != nil {
		return err
	}
	existing = append(existing, BuybackAskRecord{Seller: ask.Seller, AmountWei: new(big.Int).Set(ask.AmountWei)})
	return m.KVPut(buybackAskListKey(epoch), existing)
}

// BuybackClearAsksForEpoch removes the pending-ask list for a settled epoch.
func (m *Manager) BuybackClearAsksForEpoch(epoch uint64) error {
	return m.KVDelete(buybackAskListKey(epoch))
}

// BuybackRefPriceRecord is the verified reference price accepted for a
// given epoch's buyback settlement, recorded by applyBuybackRefPrice once
// buyback.VerifyReferencePrice confirms an M-of-N quorum of genesis-declared
// signers. RateNum/RateDenom together are the exact NHB-per-whole-ZNHB
// rate, matching buyback.ReferencePrice.Rate (a big.Rat).
type BuybackRefPriceRecord struct {
	Epoch       uint64
	RateNum     *big.Int
	RateDenom   *big.Int
	TimestampAt uint64
	Signers     [][20]byte
}

func buybackRefPriceKey(epoch uint64) []byte {
	buf := make([]byte, len(buybackRefPricePrefix)+8)
	copy(buf, buybackRefPricePrefix)
	binary.BigEndian.PutUint64(buf[len(buybackRefPricePrefix):], epoch)
	return buf
}

// BuybackRefPriceForEpoch returns the verified reference price recorded for
// the given epoch, if one was submitted before settlement.
func (m *Manager) BuybackRefPriceForEpoch(epoch uint64) (*BuybackRefPriceRecord, bool, error) {
	var rec BuybackRefPriceRecord
	ok, err := m.KVGet(buybackRefPriceKey(epoch), &rec)
	if err != nil {
		return nil, false, fmt.Errorf("buyback: load reference price for epoch %d: %w", epoch, err)
	}
	if !ok {
		return nil, false, nil
	}
	return &rec, true, nil
}

// BuybackSetRefPriceForEpoch records the verified reference price for an
// epoch. Callers must ensure a price hasn't already been recorded for this
// epoch (BuybackRefPriceForEpoch) before calling -- this overwrites
// unconditionally.
func (m *Manager) BuybackSetRefPriceForEpoch(epoch uint64, rec BuybackRefPriceRecord) error {
	return m.KVPut(buybackRefPriceKey(epoch), rec)
}

// BuybackClearRefPriceForEpoch removes a settled epoch's reference price
// record.
func (m *Manager) BuybackClearRefPriceForEpoch(epoch uint64) error {
	return m.KVDelete(buybackRefPriceKey(epoch))
}
