package core

import (
	"math/big"
	"testing"
	"time"

	potsoevents "nhbchain/core/events"
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
// exceeding treasuryBalance -- the shortfall check could therefore never fire
// in the default/auto payout path. With the fix, the budget is derived from
// emission alone, so a treasury that genuinely cannot cover the configured
// emission is now discovered by the real payout attempt.
//
// Discovering the shortfall must NOT abort the block or the chain, though:
// processPotsoRewardEpoch is invoked unconditionally on every block from
// ProcessBlockLifecycle (core/epochs.go), not from a discretionary,
// skippable transaction, and every ProcessBlockLifecycle caller in
// core/node.go treats its error as an unconditional whole-block abort with
// no per-epoch retry disposition (unlike classifyProposalError's
// skip/prune handling for a single bad transaction). An error here would
// therefore halt block production chain-wide, retried identically forever,
// since maybeProcessPotsoRewards only marks an epoch processed after a
// successful call. The correct, current behavior is: the block still
// commits, the epoch is still marked processed (never retried), the payout
// for this epoch is degraded to zero, and the shortfall is surfaced via the
// persisted RewardEpochMeta.Shortfall flag and a dedicated
// potso.reward.shortfall event for operators/monitoring to observe.
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
	// The block carrying the shortfall epoch's close must still succeed --
	// this is the heart of the fix. Returning potso.ErrInsufficientTreasury
	// here, as the pre-fix code did, would abort ProcessBlockLifecycle and
	// therefore the whole block.
	if err := sp.ProcessBlockLifecycle(2, now.Unix()); err != nil {
		t.Fatalf("epoch close with a treasury shortfall must not abort the block, got: %v", err)
	}

	// The epoch MUST be marked processed despite the shortfall -- otherwise
	// it is retried, identically, on every subsequent block forever (the
	// exact chain-halt this fix removes).
	lastProcessed, ok, err := manager.PotsoRewardsLastProcessedEpoch()
	if err != nil {
		t.Fatalf("last processed lookup: %v", err)
	} else if !ok || lastProcessed != 0 {
		t.Fatalf("expected epoch 0 to be marked processed despite the shortfall: ok=%v lastProcessed=%d", ok, lastProcessed)
	}

	// The shortfall must be durably surfaced, not silently swallowed: the
	// persisted meta carries a Shortfall flag and TotalPaid of zero (the
	// degraded outcome), not the unaffordable amount potso.ComputeRewards
	// originally proposed.
	meta, ok, err := manager.PotsoRewardsGetMeta(0)
	if err != nil || !ok || meta == nil {
		t.Fatalf("expected epoch 0 meta: ok=%v err=%v", ok, err)
	}
	if !meta.Shortfall {
		t.Fatalf("expected meta.Shortfall to be true")
	}
	if meta.TotalPaid == nil || meta.TotalPaid.Sign() != 0 {
		t.Fatalf("expected degraded TotalPaid of 0, got %v", meta.TotalPaid)
	}
	if meta.Winners != 0 {
		t.Fatalf("expected 0 recorded winners for a skipped payout, got %d", meta.Winners)
	}

	// No claim should have been recorded for the participant: a shortfall
	// epoch must not promise a reward it cannot pay.
	if _, ok, err := manager.PotsoRewardsGetClaim(0, participant); err != nil {
		t.Fatalf("claim lookup: %v", err)
	} else if ok {
		t.Fatalf("expected no claim to be recorded for a skipped, underfunded epoch")
	}

	// The treasury must be untouched -- nothing was actually paid out.
	reloaded, err := manager.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("reload treasury: %v", err)
	}
	if reloaded.BalanceZNHB.Cmp(big.NewInt(10)) != 0 {
		t.Fatalf("expected treasury balance to remain 10 after a skipped payout, got %s", reloaded.BalanceZNHB)
	}

	// Governance voting power must still have been recorded for this epoch
	// -- the weight snapshot is computed unconditionally (sub-issue 3),
	// independent of the shortfall.
	if _, ok, err := manager.SnapshotPotsoWeights(0); err != nil {
		t.Fatalf("snapshot potso weights: %v", err)
	} else if !ok {
		t.Fatalf("expected a governance-facing weight snapshot despite the shortfall")
	}
}

