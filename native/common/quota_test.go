package common

import (
	"errors"
	"testing"
)

func TestCheckQuotaRequestLimit(t *testing.T) {
	q := Quota{MaxRequestsPerMin: 10}
	prev := QuotaNow{EpochID: 1}

	next, err := CheckQuota(q, 1, prev, 10, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if next.ReqCount != 10 {
		t.Fatalf("unexpected request count: %d", next.ReqCount)
	}

	denied, err := CheckQuota(q, 1, next, 1, 0)
	if !errors.Is(err, ErrQuotaRequestsExceeded) {
		t.Fatalf("expected ErrQuotaRequestsExceeded, got %v", err)
	}
	if denied != next {
		t.Fatalf("expected counters to remain unchanged on denial")
	}

	rollover, err := CheckQuota(q, 2, next, 1, 0)
	if err != nil {
		t.Fatalf("unexpected error after epoch rollover: %v", err)
	}
	if rollover.EpochID != 2 || rollover.ReqCount != 1 {
		t.Fatalf("unexpected state after rollover: %+v", rollover)
	}
}

func TestCheckQuotaNHB(t *testing.T) {
	q := Quota{MaxNHBPerEpoch: 1000}
	prev := QuotaNow{EpochID: 5}

	next, err := CheckQuota(q, 5, prev, 0, 1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if next.NHBUsed != 1000 {
		t.Fatalf("unexpected nhb used: %d", next.NHBUsed)
	}

	denied, err := CheckQuota(q, 5, next, 0, 1)
	if !errors.Is(err, ErrQuotaNHBCapExceeded) {
		t.Fatalf("expected ErrQuotaNHBCapExceeded, got %v", err)
	}
	if denied != next {
		t.Fatalf("expected counters to remain unchanged on denial")
	}

	rollover, err := CheckQuota(q, 6, next, 0, 500)
	if err != nil {
		t.Fatalf("unexpected error after epoch rollover: %v", err)
	}
	if rollover.NHBUsed != 500 {
		t.Fatalf("unexpected nhb used after rollover: %d", rollover.NHBUsed)
	}
}
