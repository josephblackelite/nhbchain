package core

import (
	"bytes"
	"encoding/csv"
	"math/big"
	"strconv"
	"strings"
	"testing"
	"time"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
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

	now := time.Unix(1_700_000_300, 0).UTC()
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

	claimA, ok, err := manager.PotsoRewardsGetClaim(0, participantA)
	if err != nil || !ok || claimA == nil {
		t.Fatalf("expected claim record for participant A")
	}
	if !claimA.Claimed {
		t.Fatalf("expected participant A to be marked claimed")
	}
	if claimA.Mode != potso.RewardPayoutModeAuto {
		t.Fatalf("unexpected claim mode for participant A: %s", claimA.Mode)
	}
	if claimA.Amount.Cmp(payoutA) != 0 {
		t.Fatalf("claim amount mismatch: %s vs %s", claimA.Amount, payoutA)
	}

	historyA, err := manager.PotsoRewardsHistory(participantA)
	if err != nil {
		t.Fatalf("history A: %v", err)
	}
	if len(historyA) != 1 {
		t.Fatalf("expected one history entry, got %d", len(historyA))
	}
	if historyA[0].Mode != potso.RewardPayoutModeAuto {
		t.Fatalf("unexpected history mode: %s", historyA[0].Mode)
	}
	if historyA[0].Amount.Cmp(payoutA) != 0 {
		t.Fatalf("history amount mismatch: %s vs %s", historyA[0].Amount, payoutA)
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

func TestPotsoRewardClaimFlow(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(db.Close)

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := NewNode(db, validatorKey, "", true)
	if err != nil {
		t.Fatalf("new node: %v", err)
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
		PayoutMode:         potso.RewardPayoutModeClaim,
	}
	if err := node.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("set config: %v", err)
	}

	manager := nhbstate.NewManager(node.state.Trie)
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

	now := time.Unix(1_700_000_400, 0).UTC()
	day := now.Format(potso.DayFormat)
	if err := manager.PotsoPutMeter(participantA, &potso.Meter{Day: day, UptimeSeconds: 30 * 60}); err != nil {
		t.Fatalf("put meter A: %v", err)
	}
	if err := manager.PotsoPutMeter(participantB, &potso.Meter{Day: day, UptimeSeconds: 10 * 60}); err != nil {
		t.Fatalf("put meter B: %v", err)
	}

	if err := node.state.ProcessBlockLifecycle(1, now.Add(-time.Second).Unix()); err != nil {
		t.Fatalf("process block 1: %v", err)
	}
	if err := node.state.ProcessBlockLifecycle(2, now.Unix()); err != nil {
		t.Fatalf("process block 2: %v", err)
	}

	claimA, ok, err := manager.PotsoRewardsGetClaim(0, participantA)
	if err != nil || !ok || claimA == nil {
		t.Fatalf("expected claim entry for A")
	}
	if claimA.Claimed {
		t.Fatalf("claim should not be marked claimed before settlement")
	}
	if claimA.Mode != potso.RewardPayoutModeClaim {
		t.Fatalf("unexpected claim mode: %s", claimA.Mode)
	}

	historyA, err := manager.PotsoRewardsHistory(participantA)
	if err != nil {
		t.Fatalf("history retrieval: %v", err)
	}
	if len(historyA) != 0 {
		t.Fatalf("expected empty history before claim")
	}

	eventsList := node.state.Events()
	readyCount := 0
	paidCount := 0
	for _, evt := range eventsList {
		switch evt.Type {
		case events.TypePotsoRewardReady:
			readyCount++
		case events.TypePotsoRewardPaid:
			paidCount++
		}
	}
	if readyCount == 0 {
		t.Fatalf("expected ready events in claim mode")
	}
	if paidCount != 0 {
		t.Fatalf("expected no paid events before manual claim")
	}

	payoutA, _, err := manager.PotsoRewardsGetPayout(0, participantA)
	if err != nil {
		t.Fatalf("payout lookup: %v", err)
	}
	payoutB, _, err := manager.PotsoRewardsGetPayout(0, participantB)
	if err != nil {
		t.Fatalf("payout lookup B: %v", err)
	}

	paid, amount, err := node.PotsoRewardClaim(0, participantA)
	if err != nil {
		t.Fatalf("claim payout: %v", err)
	}
	if !paid {
		t.Fatalf("expected payout to occur on first claim")
	}
	if amount.Cmp(payoutA) != 0 {
		t.Fatalf("claimed amount mismatch: %s vs %s", amount, payoutA)
	}

	claimA, ok, err = manager.PotsoRewardsGetClaim(0, participantA)
	if err != nil || !ok || claimA == nil || !claimA.Claimed {
		t.Fatalf("claim should be marked claimed after payout")
	}
	if claimA.ClaimedAt == 0 {
		t.Fatalf("expected claimedAt timestamp to be recorded")
	}

	historyA, err = manager.PotsoRewardsHistory(participantA)
	if err != nil {
		t.Fatalf("history after claim: %v", err)
	}
	if len(historyA) != 1 {
		t.Fatalf("expected one history entry after claim, got %d", len(historyA))
	}
	if historyA[0].Mode != potso.RewardPayoutModeClaim {
		t.Fatalf("unexpected history mode after claim: %s", historyA[0].Mode)
	}

	paidAgain, _, err := node.PotsoRewardClaim(0, participantA)
	if err != nil {
		t.Fatalf("idempotent claim error: %v", err)
	}
	if paidAgain {
		t.Fatalf("expected idempotent claim to report paid=false")
	}

	updatedTreasury, err := manager.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("reload treasury: %v", err)
	}
	expectedTreasury := big.NewInt(900)
	expectedTreasury.Sub(expectedTreasury, payoutA)
	if updatedTreasury.BalanceZNHB.Cmp(expectedTreasury) != 0 {
		t.Fatalf("treasury not debited after claim: %s", updatedTreasury.BalanceZNHB)
	}

	eventsList = node.state.Events()
	paidCount = 0
	for _, evt := range eventsList {
		if evt.Type == events.TypePotsoRewardPaid {
			paidCount++
		}
	}
	if paidCount == 0 {
		t.Fatalf("expected paid event after manual claim")
	}

	csvData, totalPaid, winners, err := manager.PotsoRewardsBuildCSV(0)
	if err != nil {
		t.Fatalf("build csv: %v", err)
	}
	if winners != 2 {
		t.Fatalf("expected two winners in CSV, got %d", winners)
	}
	expectedTotal := new(big.Int).Add(payoutA, payoutB)
	if totalPaid.Cmp(expectedTotal) != 0 {
		t.Fatalf("csv total mismatch: %s vs %s", totalPaid, expectedTotal)
	}
	records, err := csv.NewReader(bytes.NewReader(csvData)).ReadAll()
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(records) != 3 { // header + 2 rows
		t.Fatalf("expected 3 CSV rows, got %d", len(records))
	}
	addrA := crypto.NewAddress(crypto.NHBPrefix, participantA[:]).String()
	addrB := crypto.NewAddress(crypto.NHBPrefix, participantB[:]).String()
	seenA := false
	seenB := false
	for _, row := range records[1:] {
		if len(row) != 5 {
			t.Fatalf("unexpected CSV columns: %v", row)
		}
		switch row[0] {
		case addrA:
			seenA = true
			if row[1] != payoutA.String() {
				t.Fatalf("csv amount mismatch for A: %s", row[1])
			}
			if row[2] != "true" {
				t.Fatalf("expected A to be marked claimed")
			}
			claimedAt, err := strconv.ParseUint(row[3], 10, 64)
			if err != nil || claimedAt == 0 {
				t.Fatalf("invalid claimedAt for A: %s", row[3])
			}
			if row[4] != string(potso.RewardPayoutModeClaim) {
				t.Fatalf("expected claim mode in CSV for A, got %s", row[4])
			}
		case addrB:
			seenB = true
			if row[1] != payoutB.String() {
				t.Fatalf("csv amount mismatch for B: %s", row[1])
			}
			if row[2] != "false" {
				t.Fatalf("expected B to be unclaimed")
			}
			if row[3] != "0" {
				t.Fatalf("expected claimedAt 0 for B, got %s", row[3])
			}
			if row[4] != string(potso.RewardPayoutModeClaim) {
				t.Fatalf("expected claim mode in CSV for B, got %s", row[4])
			}
		}
	}
	if !seenA || !seenB {
		t.Fatalf("csv rows missing: A=%v B=%v", seenA, seenB)
	}
}

