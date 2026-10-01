package core

import (
	"errors"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/native/potso"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

// TestProcessPotsoRewardEpoch_TreasuryShortfallReachable is a regression test
// for ledger PL-DC-29, sub-issue 1: processPotsoRewardEpoch used to compute
// the reward budget as min(emission, treasuryBalance) BEFORE calling
// potso.ComputeRewards, which made totalPaid structurally incapable of ever
// exceeding treasuryBalance -- the later `if totalPaid > treasuryBalance`
// shortfall check could therefore never fire in the default/auto payout
// path. With the fix, the budget is derived from emission alone, so a
// treasury that genuinely cannot cover the configured emission is now
// discovered by the real payout attempt and surfaces as
// potso.ErrInsufficientTreasury instead of silently succeeding with a
// quietly reduced payout.
func TestProcessPotsoRewardEpoch_TreasuryShortfallReachable(t *testing.T) {
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

	treasury := [20]byte{0x51}
	cfg := potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(1000),
		TreasuryAddress:    treasury,
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
		// PayoutMode left at its zero value, which Normalise()s to auto --
		// this is the default/auto payout path the bug report names.
	}
	if err := sp.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("set potso config: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	treasuryAcc, err := manager.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("treasury account: %v", err)
	}
	// The treasury can cover only a tiny fraction of the configured 1000
	// emission. Under the pre-fix bug, budget would have been silently
	// capped to this 10, so totalPaid (<=10) could never exceed
	// treasuryBalance (10) and the shortfall check was dead code.
	treasuryAcc.BalanceZNHB = big.NewInt(10)
	if err := manager.PutAccount(treasury[:], treasuryAcc); err != nil {
		t.Fatalf("store treasury: %v", err)
	}

	participant := [20]byte{0x52}
	if err := manager.PotsoStakeSetBondedTotal(participant, big.NewInt(100)); err != nil {
		t.Fatalf("set stake: %v", err)
	}
	// Sole participant in both the stake and engagement pools, so their
	// composite weight is exactly 1.0 regardless of alpha -- totalPaid will
	// equal the full (uncapped) budget of 1000, comfortably exceeding the
	// treasury's 10.
	if err := manager.PotsoMetricsAddEngagement(0, participant, 0, 0, 30*60); err != nil {
		t.Fatalf("seed engagement: %v", err)
	}

	now := time.Unix(1_700_200_000, 0).UTC()
	if err := sp.ProcessBlockLifecycle(1, now.Add(-time.Second).Unix()); err != nil {
		t.Fatalf("process block 1: %v", err)
	}
	err = sp.ProcessBlockLifecycle(2, now.Unix())
	if !errors.Is(err, potso.ErrInsufficientTreasury) {
		t.Fatalf("expected ErrInsufficientTreasury from epoch close, got: %v", err)
	}

	// The epoch must not be marked processed when the payout attempt fails
	// outright -- a silently-succeeded, quietly-reduced payout is exactly
	// the behavior this fix removes.
	if _, ok, err := manager.PotsoRewardsLastProcessedEpoch(); err != nil {
		t.Fatalf("last processed lookup: %v", err)
	} else if ok {
		t.Fatalf("epoch should not be marked processed after a treasury shortfall")
	}
}

