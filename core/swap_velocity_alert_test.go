package core

import (
	"math/big"
	"testing"

	"nhbchain/native/swap"
)

// The velocity alert used to be built without the configured limit, so its
// allowedMints attribute was always "0" (PL-DC-34). The event is still dropped
// when the transaction fails (the whole transaction's events are discarded),
// so this changes no stored state; the test pins the content of the event the
// processor builds.
func TestEmitSwapRiskViolationVelocityCarriesAllowedMints(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	voucher := &swap.VoucherV1{Recipient: [20]byte{9}, Amount: big.NewInt(5)}
	violation := &swap.RiskViolation{
		Code:          swap.RiskCodeVelocity,
		WindowSeconds: 600,
		Count:         3,
		Limit:         big.NewInt(3),
	}
	err := sp.emitSwapRiskViolation(violation, "provider-x", "tx-1", voucher)
	if err != ErrSwapVelocityExceeded {
		t.Fatalf("expected ErrSwapVelocityExceeded, got %v", err)
	}
	evts := sp.Events()
	if len(evts) != 1 {
		t.Fatalf("expected one event, got %d", len(evts))
	}
	got := evts[0].Attributes
	if got["allowedMints"] != "3" {
		t.Fatalf("allowedMints = %q, want 3", got["allowedMints"])
	}
	if got["observedCount"] != "3" || got["windowSeconds"] != "600" {
		t.Fatalf("unexpected attributes: %v", got)
	}
}

// A violation without a limit (older callers) still produces a valid event.
func TestEmitSwapRiskViolationVelocityWithoutLimitIsZero(t *testing.T) {
	sp, _ := newTestStateProcessor(t)
	voucher := &swap.VoucherV1{Recipient: [20]byte{9}, Amount: big.NewInt(5)}
	violation := &swap.RiskViolation{Code: swap.RiskCodeVelocity, WindowSeconds: 600, Count: 3}
	if err := sp.emitSwapRiskViolation(violation, "p", "t", voucher); err != ErrSwapVelocityExceeded {
		t.Fatalf("expected ErrSwapVelocityExceeded, got %v", err)
	}
	if got := sp.Events()[0].Attributes["allowedMints"]; got != "0" {
		t.Fatalf("allowedMints = %q, want 0", got)
	}
}
