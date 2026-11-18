package core

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/events"
	nhbstate "nhbchain/core/state"
	"nhbchain/native/potso"
	"nhbchain/storage"
	statetrie "nhbchain/storage/trie"
)

// PL-DC-29: processPotsoRewardEpoch caps the reward budget at the treasury balance
// before it computes the rewards, so the shortfall check that follows (totalPaid above
// the treasury) can never fire; an underfunded treasury quietly pays a smaller, capped
// epoch and nothing records that it happened. From the activation height the budget is
// the emission, a treasury that cannot cover what the epoch would pay is detected before
// anything is written, and the epoch degrades to a recorded zero payout (no error: an
// error here would abort every block, since this runs from the block lifecycle). The
// claim-mode half of PL-DC-29 is not touched: there is no claim path to reserve for.

const shortfallNow = 1_700_200_000

func shortfallRules(h uint64) ValidatorRules {
	return ValidatorRules{PotsoTreasuryShortfallActivationHeight: h}
}

type shortfallEnv struct {
	t           *testing.T
	sp          *StateProcessor
	manager     *nhbstate.Manager
	treasury    [20]byte
	participant [20]byte
}

// newShortfallEnv builds one participant with all the weight, an emission of 1000 per
// epoch of two blocks, and a treasury holding treasuryBalance.
func newShortfallEnv(t *testing.T, rules ValidatorRules, treasuryBalance int64) *shortfallEnv {
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
	e := &shortfallEnv{t: t, sp: sp, manager: nhbstate.NewManager(sp.Trie), treasury: [20]byte{0x51}, participant: [20]byte{0x52}}
	if err := sp.SetPotsoRewardConfig(potso.RewardConfig{
		EpochLengthBlocks:  2,
		AlphaStakeBps:      7000,
		MinPayoutWei:       big.NewInt(0),
		EmissionPerEpoch:   big.NewInt(1000),
		TreasuryAddress:    e.treasury,
		MaxWinnersPerEpoch: 10,
		CarryRemainder:     true,
	}); err != nil {
		t.Fatalf("set potso config: %v", err)
	}
	sp.SetValidatorRules(rules)
	acct, err := e.manager.GetAccount(e.treasury[:])
	if err != nil {
		t.Fatal(err)
	}
	acct.BalanceZNHB = big.NewInt(treasuryBalance)
	if err := e.manager.PutAccount(e.treasury[:], acct); err != nil {
		t.Fatal(err)
	}
	if err := e.manager.PotsoStakeSetBondedTotal(e.participant, big.NewInt(100)); err != nil {
		t.Fatal(err)
	}
	if err := e.manager.PotsoMetricsAddEngagement(0, e.participant, 0, 0, 30*60); err != nil {
		t.Fatal(err)
	}
	return e
}

// close runs blocks 1 and 2, the second closing epoch 0, at the given heights.
func (e *shortfallEnv) closeEpoch(h1, h2 uint64) {
	e.t.Helper()
	now := time.Unix(shortfallNow, 0).UTC()
	e.sp.BeginBlock(h1, now.Add(-time.Second))
	if err := e.sp.ProcessBlockLifecycle(1, now.Add(-time.Second).Unix()); err != nil {
		e.t.Fatalf("process block 1: %v", err)
	}
	e.sp.BeginBlock(h2, now)
	if err := e.sp.ProcessBlockLifecycle(2, now.Unix()); err != nil {
		e.t.Fatalf("epoch close must not abort the block: %v", err)
	}
}

func (e *shortfallEnv) meta() *potso.RewardEpochMeta {
	e.t.Helper()
	meta, ok, err := e.manager.PotsoRewardsGetMeta(0)
	if err != nil || !ok || meta == nil {
		e.t.Fatalf("no meta for epoch 0 (ok=%v err=%v)", ok, err)
	}
	return meta
}

func (e *shortfallEnv) treasuryBalance() *big.Int {
	acct, err := e.manager.GetAccount(e.treasury[:])
	if err != nil {
		e.t.Fatal(err)
	}
	return acct.BalanceZNHB
}

func (e *shortfallEnv) shortfallEvents() []map[string]string {
	var out []map[string]string
	for _, evt := range e.sp.Events() {
		if evt.Type == events.TypePotsoRewardShortfall {
			out = append(out, evt.Attributes)
		}
	}
	return out
}