// TestProcessPotsoRewardEpoch_ClaimModeReservesTreasuryAcrossEpochs is a
// regression test for ledger PL-DC-29, sub-issue 2: in
// RewardPayoutModeClaim, winners used to be recorded via
// PotsoRewardsSetClaim with no treasury debit or reservation at epoch close
// -- only the default/auto payout branch actually debited the treasury.
// Since the treasury balance read at the top of processPotsoRewardEpoch is a
// fresh, undebited read every epoch, successive claim-mode epochs could each
// size their budget against the same unreserved balance, promising more in
// claimable rewards across epochs than the treasury could ever actually pay
// out. With the fix, the claim-mode branch reserves/debits the full
// obligation from the treasury at the moment it is created (epoch close),
// so a second epoch's fresh balance read already reflects the first epoch's
// still-unclaimed reservation.
func TestProcessPotsoRewardEpoch_ClaimModeReservesTreasuryAcrossEpochs(t *testing.T) {
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

	treasury := [20]byte{0x61}
	cfg := potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(600),
		TreasuryAddress:    treasury,
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
		PayoutMode:         potso.RewardPayoutModeClaim,
	}
	if err := sp.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("set potso config: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	treasuryAcc, err := manager.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("treasury account: %v", err)
	}
	// Enough to fund exactly ONE epoch's 600 emission, but not two (1200).
	treasuryAcc.BalanceZNHB = big.NewInt(1000)
	if err := manager.PutAccount(treasury[:], treasuryAcc); err != nil {
		t.Fatalf("store treasury: %v", err)
	}

	participant := [20]byte{0x62}
	if err := manager.PotsoStakeSetBondedTotal(participant, big.NewInt(100)); err != nil {
		t.Fatalf("set stake: %v", err)
	}

	base := time.Unix(1_700_300_000, 0).UTC()

	// Epoch 0 (heights 1-2): sole participant earns the full 600 budget as
	// an unclaimed obligation. The treasury can afford this.
	if err := manager.PotsoMetricsAddEngagement(0, participant, 0, 0, 30*60); err != nil {
		t.Fatalf("seed engagement epoch 0: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(1, base.Add(-time.Second).Unix()); err != nil {
		t.Fatalf("process block 1: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(2, base.Unix()); err != nil {
		t.Fatalf("epoch 0 close should succeed (treasury can fund one epoch): %v", err)
	}

	meta0, ok, err := manager.PotsoRewardsGetMeta(0)
	if err != nil || !ok || meta0 == nil {
		t.Fatalf("expected epoch 0 meta: ok=%v err=%v", ok, err)
	}
	if meta0.TotalPaid.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected epoch 0 total paid of 600, got %s", meta0.TotalPaid)
	}

	reserved, err := manager.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("reload treasury after epoch 0: %v", err)
	}
	if reserved.BalanceZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected treasury reserved down to 400 after epoch 0 close (claim obligation unpaid), got %s", reserved.BalanceZNHB)
	}

	claim0, ok, err := manager.PotsoRewardsGetClaim(0, participant)
	if err != nil || !ok || claim0 == nil {
		t.Fatalf("expected epoch 0 claim record: ok=%v err=%v", ok, err)
	}
	if claim0.Claimed {
		t.Fatalf("epoch 0 reward should still be unclaimed")
	}

	// Epoch 1 (heights 3-4): identical participant/emission. Before the fix,
	// this epoch's budget would be sized against the treasury's full,
	// never-debited balance and silently approved. After the fix, the
	// treasury's genuinely remaining 400 cannot cover another 600, and the
	// shared shortfall check (now reachable per sub-issue 1's fix) catches
	// it at this epoch's close.
	if err := manager.PotsoMetricsAddEngagement(1, participant, 0, 0, 30*60); err != nil {
		t.Fatalf("seed engagement epoch 1: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(3, base.Add(24*time.Hour-time.Second).Unix()); err != nil {
		t.Fatalf("process block 3: %v", err)
	}
	err = sp.ProcessBlockLifecycle(4, base.Add(24*time.Hour).Unix())
	if !errors.Is(err, potso.ErrInsufficientTreasury) {
		t.Fatalf("expected epoch 1 close to be caught by the treasury shortfall check, got: %v", err)
	}

	// Epoch 0's reservation and claim record must be untouched by the
	// failed epoch 1 attempt.
	stillReserved, err := manager.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("reload treasury after epoch 1 failure: %v", err)
	}
	if stillReserved.BalanceZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("epoch 0's reservation should be unaffected by epoch 1's failure, got %s", stillReserved.BalanceZNHB)
	}
}

// TestComputeRewardsZeroBudgetStillReturnsWeightSnapshot is a regression
// test for ledger PL-DC-29, sub-issue 3: potso.ComputeRewards used to return
// a nil WeightSnapshot whenever budget <= 0 (treasury empty or emission 0).
// Weight computation must be independent of whether there is any reward
// budget to pay out this epoch -- governance voting should never be gated
// on treasury funding.
func TestComputeRewardsZeroBudgetStillReturnsWeightSnapshot(t *testing.T) {
	cfg := potso.DefaultRewardConfig()
	cfg.AlphaStakeBps = 7000

	participant := [20]byte{0x70}
	snapshot := potso.RewardSnapshot{
		Epoch: 5,
		Entries: []potso.RewardSnapshotEntry{
			{
				Address: participant,
				Stake:   big.NewInt(100),
				Meter:   potso.EngagementMeter{},
			},
		},
	}

	outcome, err := potso.ComputeRewards(cfg, potso.DefaultWeightParams(), snapshot, big.NewInt(0))
	if err != nil {
		t.Fatalf("compute rewards: %v", err)
	}
	if outcome.WeightSnapshot == nil {
		t.Fatalf("expected a non-nil weight snapshot even though budget is zero")
	}
	if len(outcome.WeightSnapshot.Entries) != 1 {
		t.Fatalf("expected 1 weight entry, got %d", len(outcome.WeightSnapshot.Entries))
	}
	if outcome.WeightSnapshot.Entries[0].WeightBps == 0 {
		t.Fatalf("expected the sole staked participant to carry nonzero weight")
	}
	if outcome.TotalPaid.Sign() != 0 {
		t.Fatalf("expected no payout with zero budget, got %s", outcome.TotalPaid)
	}
	if len(outcome.Winners) != 0 {
		t.Fatalf("expected no winners with zero budget, got %d", len(outcome.Winners))
	}
}

