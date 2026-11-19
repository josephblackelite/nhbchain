package core

import (
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
	"nhbchain/native/potso"
)

// PL-CLM-04: who may vote, and with how much power. Governance reads the stored
// POTSO weight snapshot of the last processed epoch. These tests build it through
// the real epoch processing, with a real validator registration and real
// delegations, and cast votes through the real governance engine.

var govWeightNow = time.Unix(1_700_600_000, 0).UTC()

type govWeightEnv struct {
	t        *testing.T
	sp       *StateProcessor
	manager  *nhbstate.Manager
	treasury [20]byte
	v1, v2   [20]byte // validators
	d1, d2   [20]byte // d1 delegates 3,000 to v1, d2 delegates 4,000 to v2
	height   uint64
}

// newGovWeightEnv builds two registered validators (2,000 of their own each) and
// two delegators, with epochs of two blocks and a reward treasury holding
// treasuryEpochs epochs of emission.
func newGovWeightEnv(t *testing.T, gate uint64, treasuryEpochs int64) *govWeightEnv {
	t.Helper()
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(1_000)); err != nil {
		t.Fatalf("minimum validator stake: %v", err)
	}
	cfg := potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(900),
		TreasuryAddress:    sp.potsoRewardConfig.TreasuryAddress,
		MaxWinnersPerEpoch: 50,
		CarryRemainder:     true,
	}
	if err := sp.SetPotsoRewardConfig(cfg); err != nil {
		t.Fatalf("potso reward config: %v", err)
	}
	treasuryAcc, err := manager.GetAccount(cfg.TreasuryAddress[:])
	if err != nil {
		t.Fatalf("treasury: %v", err)
	}
	treasuryAcc.BalanceZNHB = big.NewInt(900 * treasuryEpochs)
	if err := manager.PutAccount(cfg.TreasuryAddress[:], treasuryAcc); err != nil {
		t.Fatalf("fund treasury: %v", err)
	}
	env := &govWeightEnv{t: t, sp: sp, manager: manager, treasury: cfg.TreasuryAddress}
	env.v1[19], env.v2[19], env.d1[19], env.d2[19] = 0xA1, 0xA2, 0xB1, 0xB2
	for _, v := range [][20]byte{env.v1, env.v2} {
		writeAccount(t, sp, v, &types.Account{BalanceZNHB: big.NewInt(5_000)})
		if err := sp.applyStake(&types.Transaction{
			Value: big.NewInt(2_000),
			Data:  mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
		}, v[:]); err != nil {
			t.Fatalf("register validator: %v", err)
		}
	}
	writeAccount(t, sp, env.d1, &types.Account{BalanceZNHB: big.NewInt(10_000)})
	writeAccount(t, sp, env.d2, &types.Account{BalanceZNHB: big.NewInt(10_000)})
	if _, err := sp.StakeDelegate(env.d1[:], env.v1[:], big.NewInt(3_000)); err != nil {
		t.Fatalf("delegate d1: %v", err)
	}
	if _, err := sp.StakeDelegate(env.d2[:], env.v2[:], big.NewInt(4_000)); err != nil {
		t.Fatalf("delegate d2: %v", err)
	}
	sp.nowFunc = func() time.Time { return govWeightNow }
	sp.SetPotsoGovernanceWeights(gate)
	return env
}

// advance processes blocks up to and including height.
func (e *govWeightEnv) advance(to uint64) {
	e.t.Helper()
	for e.height < to {
		e.height++
		ts := govWeightNow.Add(time.Duration(e.height) * time.Second)
		e.sp.BeginBlock(e.height, ts)
		if err := e.sp.ProcessBlockLifecycle(e.height, ts.Unix()); err != nil {
			e.t.Fatalf("process block %d: %v", e.height, err)
		}
	}
}

func (e *govWeightEnv) govSnapshot(epoch uint64) map[[20]byte]potso.StoredWeightEntry {
	e.t.Helper()
	snap, ok, err := e.manager.SnapshotPotsoWeights(epoch)
	if err != nil {
		e.t.Fatalf("governance snapshot: %v", err)
	}
	if !ok || snap == nil {
		e.t.Fatalf("no governance snapshot for epoch %d", epoch)
	}
	out := make(map[[20]byte]potso.StoredWeightEntry, len(snap.Entries))
	for _, entry := range snap.Entries {
		out[entry.Address] = entry
	}
	return out
}

