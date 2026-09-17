package common

import (
	"errors"
	"fmt"
	"math"
)

var (
	ErrQuotaRequestsExceeded = errors.New("quota requests exceeded")
	ErrQuotaNHBCapExceeded   = errors.New("quota nhb cap exceeded")
	ErrQuotaCounterOverflow  = errors.New("quota counter overflow")
)

// Store provides persistence for quota counters.
type Store interface {
	Load(module string, epoch uint64, addr []byte) (QuotaNow, bool, error)
	Save(module string, epoch uint64, addr []byte, counters QuotaNow) error
}

// QuotaNow captures the current quota usage counters for an address.
type QuotaNow struct {
	ReqCount uint32
	NHBUsed  uint64
	EpochID  uint64
}

// Quota defines the limits enforced for a module interaction per address.
type Quota struct {
	MaxRequestsPerMin uint32
	MaxNHBPerEpoch    uint64
	EpochSeconds      uint32
}

// CheckQuota verifies whether the additional request and NHB usage fit within the
// configured quota. The returned QuotaNow reflects the updated counters when the
// quota is not exceeded.
func CheckQuota(q Quota, nowEpoch uint64, prev QuotaNow, addReq uint32, addNHB uint64) (QuotaNow, error) {
	next := prev
	if prev.EpochID != nowEpoch {
		next = QuotaNow{EpochID: nowEpoch}
	}

	if addReq > 0 {
		if next.ReqCount > math.MaxUint32-addReq {
			return prev, ErrQuotaCounterOverflow
		}
		next.ReqCount += addReq
	}
	if q.MaxRequestsPerMin > 0 && next.ReqCount > q.MaxRequestsPerMin {
		return prev, ErrQuotaRequestsExceeded
	}

	if addNHB > 0 {
		if next.NHBUsed > math.MaxUint64-addNHB {
			return prev, ErrQuotaCounterOverflow
		}
		next.NHBUsed += addNHB
	}
	if q.MaxNHBPerEpoch > 0 && next.NHBUsed > q.MaxNHBPerEpoch {
		return prev, ErrQuotaNHBCapExceeded
	}

	return next, nil
}

// Apply loads the persisted counters for the provided address and updates them
// with the supplied increments when within quota limits. The updated counters
// are stored back to the underlying persistence layer. When the quota is
// exceeded the original counters are returned alongside the error.
func Apply(store Store, module string, nowEpoch uint64, addr []byte, q Quota, addReq uint32, addNHB uint64) (QuotaNow, error) {
	if store == nil {
		return QuotaNow{}, fmt.Errorf("quota: store unavailable")
	}
	if len(addr) == 0 {
		return QuotaNow{}, fmt.Errorf("quota: address required")
	}
	prev, _, err := store.Load(module, nowEpoch, addr)
	if err != nil {
		return QuotaNow{}, err
	}
	next, err := CheckQuota(q, nowEpoch, prev, addReq, addNHB)
	if err != nil {
		return prev, err
	}
	if err := store.Save(module, nowEpoch, addr, next); err != nil {
		return QuotaNow{}, err
	}
	return next, nil
}