// Off, an underfunded treasury pays a capped epoch and says nothing: today's behaviour.
func TestPotsoTreasuryShortfall_OffPaysACappedEpochSilently(t *testing.T) {
	for _, from := range []uint64{0, 1_000_000} {
		e := newShortfallEnv(t, shortfallRules(from), 10)
		e.closeEpoch(1, 2)
		meta := e.meta()
		if meta.Shortfall {
			t.Fatalf("height %d: shortfall flagged with the rule off", from)
		}
		if meta.TotalPaid.Cmp(big.NewInt(10)) != 0 {
			t.Fatalf("height %d: paid %s, want the capped 10", from, meta.TotalPaid)
		}
		if e.treasuryBalance().Sign() != 0 {
			t.Fatalf("height %d: treasury holds %s, want 0 after paying out its 10", from, e.treasuryBalance())
		}
		if len(e.shortfallEvents()) != 0 {
			t.Fatalf("height %d: a shortfall event was emitted with the rule off", from)
		}
	}
}

// On, the shortfall is detected, the block still succeeds, the epoch is spent, nothing is
// paid or promised, and the shortfall is recorded and announced.
func TestPotsoTreasuryShortfall_OnDegradesToARecordedZeroPayout(t *testing.T) {
	e := newShortfallEnv(t, shortfallRules(100), 10)
	e.closeEpoch(200, 201)

	last, ok, err := e.manager.PotsoRewardsLastProcessedEpoch()
	if err != nil || !ok || last != 0 {
		t.Fatalf("epoch 0 must be marked processed so it is never retried (ok=%v last=%d err=%v)", ok, last, err)
	}
	meta := e.meta()
	if !meta.Shortfall {
		t.Fatalf("meta.Shortfall is false")
	}
	if meta.TotalPaid.Sign() != 0 || meta.Winners != 0 {
		t.Fatalf("a skipped epoch paid %s to %d winners", meta.TotalPaid, meta.Winners)
	}
	if meta.Remainder == nil || meta.Remainder.Cmp(meta.Budget) != 0 {
		t.Fatalf("remainder %v, want the whole budget %v", meta.Remainder, meta.Budget)
	}
	if e.treasuryBalance().Cmp(big.NewInt(10)) != 0 {
		t.Fatalf("treasury holds %s, want its 10 untouched", e.treasuryBalance())
	}
	if _, ok, _ := e.manager.PotsoRewardsGetClaim(0, e.participant); ok {
		t.Fatalf("a claim was recorded for an epoch that paid nothing")
	}
	if _, ok, _ := e.manager.PotsoRewardsGetPayout(0, e.participant); ok {
		t.Fatalf("a payout was recorded for an epoch that paid nothing")
	}
	evts := e.shortfallEvents()
	if len(evts) != 1 {
		t.Fatalf("%d shortfall events, want 1", len(evts))
	}
	if evts[0]["available"] != "10" || evts[0]["required"] != "1000" || evts[0]["epoch"] != "0" {
		t.Fatalf("shortfall event %v, want epoch 0, required 1000, available 10", evts[0])
	}
	if _, ok, err := e.manager.SnapshotPotsoWeights(0); err != nil || !ok {
		t.Fatalf("the governance weight snapshot must still be stored (ok=%v err=%v)", ok, err)
	}
}

// A funded treasury is not touched by the rule: the epoch is the one the old code
// produces, byte for byte in the stored meta.
func TestPotsoTreasuryShortfall_FundedEpochIsUnchanged(t *testing.T) {
	off := newShortfallEnv(t, shortfallRules(0), 5_000)
	on := newShortfallEnv(t, shortfallRules(100), 5_000)
	off.closeEpoch(200, 201)
	on.closeEpoch(200, 201)
	if on.meta().Shortfall {
		t.Fatalf("a funded epoch was flagged")
	}
	a, err := rlp.EncodeToBytes(off.meta())
	if err != nil {
		t.Fatal(err)
	}
	b, err := rlp.EncodeToBytes(on.meta())
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatalf("the stored meta of a funded epoch differs with the rule on")
	}
	if off.treasuryBalance().Cmp(on.treasuryBalance()) != 0 || len(on.shortfallEvents()) != 0 {
		t.Fatalf("a funded epoch paid differently or announced a shortfall")
	}
	if off.sp.Trie.Hash() != on.sp.Trie.Hash() {
		t.Fatalf("a funded epoch left another state with the rule on")
	}
}

