package core

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/events"
	"nhbchain/core/rewards"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/native/governance"
)

// PL-R5T-27: the reward rate used when a stake, unstake or claim executes is set once
// at process start from the node's own config.toml (overridden by the on-chain
// staking.aprBps as it stood at that moment) and nothing refreshes it when a governance
// parameter changes. It is written into the state trie with the reward index, so two
// validators with different config, or one restarted after a governed change while the
// other keeps running, compute different state for the same transaction. These tests
// drive the real index and the real governance execution.

var aprT0 = time.Unix(1_800_000_000, 0).UTC()

const day = 24 * time.Hour

// referenceIndex replays the same updates on a standalone engine: (time, rate) pairs.
func referenceIndex(steps ...struct {
	at  time.Time
	apr uint64
}) *big.Int {
	e := rewards.NewEngine()
	for _, s := range steps {
		e.UpdateGlobalIndex(s.at, s.apr)
	}
	return e.Index()
}

type aprStep = struct {
	at  time.Time
	apr uint64
}

func newAPRProcessor(t *testing.T, rules ValidatorRules, cached uint64) *StateProcessor {
	t.Helper()
	sp := newStakingStateProcessor(t)
	sp.SetValidatorRules(rules)
	sp.stakeRewardAPR = cached // what SyncStakingParams sets from the node's own config
	return sp
}

func aprRule(h uint64) ValidatorRules { return ValidatorRules{StakeAPRFromStateActivationHeight: h} }

func setAPRParam(t *testing.T, sp *StateProcessor, raw string) {
	t.Helper()
	if err := nhbstate.NewManager(sp.Trie).ParamStoreSet(governance.ParamKeyStakingAprBps, []byte(raw)); err != nil {
		t.Fatalf("set staking.aprBps: %v", err)
	}
}

// advanceAt runs the index update a staking transaction runs, in a block at height and time.
func advanceAt(t *testing.T, sp *StateProcessor, height uint64, at time.Time) *big.Int {
	t.Helper()
	sp.BeginBlock(height, at)
	index, err := sp.advanceStakeRewards()
	if err != nil {
		t.Fatalf("advance at %d: %v", height, err)
	}
	return index
}

func persistedAPR(t *testing.T, sp *StateProcessor) uint64 {
	t.Helper()
	data, err := sp.Trie.Get(stakeRewardStateKey)
	if err != nil || len(data) == 0 {
		t.Fatalf("no persisted reward state (%v)", err)
	}
	var rec stakeRewardRecord
	if err := rlp.DecodeBytes(data, &rec); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return rec.APRBps
}

// Off (the shipped and live setting, or a height this run never reaches), the rate is
// the one cached at start-up, whatever the chain says. Pinned.
func TestStakeAPRFromState_OffUsesTheCachedRate(t *testing.T) {
	for _, from := range []uint64{0, 1_000_000} {
		sp := newAPRProcessor(t, aprRule(from), 1250)
		setAPRParam(t, sp, "500")
		advanceAt(t, sp, 200, aprT0)
		got := advanceAt(t, sp, 201, aprT0.Add(30*day))
		want := referenceIndex(aprStep{aprT0, 1250}, aprStep{aprT0.Add(30 * day), 1250})
		if got.Cmp(want) != 0 {
			t.Fatalf("height %d: index %s, want the cached-rate index %s", from, got, want)
		}
	}
}

// On, the rate is the on-chain parameter, in every spelling governance stores.
func TestStakeAPRFromState_OnReadsTheParameter(t *testing.T) {
	for _, spelling := range numberSpellings("500") {
		sp := newAPRProcessor(t, aprRule(100), 3000)
		setAPRParam(t, sp, spelling)
		advanceAt(t, sp, 200, aprT0)
		got := advanceAt(t, sp, 201, aprT0.Add(30*day))
		want := referenceIndex(aprStep{aprT0, 500}, aprStep{aprT0.Add(30 * day), 500})
		if got.Cmp(want) != 0 {
			t.Fatalf("spelling %q: index %s, want the 500 bps index %s", spelling, got, want)
		}
	}
}

// With no parameter on the chain the rate is the fixed default, never local config.
func TestStakeAPRFromState_NoParameterMeansTheFixedDefault(t *testing.T) {
	sp := newAPRProcessor(t, aprRule(100), 3000)
	advanceAt(t, sp, 200, aprT0)
	got := advanceAt(t, sp, 201, aprT0.Add(30*day))
	want := referenceIndex(aprStep{aprT0, 1250}, aprStep{aprT0.Add(30 * day), 1250})
	if got.Cmp(want) != 0 {
		t.Fatalf("index %s, want the default 1250 bps index %s", got, want)
	}
}

