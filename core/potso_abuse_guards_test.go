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

// PL-EXT-39 and PL-PO-01 (bug-bounty reports of 2026-11-19, 2026-11-18 and others): the POTSO
// engagement weight counts transactions, a transfer of one wei costs no fee, so an account with
// no stake that makes two transfers an epoch reaches the engagement cap and takes up to 30
// percent of the epoch's emission and of the governance weight. The guards against it exist and
// are all 0 on the live validators: MinStakeToEarnWei, MinStakeToWinWei, MinEngagementToWin,
// QuadraticTxDampenAfter (potso.weights and potso.abuse in config.toml) and MaxUserShareBps.
// They are node-local settings read at start, so switching one on at a restart would be a chain
// rule without a height (PL-DC-04). PotsoAbuseGuardsActivationHeight is that height: before it
// the five values are applied as 0 whatever the node's file says, from it they are applied as
// configured, so an operator can set the values on every validator ahead of the height and
// history stays replayable.

const potsoGuardNow = 1_700_300_000

type potsoGuardEnv struct {
	t        *testing.T
	sp       *StateProcessor
	manager  *nhbstate.Manager
	treasury [20]byte
	staker   [20]byte
	farmer   [20]byte
}

// newPotsoGuardEnv builds a staker (stake 1,000, two transactions in the epoch) and a farmer (no
// stake, two transactions), an emission of 1,000 per epoch of two blocks and a funded treasury.
func newPotsoGuardEnv(t *testing.T, rules ValidatorRules, weight potso.WeightParams, maxUserShareBps uint64) *potsoGuardEnv {
	t.Helper()
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
	e := &potsoGuardEnv{t: t, sp: sp, manager: nhbstate.NewManager(sp.Trie), treasury: [20]byte{0x61}, staker: [20]byte{0x62}, farmer: [20]byte{0x63}}
	if err := sp.SetPotsoRewardConfig(potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(1000),
		TreasuryAddress:    e.treasury,
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
		MaxUserShareBps:    maxUserShareBps,
	}); err != nil {
		t.Fatalf("set potso reward config: %v", err)
	}
	if err := sp.SetPotsoWeightConfig(weight); err != nil {
		t.Fatalf("set potso weight config: %v", err)
	}
	sp.SetValidatorRules(rules)
	acct, err := e.manager.GetAccount(e.treasury[:])
	if err != nil {
		t.Fatal(err)
	}
	acct.BalanceZNHB = big.NewInt(1_000_000)
	if err := e.manager.PutAccount(e.treasury[:], acct); err != nil {
		t.Fatal(err)
	}
	if err := e.manager.PotsoStakeSetBondedTotal(e.staker, big.NewInt(1000)); err != nil {
		t.Fatal(err)
	}
	if err := e.manager.PotsoMetricsAddEngagement(0, e.staker, 2, 0, 0); err != nil {
		t.Fatal(err)
	}
	if err := e.manager.PotsoMetricsAddEngagement(0, e.farmer, 2, 0, 0); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *potsoGuardEnv) closeEpoch(h1, h2 uint64) {
	e.t.Helper()
	now := time.Unix(potsoGuardNow, 0).UTC()
	e.sp.BeginBlock(h1, now.Add(-time.Second))
	if err := e.sp.ProcessBlockLifecycle(1, now.Add(-time.Second).Unix()); err != nil {
		e.t.Fatalf("process block 1: %v", err)
	}
	e.sp.BeginBlock(h2, now)
	if err := e.sp.ProcessBlockLifecycle(2, now.Unix()); err != nil {
		e.t.Fatalf("epoch close: %v", err)
	}
}

// paid is what epoch 0 gave an address, as a payout or as a claim, whichever mode is set.
func (e *potsoGuardEnv) paid(addr [20]byte) *big.Int {
	e.t.Helper()
	if amount, ok, err := e.manager.PotsoRewardsGetPayout(0, addr); err != nil {
		e.t.Fatal(err)
	} else if ok && amount != nil {
		return amount
	}
	if claim, ok, err := e.manager.PotsoRewardsGetClaim(0, addr); err != nil {
		e.t.Fatal(err)
	} else if ok && claim != nil && claim.Amount != nil {
		return claim.Amount
	}
	return big.NewInt(0)
}

func guardsRule(h uint64) ValidatorRules { return ValidatorRules{PotsoAbuseGuardsActivationHeight: h} }

