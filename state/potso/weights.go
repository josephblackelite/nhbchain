package potso

import (
	"encoding/hex"
	"errors"
	"math/big"
	"sync"
)

type WeightEntry struct {
	Base  *big.Int
	Value *big.Int
}

type WeightUpdate struct {
	Offender [20]byte
	Previous *big.Int
	Current  *big.Int
	Applied  *big.Int
	Clamped  bool
}

type Ledger struct {
	mu      sync.RWMutex
	floor   *big.Int
	ceil    *big.Int
	entries map[[20]byte]*weightRecord
	applied map[string]struct{}
}

type weightRecord struct {
	base  *big.Int
	value *big.Int
}

func NewLedger(floor, ceil *big.Int) (*Ledger, error) {
	if floor != nil && floor.Sign() < 0 {
		return nil, errors.New("potso: weight floor cannot be negative")
	}
	if ceil != nil && ceil.Sign() < 0 {
		return nil, errors.New("potso: weight ceiling cannot be negative")
	}
	if floor != nil && ceil != nil && floor.Cmp(ceil) > 0 {
		return nil, errors.New("potso: weight floor exceeds ceiling")
	}
	ledger := &Ledger{
		floor:   copyBigInt(floor),
		ceil:    copyBigInt(ceil),
		entries: make(map[[20]byte]*weightRecord),
		applied: make(map[string]struct{}),
	}
	return ledger, nil
}

func copyBigInt(v *big.Int) *big.Int {
	if v == nil {
		return nil
	}
	return new(big.Int).Set(v)
}

func (l *Ledger) Floor() *big.Int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return copyBigInt(l.floor)
}

func (l *Ledger) Ceiling() *big.Int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return copyBigInt(l.ceil)
}

func (l *Ledger) SetBounds(floor, ceil *big.Int) error {
	if floor != nil && floor.Sign() < 0 {
		return errors.New("potso: weight floor cannot be negative")
	}
	if ceil != nil && ceil.Sign() < 0 {
		return errors.New("potso: weight ceiling cannot be negative")
	}
	if floor != nil && ceil != nil && floor.Cmp(ceil) > 0 {
		return errors.New("potso: weight floor exceeds ceiling")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.floor = copyBigInt(floor)
	l.ceil = copyBigInt(ceil)
	for _, rec := range l.entries {
		rec.value = l.clampUnlocked(rec.value)
	}
	return nil
}

func (l *Ledger) clampUnlocked(value *big.Int) *big.Int {
	if value == nil {
		return copyBigInt(l.floor)
	}
	result := new(big.Int).Set(value)
	if l.ceil != nil && result.Cmp(l.ceil) > 0 {
		result.Set(l.ceil)
	}
	if l.floor != nil && result.Cmp(l.floor) < 0 {
		result.Set(l.floor)
	}
	return result
}

func (l *Ledger) getOrCreate(addr [20]byte) *weightRecord {
	if rec, ok := l.entries[addr]; ok {
		return rec
	}
	base := copyBigInt(l.floor)
	if base == nil {
		base = big.NewInt(0)
	}
	rec := &weightRecord{
		base:  base,
		value: copyBigInt(base),
	}
	l.entries[addr] = rec
	return rec
}

func (l *Ledger) Set(addr [20]byte, base, value *big.Int) (*WeightEntry, error) {
	if base != nil && base.Sign() < 0 {
		return nil, errors.New("potso: base weight cannot be negative")
	}
	if value != nil && value.Sign() < 0 {
		return nil, errors.New("potso: weight cannot be negative")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.getOrCreate(addr)
	if base != nil {
		rec.base = new(big.Int).Set(base)
	}
	if value != nil {
		rec.value = new(big.Int).Set(value)
	} else if rec.value == nil {
		rec.value = copyBigInt(rec.base)
	}
	rec.value = l.clampUnlocked(rec.value)
	return &WeightEntry{Base: copyBigInt(rec.base), Value: copyBigInt(rec.value)}, nil
}

func (l *Ledger) Entry(addr [20]byte) WeightEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	rec, ok := l.entries[addr]
	if !ok {
		base := copyBigInt(l.floor)
		if base == nil {
			base = big.NewInt(0)
		}
		return WeightEntry{Base: base, Value: copyBigInt(base)}
	}
	return WeightEntry{Base: copyBigInt(rec.base), Value: copyBigInt(rec.value)}
}

func (l *Ledger) ApplyDecay(addr [20]byte, amount *big.Int) (*WeightUpdate, error) {
	if amount != nil && amount.Sign() < 0 {
		return nil, errors.New("potso: decay amount cannot be negative")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	rec := l.getOrCreate(addr)
	previous := copyBigInt(rec.value)
	if previous == nil {
		previous = copyBigInt(rec.base)
	}
	if previous == nil {
		previous = copyBigInt(l.floor)
		if previous == nil {
			previous = big.NewInt(0)
		}
	}
	var requested *big.Int
	if amount == nil {
		requested = big.NewInt(0)
	} else {
		requested = new(big.Int).Set(amount)
	}
	proposed := new(big.Int).Sub(previous, requested)
	clamped := false
	if l.floor != nil && proposed.Cmp(l.floor) < 0 {
		proposed.Set(l.floor)
		clamped = true
	}
	if l.ceil != nil && proposed.Cmp(l.ceil) > 0 {
		proposed.Set(l.ceil)
		clamped = true
	}
	rec.value = new(big.Int).Set(proposed)
	applied := new(big.Int).Sub(previous, rec.value)
	if applied.Sign() < 0 {
		applied.SetInt64(0)
	}
	update := &WeightUpdate{
		Offender: addr,
		Previous: previous,
		Current:  copyBigInt(rec.value),
		Applied:  applied,
		Clamped:  clamped || applied.Cmp(requested) != 0,
	}
	return update, nil
}

func (l *Ledger) WasPenaltyApplied(hash [32]byte, offender [20]byte) bool {
	key := penaltyKey(hash, offender)
	l.mu.RLock()
	defer l.mu.RUnlock()
	_, ok := l.applied[key]
	return ok
}

func (l *Ledger) MarkPenaltyApplied(hash [32]byte, offender [20]byte) {
	key := penaltyKey(hash, offender)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.applied[key] = struct{}{}
}

func penaltyKey(hash [32]byte, offender [20]byte) string {
	buf := make([]byte, 0, len(hash)*2+len(offender)*2)
	buf = append(buf, []byte(hex.EncodeToString(hash[:]))...)
	buf = append(buf, ':')
	buf = append(buf, []byte(hex.EncodeToString(offender[:]))...)
	return string(buf)
}