func stakeOf(t *testing.T, snap map[[20]byte]potso.StoredWeightEntry, addr [20]byte) *big.Int {
	t.Helper()
	if entry, ok := snap[addr]; ok && entry.Stake != nil {
		return entry.Stake
	}
	return big.NewInt(0)
}

func totalStake(snap map[[20]byte]potso.StoredWeightEntry) *big.Int {
	total := big.NewInt(0)
	for _, entry := range snap {
		if entry.Stake != nil {
			total.Add(total, entry.Stake)
		}
	}
	return total
}

// Off, governance reads the rewards' snapshot as it always did: the validators
// hold all of it, counting what was delegated to them, and a delegator holds none.
func TestPotsoGovernanceWeights_OffIsTheRewardsSnapshot(t *testing.T) {
	for _, gate := range []uint64{0, 1_000_000} { // zero, and a height this run never reaches
		env := newGovWeightEnv(t, gate, 10)
		env.advance(2)
		snap := env.govSnapshot(0)
		if got := stakeOf(t, snap, env.v1); got.Cmp(big.NewInt(5_000)) != 0 {
			t.Fatalf("gate %d: v1 counted stake = %s, want 2,000 own + 3,000 delegated", gate, got)
		}
		if got := stakeOf(t, snap, env.v2); got.Cmp(big.NewInt(6_000)) != 0 {
			t.Fatalf("gate %d: v2 counted stake = %s, want 2,000 own + 4,000 delegated", gate, got)
		}
		if _, ok := snap[env.d1]; ok {
			t.Fatalf("gate %d: a delegator holds governance weight", gate)
		}
		rewards, ok, err := env.manager.PotsoMetricsGetSnapshot(0)
		if err != nil || !ok {
			t.Fatalf("rewards snapshot: ok=%v err=%v", ok, err)
		}
		if len(rewards.Entries) != len(snap) {
			t.Fatalf("gate %d: governance snapshot has %d entries, the rewards' has %d", gate, len(snap), len(rewards.Entries))
		}
		for _, entry := range rewards.Entries {
			if snap[entry.Address].WeightBps != entry.WeightBps || snap[entry.Address].Engagement != entry.Engagement {
				t.Fatalf("gate %d: governance snapshot differs from the rewards' for %x", gate, entry.Address)
			}
		}
	}
}

// On, stake follows ownership: the delegators hold what they delegated, the
// validators what they own, and the total counted stake is the same.
func TestPotsoGovernanceWeights_DelegatedStakeIsCountedForTheDelegator(t *testing.T) {
	off := newGovWeightEnv(t, 0, 10)
	off.advance(2)
	offSnap := off.govSnapshot(0)

	on := newGovWeightEnv(t, 1, 10)
	on.advance(2)
	snap := on.govSnapshot(0)

	for name, want := range map[string]struct {
		addr  [20]byte
		stake int64
	}{
		"v1 keeps its own":   {on.v1, 2_000},
		"v2 keeps its own":   {on.v2, 2_000},
		"d1 holds its 3,000": {on.d1, 3_000},
		"d2 holds its 4,000": {on.d2, 4_000},
	} {
		if got := stakeOf(t, snap, want.addr); got.Cmp(big.NewInt(want.stake)) != 0 {
			t.Fatalf("%s: counted stake = %s, want %d", name, got, want.stake)
		}
	}
	if a, b := totalStake(offSnap), totalStake(snap); a.Cmp(b) != 0 {
		t.Fatalf("total counted stake changed: %s before, %s after (delegated stake counted twice or dropped)", a, b)
	}
	// The weights are shares of that total: 70 percent of the stake share.
	if w := snap[on.d2].WeightBps; w != 7000*4_000/11_000 {
		t.Fatalf("d2 weight = %d bps, want %d", w, 7000*4_000/11_000)
	}
}

