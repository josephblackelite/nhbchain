package state

import "math/big"

// GlobalIndex captures the protocol-wide staking index metadata persisted in
// deterministic key/value form. The accumulator value is stored as a
// fixed-precision UQ128x128 byte slice alongside book-keeping metadata used to
// calculate time-dependent reward accruals.
type GlobalIndex struct {
	UQ128x128      []byte
	LastUpdateUnix int64
	YTDEmissions   *big.Int
}

// AccountSnap models the staking snapshot persisted for a delegator. The
// encoded representation mirrors the deterministic layout used for
// serialization, ensuring cross-client compatibility when performing state
// migrations or generating genesis dumps.
type AccountSnap struct {
	LastIndexUQ128x128 []byte
	AccruedZNHB        *big.Int
	LastPayoutUnix     int64
}

type storedGlobalIndex struct {
	UQ128x128      []byte
	LastUpdateUnix uint64
	YTDEmissions   *big.Int
}

func newStoredGlobalIndex(idx *GlobalIndex) *storedGlobalIndex {
	if idx == nil {
		idx = &GlobalIndex{}
	}
	ts := idx.LastUpdateUnix
	if ts < 0 {
		ts = 0
	}
	stored := &storedGlobalIndex{
		LastUpdateUnix: uint64(ts),
		UQ128x128:      append([]byte(nil), idx.UQ128x128...),
		YTDEmissions:   big.NewInt(0),
	}
	if idx.YTDEmissions != nil {
		stored.YTDEmissions = new(big.Int).Set(idx.YTDEmissions)
	}
	return stored
}

func (s *storedGlobalIndex) toGlobalIndex() *GlobalIndex {
	if s == nil {
		return &GlobalIndex{YTDEmissions: big.NewInt(0)}
	}
	idx := &GlobalIndex{
		LastUpdateUnix: int64(s.LastUpdateUnix),
		UQ128x128:      append([]byte(nil), s.UQ128x128...),
		YTDEmissions:   big.NewInt(0),
	}
	if s.YTDEmissions != nil {
		idx.YTDEmissions = new(big.Int).Set(s.YTDEmissions)
	}
	return idx
}

type storedAccountSnap struct {
	LastIndexUQ128x128 []byte
	AccruedZNHB        *big.Int
	LastPayoutUnix     uint64
}

func newStoredAccountSnap(snap *AccountSnap) *storedAccountSnap {
	if snap == nil {
		snap = &AccountSnap{}
	}
	ts := snap.LastPayoutUnix
	if ts < 0 {
		ts = 0
	}
	stored := &storedAccountSnap{
		LastIndexUQ128x128: append([]byte(nil), snap.LastIndexUQ128x128...),
		LastPayoutUnix:     uint64(ts),
		AccruedZNHB:        big.NewInt(0),
	}
	if snap.AccruedZNHB != nil {
		stored.AccruedZNHB = new(big.Int).Set(snap.AccruedZNHB)
	}
	return stored
}

func (s *storedAccountSnap) toAccountSnap() *AccountSnap {
	if s == nil {
		return &AccountSnap{AccruedZNHB: big.NewInt(0)}
	}
	snap := &AccountSnap{
		LastIndexUQ128x128: append([]byte(nil), s.LastIndexUQ128x128...),
		LastPayoutUnix:     int64(s.LastPayoutUnix),
		AccruedZNHB:        big.NewInt(0),
	}
	if s.AccruedZNHB != nil {
		snap.AccruedZNHB = new(big.Int).Set(s.AccruedZNHB)
	}
	return snap
}