// The exact boundary: a treasury holding exactly what the epoch pays is enough; one wei
// less is a shortfall.
func TestPotsoTreasuryShortfall_Boundary(t *testing.T) {
	exact := newShortfallEnv(t, shortfallRules(100), 1_000)
	exact.closeEpoch(200, 201)
	if exact.meta().Shortfall || exact.meta().TotalPaid.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("a treasury of exactly the emission was treated as short (paid %s)", exact.meta().TotalPaid)
	}
	short := newShortfallEnv(t, shortfallRules(100), 999)
	short.closeEpoch(200, 201)
	if !short.meta().Shortfall {
		t.Fatalf("a treasury one wei short was not flagged")
	}
}

// The rule starts at its height.
func TestPotsoTreasuryShortfall_ActivationBoundary(t *testing.T) {
	before := newShortfallEnv(t, shortfallRules(300), 10)
	before.closeEpoch(298, 299)
	if before.meta().Shortfall {
		t.Fatalf("the rule applied before its height")
	}
	at := newShortfallEnv(t, shortfallRules(300), 10)
	at.closeEpoch(299, 300)
	if !at.meta().Shortfall {
		t.Fatalf("the rule did not apply at its height")
	}
}

// Two nodes that close the same underfunded epoch reach the same state.
func TestPotsoTreasuryShortfall_TwoNodesAgree(t *testing.T) {
	a := newShortfallEnv(t, shortfallRules(100), 10)
	b := newShortfallEnv(t, shortfallRules(100), 10)
	a.closeEpoch(200, 201)
	b.closeEpoch(200, 201)
	if a.sp.Trie.Hash() != b.sp.Trie.Hash() {
		t.Fatalf("two nodes disagree on an underfunded epoch")
	}
}

// RewardEpochMeta gained an optional trailing flag; a meta with the flag unset is
// stored exactly as before, so every epoch already on the chain decodes and re-encodes
// unchanged.
func TestRewardEpochMetaWithoutShortfallEncodesAsBefore(t *testing.T) {
	type oldMeta struct {
		Epoch           uint64
		Day             string
		StakeTotal      *big.Int
		EngagementTotal *big.Int
		AlphaBps        uint64
		Emission        *big.Int
		Budget          *big.Int
		TotalPaid       *big.Int
		Remainder       *big.Int
		Winners         uint64
	}
	old := oldMeta{Epoch: 7, Day: "2026-11-01", StakeTotal: big.NewInt(5), EngagementTotal: big.NewInt(6), AlphaBps: 7000,
		Emission: big.NewInt(1000), Budget: big.NewInt(900), TotalPaid: big.NewInt(800), Remainder: big.NewInt(100), Winners: 3}
	want, err := rlp.EncodeToBytes(old)
	if err != nil {
		t.Fatal(err)
	}
	cur := potso.RewardEpochMeta{Epoch: 7, Day: "2026-11-01", StakeTotal: big.NewInt(5), EngagementTotal: big.NewInt(6), AlphaBps: 7000,
		Emission: big.NewInt(1000), Budget: big.NewInt(900), TotalPaid: big.NewInt(800), Remainder: big.NewInt(100), Winners: 3}
	got, err := rlp.EncodeToBytes(&cur)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("a meta without the flag encodes differently from before")
	}
	var back potso.RewardEpochMeta
	if err := rlp.DecodeBytes(want, &back); err != nil {
		t.Fatalf("an old record no longer decodes: %v", err)
	}
	cur.Shortfall = true
	flagged, err := rlp.EncodeToBytes(&cur)
	if err != nil {
		t.Fatal(err)
	}
	var round potso.RewardEpochMeta
	if err := rlp.DecodeBytes(flagged, &round); err != nil || !round.Shortfall {
		t.Fatalf("the flag does not round-trip (%v, %+v)", err, round)
	}
	if clone := round.Clone(); !clone.Shortfall {
		t.Fatalf("Clone drops the flag")
	}
}