// Rewards are paid from the rewards' snapshot, which the setting does not touch.
func TestPotsoGovernanceWeights_RewardsAreUnchanged(t *testing.T) {
	payouts := func(env *govWeightEnv) map[string]string {
		env.advance(2)
		out := map[string]string{}
		for _, addr := range [][20]byte{env.v1, env.v2, env.d1, env.d2} {
			amount, _, err := env.manager.PotsoRewardsGetPayout(0, addr)
			if err != nil {
				env.t.Fatalf("payout: %v", err)
			}
			out[fmt.Sprintf("%x", addr[:3])] = amount.String()
		}
		meta, _, err := env.manager.PotsoRewardsGetMeta(0)
		if err != nil || meta == nil {
			env.t.Fatalf("meta: %v", err)
		}
		out["total"] = meta.TotalPaid.String()
		return out
	}
	off := payouts(newGovWeightEnv(t, 0, 10))
	on := payouts(newGovWeightEnv(t, 1, 10))
	for k, v := range off {
		if on[k] != v {
			t.Fatalf("payout %s differs: %s with the setting off, %s on", k, v, on[k])
		}
	}
	if off["total"] == "0" {
		t.Fatalf("test setup: nothing was paid")
	}
}

func castVote(t *testing.T, env *govWeightEnv, voter [20]byte) (uint64, error) {
	t.Helper()
	env.sp.SetGovernancePolicy(governance.ProposalPolicy{
		MinDepositWei:       big.NewInt(0),
		VotingPeriodSeconds: 1000,
		TimelockSeconds:     1000,
		QuorumBps:           1,
		PassThresholdBps:    5000,
	})
	id, err := env.manager.GovernanceNextProposalID()
	if err != nil {
		t.Fatalf("next proposal id: %v", err)
	}
	now := govWeightNow
	proposal := &governance.Proposal{
		ID:          id,
		Submitter:   crypto.MustNewAddress(crypto.NHBPrefix, env.v1[:]),
		Status:      governance.ProposalStatusVotingPeriod,
		Deposit:     big.NewInt(0),
		SubmitTime:  now.Add(-time.Hour),
		VotingStart: now.Add(-time.Hour),
		VotingEnd:   now.Add(time.Hour),
		TimelockEnd: now.Add(2 * time.Hour),
		Target:      governance.ProposalKindParamUpdate,
	}
	if err := env.manager.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}
	return id, env.sp.governanceEngine().CastVote(id, voter, "yes")
}

// A delegator cannot vote today and can from the height; a validator still can.
func TestPotsoGovernanceWeights_ADelegatorCanVote(t *testing.T) {
	off := newGovWeightEnv(t, 0, 10)
	off.advance(2)
	_, err := castVote(t, off, off.d2)
	if err == nil || !strings.Contains(err.Error(), "zero voting power") {
		t.Fatalf("a delegator voted with the setting off or failed otherwise: %v", err)
	}

	on := newGovWeightEnv(t, 1, 10)
	on.advance(2)
	delegatorProposal, err := castVote(t, on, on.d2)
	if err != nil {
		t.Fatalf("a delegator could not vote with the setting on: %v", err)
	}
	if _, err := castVote(t, on, on.v2); err != nil {
		t.Fatalf("a validator could not vote with the setting on: %v", err)
	}
	vote, ok, err := on.manager.GovernanceGetVote(delegatorProposal, on.d2[:])
	if err != nil || !ok || vote == nil || vote.PowerBps == 0 {
		t.Fatalf("the delegator's ballot was not recorded with power: ok=%v err=%v vote=%v", ok, err, vote)
	}
}

// With an empty reward treasury no snapshot is stored today and every vote fails;
// from the height the governance snapshot is stored anyway.
func TestPotsoGovernanceWeights_ASnapshotExistsWithoutARewardBudget(t *testing.T) {
	off := newGovWeightEnv(t, 0, 0)
	off.advance(2)
	if _, ok, err := off.manager.SnapshotPotsoWeights(0); err != nil || ok {
		t.Fatalf("with no reward budget and the setting off a snapshot was stored (ok=%v err=%v)", ok, err)
	}
	on := newGovWeightEnv(t, 1, 0)
	on.advance(2)
	snap := on.govSnapshot(0)
	if len(snap) == 0 {
		t.Fatalf("the governance snapshot is empty")
	}
	if _, err := castVote(t, on, on.d1); err != nil {
		t.Fatalf("vote with no reward budget: %v", err)
	}
}