// A stored value that cannot be read gives the same fixed default on every node.
func TestStakeAPRFromState_UnreadableParameterMeansTheFixedDefault(t *testing.T) {
	for _, raw := range []string{"not-a-number", `"12.5"`, "-5", "99999999999999999999999999"} {
		sp := newAPRProcessor(t, aprRule(100), 3000)
		setAPRParam(t, sp, raw)
		advanceAt(t, sp, 200, aprT0)
		got := advanceAt(t, sp, 201, aprT0.Add(30*day))
		want := referenceIndex(aprStep{aprT0, 1250}, aprStep{aprT0.Add(30 * day), 1250})
		if got.Cmp(want) != 0 {
			t.Fatalf("param %q: index %s, want the default 1250 bps index %s", raw, got, want)
		}
	}
}

// The core of the finding: two validators whose config gives different cached rates
// (one restarted, one not; one with another config.toml) reach different state for the
// same transaction. Off, they differ; on, they agree.
func TestStakeAPRFromState_ConfigSkewBetweenNodes(t *testing.T) {
	run := func(rules ValidatorRules, cached uint64) (*StateProcessor, *big.Int) {
		sp := newAPRProcessor(t, rules, cached)
		setAPRParam(t, sp, "800")
		advanceAt(t, sp, 200, aprT0)
		idx := advanceAt(t, sp, 201, aprT0.Add(45*day))
		return sp, idx
	}
	offA, idxA := run(aprRule(0), 1250)
	offB, idxB := run(aprRule(0), 3000)
	if idxA.Cmp(idxB) == 0 || offA.Trie.Hash() == offB.Trie.Hash() {
		t.Fatalf("setup: with the setting off the two nodes should have diverged (they did not)")
	}
	onA, idxA := run(aprRule(100), 1250)
	onB, idxB := run(aprRule(100), 3000)
	if idxA.Cmp(idxB) != 0 {
		t.Fatalf("on: indexes differ: %s vs %s", idxA, idxB)
	}
	if onA.Trie.Hash() != onB.Trie.Hash() {
		t.Fatalf("on: state roots differ across nodes with different config")
	}
}

// A node restarted in the middle of the history (its in-memory rate is replaced by the
// start-up value) ends in the same state as one that was not.
func TestStakeAPRFromState_RestartedNodeAgrees(t *testing.T) {
	history := func(sp *StateProcessor, from, to int) {
		for i := from; i < to; i++ {
			advanceAt(t, sp, uint64(200+i), aprT0.Add(time.Duration(i)*10*day))
		}
	}
	steady := newAPRProcessor(t, aprRule(100), 1250)
	setAPRParam(t, steady, "900")
	history(steady, 0, 6)

	restarted := newAPRProcessor(t, aprRule(100), 1250)
	setAPRParam(t, restarted, "900")
	history(restarted, 0, 3)
	// The process restarts: a new processor over the same trie, and start-up sets the
	// cached rate from this node's own config.
	again, err := NewStateProcessor(restarted.Trie)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	again.SetValidatorRules(aprRule(100))
	again.stakeRewardAPR = 4242
	history(again, 3, 6)

	if steady.Trie.Hash() != again.Trie.Hash() {
		t.Fatalf("a restarted node ended in another state than one that kept running")
	}
}

// The activation boundary: one block before the height the cached rate is used, at the
// height the chain's.
func TestStakeAPRFromState_ActivationBoundary(t *testing.T) {
	before := newAPRProcessor(t, aprRule(200), 1250)
	setAPRParam(t, before, "500")
	advanceAt(t, before, 150, aprT0)
	got := advanceAt(t, before, 199, aprT0.Add(30*day))
	if want := referenceIndex(aprStep{aprT0, 1250}, aprStep{aprT0.Add(30 * day), 1250}); got.Cmp(want) != 0 {
		t.Fatalf("height 199: index %s, want the cached-rate index %s", got, want)
	}

	at := newAPRProcessor(t, aprRule(200), 1250)
	setAPRParam(t, at, "500")
	advanceAt(t, at, 150, aprT0)
	got = advanceAt(t, at, 200, aprT0.Add(30*day))
	// The interval before the height was settled at the rate that was in force (1250),
	// then the rest at the chain's rate (500): all of it is in this one step, because
	// nothing was touched in between, so the settlement is at the old rate.
	if want := referenceIndex(aprStep{aprT0, 1250}, aprStep{aprT0.Add(30 * day), 1250}); got.Cmp(want) != 0 {
		t.Fatalf("height 200: the interval before the switch must settle at the old rate: index %s, want %s", got, want)
	}
	next := advanceAt(t, at, 201, aprT0.Add(60*day))
	want := referenceIndex(aprStep{aprT0, 1250}, aprStep{aprT0.Add(30 * day), 1250}, aprStep{aprT0.Add(60 * day), 500})
	if next.Cmp(want) != 0 {
		t.Fatalf("after the switch: index %s, want %s (500 bps from the switch on)", next, want)
	}
}