// guardedWeights is the weight configuration an operator sets once the owner has chosen the
// values: engagement counts only for an account holding at least 500 staked.
func guardedWeights() potso.WeightParams {
	w := potso.DefaultWeightParams()
	w.MinStakeToEarnWei = big.NewInt(500)
	return w
}

// The values are configured, the rule is off: they are not applied, so an epoch closes exactly
// as it does with the values at 0 (what the validators run), the farmer is paid and the state is
// the same, byte for byte.
func TestPotsoAbuseGuards_OffIgnoresTheConfiguredValues(t *testing.T) {
	for _, from := range []uint64{0, 1_000_000} {
		configured := newPotsoGuardEnv(t, guardsRule(from), guardedWeights(), 4000)
		plain := newPotsoGuardEnv(t, guardsRule(from), potso.DefaultWeightParams(), 0)
		configured.closeEpoch(1, 2)
		plain.closeEpoch(1, 2)
		if configured.paid(configured.farmer).Sign() == 0 {
			t.Fatalf("height %d: the farmer was refused with the rule off", from)
		}
		if configured.sp.Trie.Hash() != plain.sp.Trie.Hash() {
			t.Fatalf("height %d: configured guard values changed the state with the rule off", from)
		}
	}
}

// On, the engagement of an account below the minimum stake does not count: the farmer earns
// nothing and the staker takes the whole epoch.
func TestPotsoAbuseGuards_OnRefusesEngagementWithoutStake(t *testing.T) {
	e := newPotsoGuardEnv(t, guardsRule(100), guardedWeights(), 0)
	e.closeEpoch(200, 201)
	if got := e.paid(e.farmer); got.Sign() != 0 {
		t.Fatalf("a zero-stake account was paid %s with the guard on", got)
	}
	if got := e.paid(e.staker); got.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("the staker was paid %s, want the whole epoch (1000)", got)
	}
}

// The same epoch with the rule on and the values still 0 is the epoch of today: the rule only
// lets configured values apply.
func TestPotsoAbuseGuards_OnWithZeroValuesChangesNothing(t *testing.T) {
	on := newPotsoGuardEnv(t, guardsRule(100), potso.DefaultWeightParams(), 0)
	off := newPotsoGuardEnv(t, guardsRule(0), potso.DefaultWeightParams(), 0)
	on.closeEpoch(200, 201)
	off.closeEpoch(200, 201)
	if on.sp.Trie.Hash() != off.sp.Trie.Hash() {
		t.Fatalf("turning the rule on with all values at 0 changed the state")
	}
}

// The rule starts at its height: one block earlier the farmer is paid, at it he is not.
func TestPotsoAbuseGuards_ActivationBoundary(t *testing.T) {
	before := newPotsoGuardEnv(t, guardsRule(300), guardedWeights(), 0)
	before.closeEpoch(298, 299)
	if before.paid(before.farmer).Sign() == 0 {
		t.Fatalf("the guard applied before its height")
	}
	at := newPotsoGuardEnv(t, guardsRule(300), guardedWeights(), 0)
	at.closeEpoch(299, 300)
	if at.paid(at.farmer).Sign() != 0 {
		t.Fatalf("the guard did not apply at its height")
	}
}

// Each of the five values is applied from the height and not before.
func TestPotsoAbuseGuards_EveryValueIsGated(t *testing.T) {
	cases := []struct {
		name   string
		weight func(*potso.WeightParams)
		share  uint64
		// farmerRefused: the farmer earns nothing with the guard on.
		farmerRefused bool
		// stakerCapped: the staker is held to the per-user share.
		stakerCapped bool
	}{
		{name: "MinStakeToEarnWei", weight: func(w *potso.WeightParams) { w.MinStakeToEarnWei = big.NewInt(500) }, farmerRefused: true},
		{name: "MinStakeToWinWei", weight: func(w *potso.WeightParams) { w.MinStakeToWinWei = big.NewInt(500) }, farmerRefused: true},
		{name: "MinEngagementToWin", weight: func(w *potso.WeightParams) { w.MinEngagementToWin = 1 << 40 }, farmerRefused: true},
		{name: "MaxUserShareBps", weight: func(*potso.WeightParams) {}, share: 2500, stakerCapped: true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			w := potso.DefaultWeightParams()
			tc.weight(&w)
			off := newPotsoGuardEnv(t, guardsRule(1000), w, tc.share)
			on := newPotsoGuardEnv(t, guardsRule(100), w, tc.share)
			off.closeEpoch(200, 201) // 201 is below 1000: the rule is off
			on.closeEpoch(200, 201)
			if off.paid(off.farmer).Sign() == 0 {
				t.Fatalf("%s applied before the height: the farmer was refused", tc.name)
			}
			if tc.farmerRefused && on.paid(on.farmer).Sign() != 0 {
				t.Fatalf("%s was not applied from the height: the farmer was paid %s", tc.name, on.paid(on.farmer))
			}
			if tc.stakerCapped {
				capWei := big.NewInt(250) // 25 percent of the 1,000 budget
				if on.paid(on.staker).Cmp(capWei) > 0 || on.paid(on.farmer).Cmp(capWei) > 0 {
					t.Fatalf("%s was not applied from the height: paid %s and %s, cap %s", tc.name, on.paid(on.staker), on.paid(on.farmer), capWei)
				}
				if off.paid(off.staker).Cmp(capWei) <= 0 && off.paid(off.farmer).Cmp(capWei) <= 0 {
					t.Fatalf("%s applied before the height", tc.name)
				}
			}
		})
	}
}