// Engagement is not changed by the setting. Characterisation of what an engaged
// account with no stake has today: weight while its engagement lasts, which is
// a few dozen epochs, and the same with the setting on or off (PL-CLM-04 reports
// why a longer half-life was not built).
func TestPotsoGovernanceWeights_EngagementIsUnchanged(t *testing.T) {
	var engaged [20]byte
	engaged[19] = 0xE1
	engagementAfter := func(gate uint64, epochs int) (uint64, bool) {
		env := newGovWeightEnv(t, gate, 100)
		if err := env.manager.PotsoMetricsAddEngagement(0, engaged, 5, 0, 0); err != nil {
			t.Fatalf("seed engagement: %v", err)
		}
		env.advance(uint64(2 * epochs))
		last, _, err := env.manager.PotsoRewardsLastProcessedEpoch()
		if err != nil {
			t.Fatalf("last epoch: %v", err)
		}
		entry, ok := env.govSnapshot(last)[engaged]
		return entry.Engagement, ok
	}
	// Right after the epoch it was active in it holds weight, on and off.
	for _, gate := range []uint64{0, 1} {
		engagement, ok := engagementAfter(gate, 2)
		if !ok || engagement == 0 {
			t.Fatalf("gate %d: an account active in the last epoch has no engagement weight (%d, in snapshot %v)", gate, engagement, ok)
		}
	}
	off, offOK := engagementAfter(0, 40)
	on, onOK := engagementAfter(1, 40)
	if off != on || offOK != onOK {
		t.Fatalf("the setting changed engagement: %d/%v off, %d/%v on", off, offOK, on, onOK)
	}
	if offOK && off > 100 {
		t.Fatalf("after forty epochs the engagement is still %d: the seven-epoch average is no longer what the report says", off)
	}
}

// Epoch 0 is processed in the block at height 2: a setting of 2 is in force there,
// a setting of 3 is not.
func TestPotsoGovernanceWeights_StartsAtTheActivationHeight(t *testing.T) {
	at := newGovWeightEnv(t, 2, 10)
	at.advance(2)
	if _, ok := at.govSnapshot(0)[at.d1]; !ok {
		t.Fatalf("the setting was not in force in the block at its activation height")
	}
	after := newGovWeightEnv(t, 3, 10)
	after.advance(2)
	if _, ok := after.govSnapshot(0)[after.d1]; ok {
		t.Fatalf("the setting was in force one block before its activation height")
	}
}

// When two delegators together lock more than their validator holds, the one with
// the lower address is served first, whatever order the delegations were made in.
func TestPotsoGovernanceWeights_ContendingDelegatorsAreServedInAddressOrder(t *testing.T) {
	env := newGovWeightEnv(t, 1, 10)
	var low [20]byte
	low[19] = 0xB0 // below d1 (0xB1), but delegates after it
	writeAccount(t, env.sp, low, &types.Account{BalanceZNHB: big.NewInt(10_000)})
	if _, err := env.sp.StakeDelegate(low[:], env.v1[:], big.NewInt(4_000)); err != nil {
		t.Fatalf("delegate: %v", err)
	}
	// v1 is shown as holding 5,000 while 3,000 + 4,000 are locked to it.
	entries := []potso.RewardSnapshotEntry{{Address: env.v1, Stake: big.NewInt(5_000)}}
	snap, err := env.sp.governanceWeightSnapshot(env.manager, 1, entries, env.sp.potsoWeightConfig)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	got := map[[20]byte]*big.Int{}
	for _, e := range snap.Entries {
		got[e.Address] = e.Stake
	}
	if got[low] == nil || got[low].Cmp(big.NewInt(4_000)) != 0 {
		t.Fatalf("the lower address holds %v, want its full 4,000", got[low])
	}
	if got[env.d1] == nil || got[env.d1].Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("the higher address holds %v, want the 1,000 that was left", got[env.d1])
	}
}