// The persisted record carries the rate in force, so a node that reads the state sees
// the chain's rate, not the one it was started with.
func TestStakeAPRFromState_PersistedRecordHoldsTheChainsRate(t *testing.T) {
	sp := newAPRProcessor(t, aprRule(100), 3000)
	setAPRParam(t, sp, "700")
	advanceAt(t, sp, 200, aprT0)
	advanceAt(t, sp, 201, aprT0.Add(day))
	if got := persistedAPR(t, sp); got != 700 {
		t.Fatalf("persisted rate %d, want 700", got)
	}
}

// The in-force rate the claim events report.
func TestStakeAPRFromState_InForceAccessor(t *testing.T) {
	sp := newAPRProcessor(t, aprRule(100), 3000)
	setAPRParam(t, sp, "700")
	sp.BeginBlock(99, aprT0)
	if got := sp.stakeAPRInForce(); got != 3000 {
		t.Fatalf("before the height: %d, want the cached 3000", got)
	}
	sp.BeginBlock(100, aprT0)
	if got := sp.stakeAPRInForce(); got != 700 {
		t.Fatalf("at the height: %d, want the chain's 700", got)
	}
}

// A governed change: the interval before it is settled at the old rate in the block that
// executes it, and the new rate applies from there on. Driven through the real
// propose, finalize, queue and execute transactions.
func TestStakeAPRFromState_GovernedChangeSettlesAtTheOldRate(t *testing.T) {
	sp := newAPRProcessor(t, aprRule(100), 3000)
	sp.SetGovernancePolicy(governance.ProposalPolicy{
		MinDepositWei:       big.NewInt(0),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
		AllowedParams:       []string{governance.ParamKeyStakingAprBps},
	})
	advanceAt(t, sp, 200, aprT0) // default 1250 from here
	if got := persistedAPR(t, sp); got != 1250 {
		t.Fatalf("setup: persisted rate %d, want the default 1250", got)
	}

	execAt := aprT0.Add(40 * day)
	sp.BeginBlock(210, execAt)
	proposer := freshGovAccountKey(t, sp, big.NewInt(0))
	applySignedGovTx(t, sp, proposer, types.TxTypeGovPropose, govProposePayload{
		Kind:    governance.ProposalKindParamUpdate,
		Payload: `{"staking.aprBps":2000}`,
		Deposit: big.NewInt(0),
	})
	proposalID := latestGovProposalID(t, sp)
	markGovProposalPassed(t, sp, proposalID)
	queuer := freshGovAccountKey(t, sp, big.NewInt(0))
	applySignedGovTx(t, sp, queuer, types.TxTypeGovQueue, govProposalIDPayload{ProposalID: proposalID})
	clearGovProposalTimelock(t, sp, proposalID)
	executor := freshGovAccountKey(t, sp, big.NewInt(0))
	applySignedGovTx(t, sp, executor, types.TxTypeGovExecute, govProposalIDPayload{ProposalID: proposalID})

	// In the block that executed it the index has been settled at the old rate.
	wantSettled := referenceIndex(aprStep{aprT0, 1250}, aprStep{execAt, 1250})
	if got := sp.stakeRewardEngine.Index(); got.Cmp(wantSettled) != 0 {
		t.Fatalf("index after the executing block %s, want it settled at 1250: %s", got, wantSettled)
	}
	if got := persistedAPR(t, sp); got != 2000 {
		t.Fatalf("persisted rate %d, want the new 2000", got)
	}
	// 30 days later the new rate has applied for exactly those 30 days.
	later := advanceAt(t, sp, 220, execAt.Add(30*day))
	want := referenceIndex(aprStep{aprT0, 1250}, aprStep{execAt, 1250}, aprStep{execAt.Add(30 * day), 2000})
	if later.Cmp(want) != 0 {
		t.Fatalf("index %s, want %s", later, want)
	}
}

