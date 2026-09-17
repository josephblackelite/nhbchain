package state

import (
	"fmt"
	"math/big"
)

var refundLedgerPrefix = []byte("refund/thread/")

// RefundLedger provides helpers for linking refund transactions back to their
// originating payment and enforcing refund invariants.
type RefundLedger struct {
	manager *Manager
}

// RefundRecord captures the stored state for a given origin transaction.
type RefundRecord struct {
	OriginHash         [32]byte
	OriginAmount       *big.Int
	OriginTimestamp    uint64
	CumulativeRefunded *big.Int
	Refunds            []RefundLink
}

// RefundLink describes an individual refund entry tied to an origin
// transaction.
type RefundLink struct {
	TxHash    [32]byte
	Amount    *big.Int
	Timestamp uint64
}

type storedRefundRecord struct {
	OriginAmount       *big.Int
	OriginTimestamp    uint64
	CumulativeRefunded *big.Int
	Refunds            []storedRefundLink
}

type storedRefundLink struct {
	TxHash    [32]byte
	Amount    *big.Int
	Timestamp uint64
}

// RefundLedger returns a refund ledger helper bound to the manager.
func (m *Manager) RefundLedger() *RefundLedger {
	if m == nil {
		return nil
	}
	return &RefundLedger{manager: m}
}

// RecordOrigin initialises the ledger entry for an origin transaction if it has
// not been recorded already. Origin amounts must be strictly positive.
func (l *RefundLedger) RecordOrigin(origin [32]byte, amount *big.Int, timestamp uint64) (*RefundRecord, error) {
	if l == nil || l.manager == nil {
		return nil, fmt.Errorf("refund: ledger unavailable")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, fmt.Errorf("refund: origin amount must be positive")
	}
	key := refundLedgerKey(origin)
	var stored storedRefundRecord
	if ok, err := l.manager.KVGet(key, &stored); err != nil {
		return nil, err
	} else if ok {
		if stored.OriginAmount == nil {
			return nil, fmt.Errorf("refund: origin amount missing for %x", origin)
		}
		if stored.OriginAmount.Cmp(amount) != 0 {
			return nil, fmt.Errorf("refund: origin %x already recorded with amount %s", origin, stored.OriginAmount)
		}
		return refundRecordFromStored(origin, &stored), nil
	}
	record := storedRefundRecord{
		OriginAmount:       new(big.Int).Set(amount),
		OriginTimestamp:    timestamp,
		CumulativeRefunded: big.NewInt(0),
		Refunds:            make([]storedRefundLink, 0),
	}
	if err := l.manager.KVPut(key, &record); err != nil {
		return nil, err
	}
	return refundRecordFromStored(origin, &record), nil
}

// ValidateRefund ensures the requested refund will not exceed the origin
// amount.
func (l *RefundLedger) ValidateRefund(origin [32]byte, amount *big.Int) error {
	if l == nil || l.manager == nil {
		return fmt.Errorf("refund: ledger unavailable")
	}
	if amount == nil || amount.Sign() <= 0 {
		return fmt.Errorf("refund: refund amount must be positive")
	}
	stored, ok, err := l.load(origin)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("refund: origin %x not found", origin)
	}
	if stored.OriginAmount == nil {
		return fmt.Errorf("refund: origin amount missing for %x", origin)
	}
	cumulative := stored.CumulativeRefunded
	if cumulative == nil {
		cumulative = big.NewInt(0)
	}
	next := new(big.Int).Add(cumulative, amount)
	if next.Cmp(stored.OriginAmount) > 0 {
		return fmt.Errorf("refund: cumulative refunds %s exceed origin amount %s", next.String(), stored.OriginAmount.String())
	}
	return nil
}

// ApplyRefund records a refund entry and updates the cumulative refunded
// amount. Validation should be performed via ValidateRefund prior to calling
// this method to avoid mid-transaction failures.
func (l *RefundLedger) ApplyRefund(origin [32]byte, refund [32]byte, amount *big.Int, timestamp uint64) (*RefundRecord, error) {
	if l == nil || l.manager == nil {
		return nil, fmt.Errorf("refund: ledger unavailable")
	}
	if amount == nil || amount.Sign() <= 0 {
		return nil, fmt.Errorf("refund: refund amount must be positive")
	}
	key := refundLedgerKey(origin)
	stored, ok, err := l.load(origin)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("refund: origin %x not found", origin)
	}
	if stored.OriginAmount == nil {
		return nil, fmt.Errorf("refund: origin amount missing for %x", origin)
	}
	if stored.CumulativeRefunded == nil {
		stored.CumulativeRefunded = big.NewInt(0)
	}
	next := new(big.Int).Add(stored.CumulativeRefunded, amount)
	if next.Cmp(stored.OriginAmount) > 0 {
		return nil, fmt.Errorf("refund: cumulative refunds %s exceed origin amount %s", next.String(), stored.OriginAmount.String())
	}
	entry := storedRefundLink{
		TxHash:    refund,
		Amount:    new(big.Int).Set(amount),
		Timestamp: timestamp,
	}
	stored.CumulativeRefunded = next
	stored.Refunds = append(stored.Refunds, entry)
	if err := l.manager.KVPut(key, stored); err != nil {
		return nil, err
	}
	return refundRecordFromStored(origin, stored), nil
}

// Thread returns the complete refund record for the supplied origin hash.
func (l *RefundLedger) Thread(origin [32]byte) (*RefundRecord, bool, error) {
	if l == nil || l.manager == nil {
		return nil, false, fmt.Errorf("refund: ledger unavailable")
	}
	stored, ok, err := l.load(origin)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	return refundRecordFromStored(origin, stored), true, nil
}

func (l *RefundLedger) load(origin [32]byte) (*storedRefundRecord, bool, error) {
	if l == nil || l.manager == nil {
		return nil, false, fmt.Errorf("refund: ledger unavailable")
	}
	key := refundLedgerKey(origin)
	var stored storedRefundRecord
	ok, err := l.manager.KVGet(key, &stored)
	if err != nil {
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	if stored.Refunds == nil {
		stored.Refunds = make([]storedRefundLink, 0)
	}
	return &stored, true, nil
}

func refundLedgerKey(origin [32]byte) []byte {
	key := make([]byte, len(refundLedgerPrefix)+len(origin))
	copy(key, refundLedgerPrefix)
	copy(key[len(refundLedgerPrefix):], origin[:])
	return key
}

func refundRecordFromStored(origin [32]byte, stored *storedRefundRecord) *RefundRecord {
	if stored == nil {
		return nil
	}
	record := &RefundRecord{
		OriginHash:         origin,
		OriginAmount:       big.NewInt(0),
		OriginTimestamp:    stored.OriginTimestamp,
		CumulativeRefunded: big.NewInt(0),
		Refunds:            make([]RefundLink, 0, len(stored.Refunds)),
	}
	if stored.OriginAmount != nil {
		record.OriginAmount = new(big.Int).Set(stored.OriginAmount)
	}
	if stored.CumulativeRefunded != nil {
		record.CumulativeRefunded = new(big.Int).Set(stored.CumulativeRefunded)
	}
	for _, link := range stored.Refunds {
		amount := big.NewInt(0)
		if link.Amount != nil {
			amount = new(big.Int).Set(link.Amount)
		}
		record.Refunds = append(record.Refunds, RefundLink{
			TxHash:    link.TxHash,
			Amount:    amount,
			Timestamp: link.Timestamp,
		})
	}
	return record
}
