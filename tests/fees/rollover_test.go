package fees_test

import (
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/fees"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func TestMonthlyRolloverSnapshots(t *testing.T) {
	db := storage.NewMemDB()
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	manager := nhbstate.NewManager(trie)

	jan31 := time.Date(2024, time.January, 31, 23, 0, 0, 0, time.UTC)
	status, err := manager.FeesEnsureMonthlyRollover(jan31)
	if err != nil {
		t.Fatalf("ensure rollover: %v", err)
	}
	if status.Window != "202401" {
		t.Fatalf("unexpected window: %s", status.Window)
	}
	if status.LastRollover != "" {
		t.Fatalf("expected empty last rollover, got %s", status.LastRollover)
	}
	if status.Used != 0 || status.Limit != 0 {
		t.Fatalf("expected zeroed counters, got used=%d limit=%d", status.Used, status.Limit)
	}

	var payer [20]byte
	janWindow := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= 3; i++ {
		if err := manager.FeesPutCounter(fees.DomainPOS, payer, janWindow, fees.FreeTierScopeAggregate, uint64(i)); err != nil {
			t.Fatalf("put counter %d: %v", i, err)
		}
		if err := manager.FeesRecordUsage(janWindow, 100, uint64(i), true); err != nil {
			t.Fatalf("record usage %d: %v", i, err)
		}
	}
	if err := manager.FeesPutCounter(fees.DomainPOS, payer, janWindow, fees.FreeTierScopeAggregate, 101); err != nil {
		t.Fatalf("put counter 101: %v", err)
	}
	if err := manager.FeesRecordUsage(janWindow, 100, 101, false); err != nil {
		t.Fatalf("record paid usage: %v", err)
	}

	status, err = manager.FeesMonthlyStatus()
	if err != nil {
		t.Fatalf("monthly status: %v", err)
	}
	if status.Used != 3 {
		t.Fatalf("expected 3 used, got %d", status.Used)
	}
	if status.Limit != 100 {
		t.Fatalf("expected limit 100, got %d", status.Limit)
	}
	if status.Wallets != 1 {
		t.Fatalf("expected wallets=1, got %d", status.Wallets)
	}
	if status.Remaining != 97 {
		t.Fatalf("expected remaining 97, got %d", status.Remaining)
	}

	feb1 := time.Date(2024, time.February, 1, 0, 0, 0, 0, time.UTC)
	febStatus, err := manager.FeesEnsureMonthlyRollover(feb1)
	if err != nil {
		t.Fatalf("rollover feb: %v", err)
	}
	if febStatus.Window != "202402" {
		t.Fatalf("unexpected feb window: %s", febStatus.Window)
	}
	if febStatus.LastRollover != "202401" {
		t.Fatalf("expected last rollover 202401, got %s", febStatus.LastRollover)
	}
	if febStatus.Used != 0 || febStatus.Limit != 0 {
		t.Fatalf("expected feb counters reset, got used=%d limit=%d", febStatus.Used, febStatus.Limit)
	}

	snapshot, ok, err := manager.FeesMonthlySnapshot("202401")
	if err != nil {
		t.Fatalf("load snapshot: %v", err)
	}
	if !ok {
		t.Fatalf("expected january snapshot")
	}
	if snapshot.Used != 3 || snapshot.Limit != 100 || snapshot.Remaining != 97 {
		t.Fatalf("unexpected snapshot totals: %+v", snapshot)
	}
	if snapshot.CompletedAt.IsZero() {
		t.Fatalf("expected snapshot timestamp")
	}

	again, err := manager.FeesEnsureMonthlyRollover(feb1.Add(6 * time.Hour))
	if err != nil {
		t.Fatalf("repeat rollover: %v", err)
	}
	if again.Window != "202402" || again.LastRollover != "202401" {
		t.Fatalf("unexpected repeat status: %+v", again)
	}

	febWindow := time.Date(2024, time.February, 1, 0, 0, 0, 0, time.UTC)
	if err := manager.FeesPutCounter(fees.DomainPOS, payer, febWindow, fees.FreeTierScopeAggregate, 1); err != nil {
		t.Fatalf("put feb counter: %v", err)
	}
	if err := manager.FeesRecordUsage(febWindow, 100, 1, true); err != nil {
		t.Fatalf("record feb usage: %v", err)
	}
	finalStatus, err := manager.FeesMonthlyStatus()
	if err != nil {
		t.Fatalf("final status: %v", err)
	}
	if finalStatus.Used != 1 || finalStatus.Remaining != 99 || finalStatus.Limit != 100 {
		t.Fatalf("unexpected feb status: %+v", finalStatus)
	}
	if finalStatus.Wallets != 1 {
		t.Fatalf("expected wallets=1 in feb, got %d", finalStatus.Wallets)
	}
}