// The delegator index of a validator can name an account whose delegation has
// since moved elsewhere: that account's stake is not taken from this validator.
func TestPotsoGovernanceWeights_AStaleIndexEntryMovesNothing(t *testing.T) {
	env := newGovWeightEnv(t, 1, 10)
	acc, err := env.manager.GetAccount(env.d2[:]) // listed under v2, 4,000 locked
	if err != nil {
		t.Fatalf("load d2: %v", err)
	}
	acc.DelegatedValidator = append([]byte(nil), env.v1[:]...) // now says v1
	if err := env.manager.PutAccount(env.d2[:], acc); err != nil {
		t.Fatalf("store d2: %v", err)
	}
	entries := []potso.RewardSnapshotEntry{
		{Address: env.v1, Stake: big.NewInt(5_000)},
		{Address: env.v2, Stake: big.NewInt(6_000)},
	}
	snap, err := env.sp.governanceWeightSnapshot(env.manager, 1, entries, env.sp.potsoWeightConfig)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	got := map[[20]byte]*big.Int{}
	for _, e := range snap.Entries {
		got[e.Address] = e.Stake
	}
	if v := got[env.v2]; v == nil || v.Cmp(big.NewInt(6_000)) != 0 {
		t.Fatalf("v2 holds %v, want its 6,000 untouched by a stale index entry", v)
	}
	if _, ok := got[env.d2]; ok {
		t.Fatalf("an account whose delegation points elsewhere gained weight through a stale index entry")
	}
}

// A validator entry with no counted stake is not an eligible validator.
func TestPotsoGovernanceWeights_AZeroBasisValidatorIsNotEligible(t *testing.T) {
	env := newGovWeightEnv(t, 1, 10)
	env.sp.EligibleValidators[string(env.v1[:])] = big.NewInt(0)
	entries := []potso.RewardSnapshotEntry{{Address: env.v1, Stake: big.NewInt(5_000)}}
	snap, err := env.sp.governanceWeightSnapshot(env.manager, 1, entries, env.sp.potsoWeightConfig)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for _, e := range snap.Entries {
		if e.Address == env.d1 {
			t.Fatalf("a delegator of a validator with no counted stake gained weight")
		}
		if e.Address == env.v1 && e.Stake.Cmp(big.NewInt(5_000)) != 0 {
			t.Fatalf("a validator with no counted stake had stake moved away: %s", e.Stake)
		}
	}
}

// The same inputs always give the same snapshot, whatever order maps iterate in.
func TestPotsoGovernanceWeights_IsDeterministic(t *testing.T) {
	env := newGovWeightEnv(t, 1, 10)
	env.advance(2)
	entries := []potso.RewardSnapshotEntry{
		{Address: env.v1, Stake: big.NewInt(5_000)},
		{Address: env.v2, Stake: big.NewInt(6_000)},
	}
	first, err := env.sp.governanceWeightSnapshot(env.manager, 1, entries, env.sp.potsoWeightConfig)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for i := 0; i < 25; i++ {
		again, err := env.sp.governanceWeightSnapshot(env.manager, 1, entries, env.sp.potsoWeightConfig)
		if err != nil {
			t.Fatalf("snapshot: %v", err)
		}
		if fmt.Sprint(again.Entries) != fmt.Sprint(first.Entries) {
			t.Fatalf("run %d differs:\n%v\n%v", i, again.Entries, first.Entries)
		}
	}
}