// TestProcessPotsoRewardEpoch_ZeroBudgetStillEnablesGovernanceVote is the
// end-to-end regression test for ledger PL-DC-29, sub-issue 3: it drives the
// real processPotsoRewardEpoch function (not just potso.ComputeRewards in
// isolation) through a zero-emission epoch and then confirms a real
// governance.Engine's CastVote -- which depends on exactly the weight
// snapshot this function writes -- now succeeds instead of failing with
// "potso snapshot unavailable".
//
// EmissionPerEpoch is set to zero and EpochLengthBlocks is left unset: the
// production maybeProcessPotsoRewards wrapper gates on EmissionPerEpoch > 0
// before ever calling processPotsoRewardEpoch (an orchestration-level
// concern untouched by this fix, and the reason a zero-budget epoch can no
// longer arise merely by draining the treasury once sub-issue 1 is fixed),
// so this test calls processPotsoRewardEpoch directly -- exactly the
// function the bug report names -- to exercise its zero-budget behavior.
func TestProcessPotsoRewardEpoch_ZeroBudgetStillEnablesGovernanceVote(t *testing.T) {
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

	treasury := [20]byte{0x80}
	cfg := potso.RewardConfig{
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(0),
		TreasuryAddress:    treasury,
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
	}

	manager := nhbstate.NewManager(sp.Trie)
	voter := [20]byte{0x81}
	if err := manager.PotsoStakeSetBondedTotal(voter, big.NewInt(500)); err != nil {
		t.Fatalf("set stake: %v", err)
	}

	const epochNumber = uint64(0)
	if err := sp.processPotsoRewardEpoch(manager, cfg, epochNumber, "2026-01-01", time.Now().Unix()); err != nil {
		t.Fatalf("process zero-budget epoch: %v", err)
	}
	if err := manager.PotsoRewardsSetLastProcessedEpoch(epochNumber); err != nil {
		t.Fatalf("mark epoch processed: %v", err)
	}

	govSnapshot, ok, err := manager.SnapshotPotsoWeights(epochNumber)
	if err != nil {
		t.Fatalf("snapshot potso weights: %v", err)
	}
	if !ok || govSnapshot == nil {
		t.Fatalf("expected a governance-facing weight snapshot even though this epoch had zero reward budget")
	}
	if len(govSnapshot.Entries) != 1 || govSnapshot.Entries[0].WeightBps == 0 {
		t.Fatalf("expected the sole staked voter to carry nonzero recorded weight")
	}

	// Wire a real governance engine against the same trie, exactly as
	// StateProcessor.governanceEngine() does in production.
	engine := sp.governanceEngine()
	now := time.Now().UTC()
	engine.SetNowFunc(func() time.Time { return now })

	proposal := &governance.Proposal{
		ID:          1,
		Submitter:   crypto.MustNewAddress(crypto.NHBPrefix, voter[:]),
		Status:      governance.ProposalStatusVotingPeriod,
		Deposit:     big.NewInt(0),
		SubmitTime:  now.Add(-time.Hour),
		VotingStart: now.Add(-time.Minute),
		VotingEnd:   now.Add(time.Hour),
	}
	if err := manager.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}

	if err := engine.CastVote(proposal.ID, voter, "yes"); err != nil {
		t.Fatalf("expected CastVote to succeed off the zero-budget epoch's snapshot, got: %v", err)
	}

	votes, err := manager.GovernanceListVotes(proposal.ID)
	if err != nil {
		t.Fatalf("list votes: %v", err)
	}
	if len(votes) != 1 {
		t.Fatalf("expected one recorded vote, got %d", len(votes))
	}
	if votes[0].PowerBps == 0 {
		t.Fatalf("expected nonzero recorded voting power")
	}
}