// A change made without the governance execution path is noticed at the next touch: the
// interval since the last update settles at the old rate and the new rate starts there.
// No node can disagree, because the old rate is read from the state.
func TestStakeAPRFromState_ChangeNoticedAtTheNextTouch(t *testing.T) {
	sp := newAPRProcessor(t, aprRule(100), 3000)
	advanceAt(t, sp, 200, aprT0)
	setAPRParam(t, sp, "2000")
	got := advanceAt(t, sp, 201, aprT0.Add(30*day))
	want := referenceIndex(aprStep{aprT0, 1250}, aprStep{aprT0.Add(30 * day), 1250})
	if got.Cmp(want) != 0 {
		t.Fatalf("index %s, want the interval settled at the old rate: %s", got, want)
	}
	later := advanceAt(t, sp, 202, aprT0.Add(60*day))
	want = referenceIndex(aprStep{aprT0, 1250}, aprStep{aprT0.Add(30 * day), 1250}, aprStep{aprT0.Add(60 * day), 2000})
	if later.Cmp(want) != 0 {
		t.Fatalf("index %s, want %s", later, want)
	}
}

// Same history, same state: two independent nodes that see a change reach the same root.
func TestStakeAPRFromState_TwoNodesAgreeAcrossAChange(t *testing.T) {
	run := func(cached uint64) *StateProcessor {
		sp := newAPRProcessor(t, aprRule(100), cached)
		advanceAt(t, sp, 200, aprT0)
		setAPRParam(t, sp, "2000")
		advanceAt(t, sp, 201, aprT0.Add(30*day))
		advanceAt(t, sp, 202, aprT0.Add(60*day))
		return sp
	}
	a, b := run(1250), run(777)
	if a.Trie.Hash() != b.Trie.Hash() {
		t.Fatalf("two nodes disagree across a governed change")
	}
}

// The claim event reports the rate in force: the chain's from the activation height, the
// cached one before it.
func TestStakeAPRFromState_ClaimEventReportsTheRateInForce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		from  uint64
		wantR string
	}{{"off", 0, "3000"}, {"on", 100, "700"}} {
		sp := newAPRProcessor(t, aprRule(tc.from), 3000)
		setAPRParam(t, sp, "700")
		var delegator [20]byte
		delegator[19] = 0x93
		period := time.Duration(stakePayoutPeriodSeconds) * time.Second
		start, end := time.Unix(1_800_100_000, 0).UTC(), time.Unix(1_800_100_000, 0).UTC().Add(period)
		engine := rewards.NewEngine()
		engine.UpdateGlobalIndex(start, 700)
		indexStart := engine.Index()
		engine.UpdateGlobalIndex(end, 700)
		indexEnd := engine.Index()
		account := &types.Account{StakeShares: big.NewInt(2_000), StakeLastIndex: new(big.Int).Set(indexStart), StakeLastPayoutTs: uint64(start.Unix())}
		writeAccount(t, sp, delegator, account)
		manager := nhbstate.NewManager(sp.Trie)
		if err := manager.PutAccountMetadata(delegator[:], account); err != nil {
			t.Fatal(err)
		}
		if err := manager.SetStakingGlobalIndex(indexEnd); err != nil {
			t.Fatal(err)
		}
		sp.BeginBlock(200, end)
		if _, err := sp.StakeClaimRewards(delegator[:]); err != nil {
			t.Fatalf("%s: claim: %v", tc.name, err)
		}
		got := ""
		for _, evt := range sp.Events() {
			if evt.Type == events.TypeStakeRewardsClaimed {
				got = evt.Attributes["aprBps"]
			}
		}
		if got != tc.wantR {
			t.Fatalf("%s: the claim event reports aprBps %q, want %q", tc.name, got, tc.wantR)
		}
	}
}

// Empty, blank and over-wide stored values give the fixed default, whatever this node
// has cached.
func TestStakeAPRFromState_EmptyAndOverWideValuesMeanTheDefault(t *testing.T) {
	for _, raw := range []string{" ", "\" \"", "\"\"", "5000000000"} {
		sp := newAPRProcessor(t, aprRule(100), 3000)
		setAPRParam(t, sp, raw)
		advanceAt(t, sp, 200, aprT0)
		got := advanceAt(t, sp, 201, aprT0.Add(30*day))
		want := referenceIndex(aprStep{aprT0, 1250}, aprStep{aprT0.Add(30 * day), 1250})
		if got.Cmp(want) != 0 {
			t.Fatalf("param %q: index %s, want the default index %s", raw, got, want)
		}
	}
}
