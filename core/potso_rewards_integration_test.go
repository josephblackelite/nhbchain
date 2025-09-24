package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/potso"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

func TestProcessPotsoRewardEpoch(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(db.Close)
	trie, err := statetrie.NewTrie(db, nil)
	if err != nil {
		t.Fatalf("new trie: %v", err)
	}
	sp, err := NewStateProcessor(trie)
	if err != nil {
		t.Fatalf("state processor: %v", err)
	}

	treasury := [20]byte{1}
	cfg := potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(900),
		TreasuryAddress:    treasury,
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
	}
	if err := sp.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("set potso config: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	treasuryAcc, err := manager.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("treasury account: %v", err)
	}
	treasuryAcc.BalanceZNHB = big.NewInt(900)
	if err := manager.PutAccount(treasury[:], treasuryAcc); err != nil {
		t.Fatalf("store treasury: %v", err)
	}

	participantA := [20]byte{2}
	participantB := [20]byte{3}
	if err := manager.PotsoStakeSetBondedTotal(participantA, big.NewInt(600)); err != nil {
		t.Fatalf("set stake A: %v", err)
	}
	if err := manager.PotsoStakeSetBondedTotal(participantB, big.NewInt(400)); err != nil {
		t.Fatalf("set stake B: %v", err)
	}

	now := time.Now().UTC()
	day := now.Format(potso.DayFormat)
	if err := manager.PotsoPutMeter(participantA, &potso.Meter{Day: day, UptimeSeconds: 30 * 60}); err != nil {
		t.Fatalf("put meter A: %v", err)
	}
	if err := manager.PotsoPutMeter(participantB, &potso.Meter{Day: day, UptimeSeconds: 10 * 60}); err != nil {
		t.Fatalf("put meter B: %v", err)
	}

	if err := sp.ProcessBlockLifecycle(1, now.Add(-time.Second).Unix()); err != nil {
		t.Fatalf("process block 1: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(2, now.Unix()); err != nil {
		t.Fatalf("process block 2: %v", err)
	}

	meta, ok, err := manager.PotsoRewardsGetMeta(0)
	if err != nil {
		t.Fatalf("get meta: %v", err)
	}
	if !ok || meta == nil {
		t.Fatalf("expected epoch meta")
	}
	if meta.TotalPaid.Cmp(big.NewInt(899)) != 0 {
		t.Fatalf("unexpected total paid: %s", meta.TotalPaid)
	}
	if meta.Winners != 2 {
		t.Fatalf("expected 2 winners, got %d", meta.Winners)
	}

	payoutA, ok, err := manager.PotsoRewardsGetPayout(0, participantA)
	if err != nil {
		t.Fatalf("payout A: %v", err)
	}
	if !ok {
		t.Fatalf("expected payout for participant A")
	}
	payoutB, ok, err := manager.PotsoRewardsGetPayout(0, participantB)
	if err != nil {
		t.Fatalf("payout B: %v", err)
	}
	if !ok {
		t.Fatalf("expected payout for participant B")
	}
	paid := new(big.Int).Add(payoutA, payoutB)
	if paid.Cmp(big.NewInt(899)) != 0 {
		t.Fatalf("payout sum mismatch: %s", paid)
	}

	updatedTreasury, err := manager.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("reload treasury: %v", err)
	}
	if updatedTreasury.BalanceZNHB.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("treasury balance unexpected: %s", updatedTreasury.BalanceZNHB)
	}

	events := sp.Events()
	if len(events) == 0 {
		t.Fatalf("expected events to be emitted")
	}
	foundEpoch := false
	foundPaid := 0
	for _, evt := range events {
		switch evt.Type {
		case "potso.reward.epoch":
			foundEpoch = true
		case "potso.reward.paid":
			foundPaid++
		}
	}
	if !foundEpoch || foundPaid != 2 {
		t.Fatalf("unexpected events: epoch=%v paid=%d", foundEpoch, foundPaid)
	}

	winners, err := manager.PotsoRewardsListWinners(0)
	if err != nil {
		t.Fatalf("list winners: %v", err)
	}
	if len(winners) != 2 {
		t.Fatalf("expected 2 stored winners, got %d", len(winners))
	}

	lastProcessed, ok, err := manager.PotsoRewardsLastProcessedEpoch()
	if err != nil {
		t.Fatalf("last processed: %v", err)
	}
	if !ok || lastProcessed != 0 {
		t.Fatalf("unexpected last processed epoch: %d", lastProcessed)
	}

	// Ensure no additional payouts occur when processing subsequent block in same epoch.
	if err := sp.ProcessBlockLifecycle(3, now.Add(time.Second).Unix()); err != nil {
		t.Fatalf("process block 3: %v", err)
	}
	repeatPayoutA, ok, err := manager.PotsoRewardsGetPayout(0, participantA)
	if err != nil || !ok || repeatPayoutA.Cmp(payoutA) != 0 {
		t.Fatalf("payout mutated after repeat processing")
	}
}