func TestPotsoRewardHistoryPagination(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(db.Close)

	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := NewNode(db, validatorKey, "", true)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}

	treasury := [20]byte{9}
	cfg := potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(900),
		TreasuryAddress:    treasury,
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
		PayoutMode:         potso.RewardPayoutModeAuto,
	}
	if err := node.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("set config: %v", err)
	}

	manager := nhbstate.NewManager(node.state.Trie)
	treasuryAcc, err := manager.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("treasury account: %v", err)
	}
	treasuryAcc.BalanceZNHB = big.NewInt(3000)
	if err := manager.PutAccount(treasury[:], treasuryAcc); err != nil {
		t.Fatalf("store treasury: %v", err)
	}

	participant := [20]byte{4}
	other := [20]byte{5}
	if err := manager.PotsoStakeSetBondedTotal(participant, big.NewInt(600)); err != nil {
		t.Fatalf("set stake participant: %v", err)
	}
	if err := manager.PotsoStakeSetBondedTotal(other, big.NewInt(400)); err != nil {
		t.Fatalf("set stake other: %v", err)
	}

	base := time.Unix(1_700_000_500, 0).UTC()
	height := uint64(1)
	processEpoch := func(day string, ts time.Time) {
		if err := manager.PotsoPutMeter(participant, &potso.Meter{Day: day, UptimeSeconds: 30 * 60}); err != nil {
			t.Fatalf("meter participant: %v", err)
		}
		if err := manager.PotsoPutMeter(other, &potso.Meter{Day: day, UptimeSeconds: 10 * 60}); err != nil {
			t.Fatalf("meter other: %v", err)
		}
		if err := node.state.ProcessBlockLifecycle(height, ts.Add(-time.Second).Unix()); err != nil {
			t.Fatalf("process block %d: %v", height, err)
		}
		height++
		if err := node.state.ProcessBlockLifecycle(height, ts.Unix()); err != nil {
			t.Fatalf("process block %d: %v", height, err)
		}
		height++
	}

	// Epoch 0 auto
	processEpoch(base.Format(potso.DayFormat), base)

	// Switch to claim mode for epoch 1
	cfg.PayoutMode = potso.RewardPayoutModeClaim
	if err := node.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("switch to claim mode: %v", err)
	}
	processEpoch(base.Add(24*time.Hour).Format(potso.DayFormat), base.Add(24*time.Hour))
	payoutEpoch1, _, err := manager.PotsoRewardsGetPayout(1, participant)
	if err != nil {
		t.Fatalf("payout epoch1: %v", err)
	}
	if payoutEpoch1.Sign() == 0 {
		t.Fatalf("expected payout for epoch 1")
	}
	if paid, _, err := node.PotsoRewardClaim(1, participant); err != nil || !paid {
		t.Fatalf("claim epoch1: paid=%v err=%v", paid, err)
	}

	// Switch back to auto for epoch 2
	cfg.PayoutMode = potso.RewardPayoutModeAuto
	if err := node.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("switch to auto mode: %v", err)
	}
	processEpoch(base.Add(48*time.Hour).Format(potso.DayFormat), base.Add(48*time.Hour))

	entries, nextCursor, err := node.PotsoRewardsHistory(participant, "", 2)
	if err != nil {
		t.Fatalf("history page 1: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries on first page, got %d", len(entries))
	}
	if entries[0].Mode != potso.RewardPayoutModeAuto || entries[0].Epoch != 2 {
		t.Fatalf("expected latest entry to be epoch 2 auto, got epoch %d mode %s", entries[0].Epoch, entries[0].Mode)
	}
	if entries[1].Mode != potso.RewardPayoutModeClaim || entries[1].Epoch != 1 {
		t.Fatalf("expected second entry to be epoch 1 claim, got epoch %d mode %s", entries[1].Epoch, entries[1].Mode)
	}
	if strings.TrimSpace(nextCursor) == "" {
		t.Fatalf("expected next cursor for remaining history")
	}

	more, next, err := node.PotsoRewardsHistory(participant, nextCursor, 2)
	if err != nil {
		t.Fatalf("history page 2: %v", err)
	}
	if len(more) != 1 {
		t.Fatalf("expected remaining single entry, got %d", len(more))
	}
	if more[0].Epoch != 0 || more[0].Mode != potso.RewardPayoutModeAuto {
		t.Fatalf("unexpected oldest entry epoch=%d mode=%s", more[0].Epoch, more[0].Mode)
	}
	if next != "" {
		t.Fatalf("expected no further cursor, got %q", next)
	}
}