// QuadraticTxDampenAfter, applied: transactions past the threshold count by their root. With
// the per-epoch engagement cap off, a farmer with 100,000 transactions beside an honest user
// with 5,000 takes about 95 percent of the engagement share undampened and about 70 percent
// with a threshold of 100 and power 2.
func TestPotsoAbuseGuards_TxDampenIsGated(t *testing.T) {
	w := potso.DefaultWeightParams()
	w.MaxEngagementPerEpoch = 0
	w.QuadraticTxDampenAfter = 100
	build := func(from uint64) *potsoGuardEnv {
		e := newPotsoGuardEnv(t, guardsRule(from), w, 0)
		if err := e.manager.PotsoMetricsAddEngagement(0, e.farmer, 100_000, 0, 0); err != nil {
			t.Fatal(err)
		}
		if err := e.manager.PotsoMetricsAddEngagement(0, [20]byte{0x64}, 5_000, 0, 0); err != nil {
			t.Fatal(err)
		}
		return e
	}
	off := build(1000)
	on := build(100)
	off.closeEpoch(200, 201)
	on.closeEpoch(200, 201)
	if on.paid(on.farmer).Cmp(off.paid(off.farmer)) >= 0 {
		t.Fatalf("the dampening was not applied from the height: farmer paid %s on, %s off", on.paid(on.farmer), off.paid(off.farmer))
	}
}

// Two nodes with the same file and rule close the same epoch to the same state.
func TestPotsoAbuseGuards_TwoNodesAgree(t *testing.T) {
	a := newPotsoGuardEnv(t, guardsRule(100), guardedWeights(), 4000)
	b := newPotsoGuardEnv(t, guardsRule(100), guardedWeights(), 4000)
	a.closeEpoch(200, 201)
	b.closeEpoch(200, 201)
	if a.sp.Trie.Hash() != b.sp.Trie.Hash() {
		t.Fatalf("two nodes disagree on an epoch closed with the guards on")
	}
}

// withoutPotsoAbuseGuards must not touch the shape of the other weights.
func TestWithoutPotsoAbuseGuardsLeavesTheOtherSettingsAlone(t *testing.T) {
	in := potso.DefaultWeightParams()
	in.MinStakeToWinWei = big.NewInt(7)
	in.MinStakeToEarnWei = big.NewInt(8)
	in.MinEngagementToWin = 9
	in.QuadraticTxDampenAfter = 10
	in.QuadraticTxDampenPower = 3
	in.TxWeightBps = 1234
	in.DecayHalfLifeEpochs = 11
	out := withoutPotsoAbuseGuards(in)
	if out.MinStakeToWinWei.Sign() != 0 || out.MinStakeToEarnWei.Sign() != 0 || out.MinEngagementToWin != 0 || out.QuadraticTxDampenAfter != 0 {
		t.Fatalf("a guard value survived: %+v", out)
	}
	if out.QuadraticTxDampenPower != 3 || out.TxWeightBps != 1234 || out.DecayHalfLifeEpochs != 11 || out.AlphaStakeBps != in.AlphaStakeBps {
		t.Fatalf("another setting changed: %+v", out)
	}
	if in.MinStakeToWinWei.Cmp(big.NewInt(7)) != 0 || in.MinStakeToEarnWei.Cmp(big.NewInt(8)) != 0 {
		t.Fatalf("the input was modified")
	}
}