// TestProcessPotsoRewardEpoch_TreasuryShortfallEventSurfacesAndAdvances
// exercises processPotsoRewardEpoch directly (rather than through
// ProcessBlockLifecycle) and asserts the shortfall is observable via the
// dedicated potso.reward.shortfall event, in addition to the persisted
// meta flag covered above -- the "operator/governance needs to be able to
// see it happened" half of the fix.
func TestProcessPotsoRewardEpoch_TreasuryShortfallEventSurfacesAndAdvances(t *testing.T) {
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

	treasury := [20]byte{0x53}
	cfg := potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(500),
		TreasuryAddress:    treasury,
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
	}

	manager := nhbstate.NewManager(sp.Trie)
	treasuryAcc, err := manager.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("treasury account: %v", err)
	}
	treasuryAcc.BalanceZNHB = big.NewInt(50)
	if err := manager.PutAccount(treasury[:], treasuryAcc); err != nil {
		t.Fatalf("store treasury: %v", err)
	}

	participant := [20]byte{0x54}
	if err := manager.PotsoStakeSetBondedTotal(participant, big.NewInt(100)); err != nil {
		t.Fatalf("set stake: %v", err)
	}
	if err := manager.PotsoMetricsAddEngagement(0, participant, 0, 0, 30*60); err != nil {
		t.Fatalf("seed engagement: %v", err)
	}

	const epochNumber = uint64(0)
	if err := sp.processPotsoRewardEpoch(manager, cfg, epochNumber, "2026-01-01", time.Now().Unix()); err != nil {
		t.Fatalf("process shortfall epoch directly: %v", err)
	}
	// The caller is responsible for marking the epoch processed on success,
	// exactly as maybeProcessPotsoRewards does -- confirming this call
	// returns nil (not potso.ErrInsufficientTreasury) is what lets that
	// marker ever get written for a shortfall epoch.
	if err := manager.PotsoRewardsSetLastProcessedEpoch(epochNumber); err != nil {
		t.Fatalf("mark epoch processed: %v", err)
	}

	emitted := sp.Events()
	found := false
	for _, evt := range emitted {
		if evt.Type == potsoevents.TypePotsoRewardShortfall {
			found = true
			if evt.Attributes["epoch"] != "0" {
				t.Fatalf("expected shortfall event for epoch 0, got attributes: %+v", evt.Attributes)
			}
			if evt.Attributes["available"] != "50" {
				t.Fatalf("expected available=50 in shortfall event, got: %+v", evt.Attributes)
			}
		}
	}
	if !found {
		t.Fatalf("expected a %s event to be emitted for the underfunded epoch", potsoevents.TypePotsoRewardShortfall)
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

	// Epoch 1 (heights 3-4): identical participant/emission. Before the
	// treasury-shortfall fix (sub-issue 1), this epoch's budget would be
	// sized against the treasury's full, never-debited balance and silently
	// approved. The shared shortfall check (now reachable per sub-issue 1)
	// correctly detects that the treasury's genuinely remaining 400 cannot
	// cover another 600 -- but, per the chain-halt fix, that detection must
	// degrade this epoch's payout to zero and keep the block committing
	// rather than aborting ProcessBlockLifecycle.
	if err := manager.PotsoMetricsAddEngagement(1, participant, 0, 0, 30*60); err != nil {
		t.Fatalf("seed engagement epoch 1: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(3, base.Add(24*time.Hour-time.Second).Unix()); err != nil {
		t.Fatalf("process block 3: %v", err)
	}
	if err := sp.ProcessBlockLifecycle(4, base.Add(24*time.Hour).Unix()); err != nil {
		t.Fatalf("epoch 1 close with a treasury shortfall must not abort the block, got: %v", err)
	}

	lastProcessed, ok, err := manager.PotsoRewardsLastProcessedEpoch()
	if err != nil {
		t.Fatalf("last processed lookup: %v", err)
	} else if !ok || lastProcessed != 1 {
		t.Fatalf("expected epoch 1 to be marked processed despite the shortfall: ok=%v lastProcessed=%d", ok, lastProcessed)
	}

	meta1, ok, err := manager.PotsoRewardsGetMeta(1)
	if err != nil || !ok || meta1 == nil {
		t.Fatalf("expected epoch 1 meta: ok=%v err=%v", ok, err)
	}
	if !meta1.Shortfall {
		t.Fatalf("expected epoch 1 meta.Shortfall to be true")
	}
	if meta1.TotalPaid == nil || meta1.TotalPaid.Sign() != 0 {
		t.Fatalf("expected epoch 1 degraded TotalPaid of 0, got %v", meta1.TotalPaid)
	}

	// Epoch 1 must not have recorded a claim either -- a claim-mode shortfall
	// must not promise an obligation the treasury cannot cover.
	if _, ok, err := manager.PotsoRewardsGetClaim(1, participant); err != nil {
		t.Fatalf("epoch 1 claim lookup: %v", err)
	} else if ok {
		t.Fatalf("expected no claim recorded for epoch 1's skipped, underfunded payout")
	}

	// Epoch 0's reservation and claim record must be untouched by epoch 1's
	// shortfall, and epoch 1's own (correctly skipped) payout must not have
	// debited anything further from the treasury.
	stillReserved, err := manager.GetAccount(treasury[:])
	if err != nil {
		t.Fatalf("reload treasury after epoch 1: %v", err)
	}
	if stillReserved.BalanceZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("epoch 0's reservation should be unaffected by epoch 1's shortfall, got %s", stillReserved.BalanceZNHB)
	}

	claim0Again, ok, err := manager.PotsoRewardsGetClaim(0, participant)
	if err != nil || !ok || claim0Again == nil {
		t.Fatalf("expected epoch 0 claim record to remain: ok=%v err=%v", ok, err)
	}
	if claim0Again.Claimed {
		t.Fatalf("epoch 0 reward should still be unclaimed")
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