// A delegator's locked amount larger than what the validator holds moves only what
// the validator holds, so the total never grows.
func TestPotsoGovernanceWeights_NeverMovesMoreThanTheValidatorHolds(t *testing.T) {
	env := newGovWeightEnv(t, 1, 10)
	acc, err := env.manager.GetAccount(env.d1[:])
	if err != nil {
		t.Fatalf("load d1: %v", err)
	}
	acc.LockedZNHB = big.NewInt(99_999)
	if err := env.manager.PutAccount(env.d1[:], acc); err != nil {
		t.Fatalf("store d1: %v", err)
	}
	entries := []potso.RewardSnapshotEntry{
		{Address: env.v1, Stake: big.NewInt(5_000)},
		{Address: env.v2, Stake: big.NewInt(6_000)},
	}
	snap, err := env.sp.governanceWeightSnapshot(env.manager, 1, entries, env.sp.potsoWeightConfig)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	total := big.NewInt(0)
	byAddr := map[[20]byte]*big.Int{}
	for _, e := range snap.Entries {
		total.Add(total, e.Stake)
		byAddr[e.Address] = e.Stake
	}
	if total.Cmp(big.NewInt(11_000)) > 0 {
		t.Fatalf("total counted stake grew to %s", total)
	}
	if got := byAddr[env.d1]; got == nil || got.Cmp(big.NewInt(5_000)) != 0 {
		t.Fatalf("d1 holds %v, want the 5,000 the validator held", got)
	}
}

// A delegation to an address that is not an eligible validator moves nothing, and
// a delegator that is itself an eligible validator is left as it was.
func TestPotsoGovernanceWeights_OnlyEligibleValidatorsDelegationsMove(t *testing.T) {
	env := newGovWeightEnv(t, 1, 10)
	delete(env.sp.EligibleValidators, string(env.v1[:])) // v1 no longer eligible
	entries := []potso.RewardSnapshotEntry{
		{Address: env.v1, Stake: big.NewInt(5_000)},
		{Address: env.v2, Stake: big.NewInt(6_000)},
	}
	snap, err := env.sp.governanceWeightSnapshot(env.manager, 1, entries, env.sp.potsoWeightConfig)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	byAddr := map[[20]byte]*big.Int{}
	for _, e := range snap.Entries {
		byAddr[e.Address] = e.Stake
	}
	if got := byAddr[env.v1]; got == nil || got.Cmp(big.NewInt(5_000)) != 0 {
		t.Fatalf("an ineligible validator's counted stake changed: %v", got)
	}
	if _, ok := byAddr[env.d1]; ok {
		t.Fatalf("a delegator of an ineligible validator gained weight")
	}
	if got := byAddr[env.d2]; got == nil || got.Cmp(big.NewInt(4_000)) != 0 {
		t.Fatalf("a delegator of an eligible validator holds %v, want 4,000", got)
	}

	// A delegator that is itself eligible keeps its entry, and the validator keeps its.
	env.sp.EligibleValidators[string(env.v1[:])] = big.NewInt(5_000)
	env.sp.EligibleValidators[string(env.d1[:])] = big.NewInt(3_000)
	entries = append(entries, potso.RewardSnapshotEntry{Address: env.d1, Stake: big.NewInt(3_000)})
	snap, err = env.sp.governanceWeightSnapshot(env.manager, 1, entries, env.sp.potsoWeightConfig)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	byAddr = map[[20]byte]*big.Int{}
	for _, e := range snap.Entries {
		byAddr[e.Address] = e.Stake
	}
	if got := byAddr[env.d1]; got == nil || got.Cmp(big.NewInt(3_000)) != 0 {
		t.Fatalf("an eligible delegator's stake changed: %v", got)
	}
	if got := byAddr[env.v1]; got == nil || got.Cmp(big.NewInt(5_000)) != 0 {
		t.Fatalf("the validator an eligible delegator delegated to had stake moved away: %v", got)
	}
}

func TestPotsoGovernanceWeights_TheSettingSurvivesCopy(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetPotsoGovernanceWeights(11)
	copied, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if copied.potsoGovernanceWeightFromHeight != 11 {
		t.Fatalf("Copy lost the setting: %d", copied.potsoGovernanceWeightFromHeight)
	}
}

func TestNodePotsoGovernanceWeights_SetterReachesTheProcessorAndSurvivesARebuild(t *testing.T) {
	node := newTestNode(t)
	node.SetPotsoGovernanceWeights(5)
	node.stateMu.Lock()
	gotH := node.state.potsoGovernanceWeightFromHeight
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH := node.state.potsoGovernanceWeightFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	if gotH != 5 {
		t.Fatalf("setter left the processor at %d, want 5", gotH)
	}
	if rebuiltH != 5 {
		t.Fatalf("a rebuilt processor lost the setting: %d, want 5", rebuiltH)
	}
}
