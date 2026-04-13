package swap

import (
	"testing"
	"time"

	"nhbchain/crypto"
)

func TestSanctionsCheckerDenyList(t *testing.T) {
	var denied [20]byte
	denied[0] = 1
	addr := crypto.MustNewAddress(crypto.NHBPrefix, denied[:])
	cfg := SanctionsConfig{DenyList: []string{addr.String()}}
	params, err := cfg.Parameters()
	if err != nil {
		t.Fatalf("parameters: %v", err)
	}
	checker := params.Checker()
	if checker(denied) {
		t.Fatalf("expected checker to block denied address")
	}
	var allowed [20]byte
	allowed[0] = 2
	if !checker(allowed) {
		t.Fatalf("expected checker to allow address not in deny list")
	}
}

func TestSanctionsLogRecordsFailures(t *testing.T) {
	store := newMemoryStore()
	log := NewSanctionsLog(store)
	addr := [20]byte{5}
	log.SetClock(func() time.Time { return time.Unix(1_000, 0) })
	if err := log.RecordFailure(addr, "provider", "tx-1"); err != nil {
		t.Fatalf("record failure: %v", err)
	}
	failures, err := log.Failures(addr)
	if err != nil {
		t.Fatalf("failures: %v", err)
	}
	if len(failures) != 1 {
		t.Fatalf("expected one failure, got %d", len(failures))
	}
	if failures[0].Provider != "provider" || failures[0].ProviderTxID != "tx-1" {
		t.Fatalf("unexpected failure record: %+v", failures[0])
	}
	if failures[0].Timestamp != 1_000 {
		t.Fatalf("unexpected timestamp: %d", failures[0].Timestamp)
	}
}
