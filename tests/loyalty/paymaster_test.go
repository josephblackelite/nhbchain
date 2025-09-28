package loyalty_test

import (
	"math/big"
	"testing"

	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/native/loyalty"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func newStateProcessor(t *testing.T) *core.StateProcessor {
	t.Helper()
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := core.NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("state processor: %v", err)
	}
	return sp
}

func TestProgramMetersRoundTrip(t *testing.T) {
	sp := newStateProcessor(t)

	var programID loyalty.ProgramID
	programID[0] = 0xAB
	day := "2024-01-20"
	epoch := uint64(42)
	addr := []byte("user-addr")

	if err := sp.SetLoyaltyProgramDailyTotalAccrued(programID, day, big.NewInt(125)); err != nil {
		t.Fatalf("set daily total: %v", err)
	}
	daily, err := sp.LoyaltyProgramDailyTotalAccrued(programID, day)
	if err != nil {
		t.Fatalf("get daily total: %v", err)
	}
	if daily.String() != "125" {
		t.Fatalf("expected daily total 125, got %s", daily.String())
	}

	if err := sp.SetLoyaltyProgramEpochAccrued(programID, epoch, big.NewInt(900)); err != nil {
		t.Fatalf("set epoch: %v", err)
	}
	epochTotal, err := sp.LoyaltyProgramEpochAccrued(programID, epoch)
	if err != nil {
		t.Fatalf("get epoch: %v", err)
	}
	if epochTotal.String() != "900" {
		t.Fatalf("expected epoch total 900, got %s", epochTotal.String())
	}

	if err := sp.SetLoyaltyProgramIssuanceAccrued(programID, addr, big.NewInt(321)); err != nil {
		t.Fatalf("set issuance: %v", err)
	}
	issuance, err := sp.LoyaltyProgramIssuanceAccrued(programID, addr)
	if err != nil {
		t.Fatalf("get issuance: %v", err)
	}
	if issuance.String() != "321" {
		t.Fatalf("expected issuance total 321, got %s", issuance.String())
	}

	manager := nhbstate.NewManager(sp.Trie)
	missingDay, err := manager.LoyaltyProgramDailyTotalAccrued(programID, "2024-01-21")
	if err != nil {
		t.Fatalf("zero daily total: %v", err)
	}
	if missingDay.Sign() != 0 {
		t.Fatalf("expected zero for missing day, got %s", missingDay.String())
	}
}
