package core

import (
	"math/big"
	"sort"
	"testing"
	"time"

	"nhbchain/core/epoch"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

// PL-VAL-01: any holder of the minimum validator stake can stop the chain by joining as
// a validator while the incumbents sit close to a third each. A validator that registers
// and holds the minimum enters the set at the next epoch boundary; the quorum is more
// than two thirds of the total, so the incumbents then cannot commit without it, and if
// it is switched off nothing can remove it (nothing is processed while blocks stop).
//
// The rule under test: from an activation height a validator that has not been in the
// set before (or is still inside its probation) is admitted only while the validators
// that have been in it for the whole probation keep strictly more than two thirds of the
// total power between them. These tests run the real epoch selection.

const admitProbation = 7 * 24 * 60 * 60 // seconds

var admitT0 = time.Unix(1_800_000_000, 0).UTC()

type admitEnv struct {
	t       *testing.T
	sp      *StateProcessor
	manager *nhbstate.Manager
	v1, v2  [20]byte
	now     time.Time
	epoch   uint64
}

func admitRules(height, probation uint64) ValidatorRules {
	return ValidatorRules{ValidatorAdmissionGuardActivationHeight: height, ValidatorAdmissionProbationSeconds: probation}
}

// newAdmitEnv builds two validators of 20,000 each, registered at T0 and in the set, and
// a clock a month later.
func newAdmitEnv(t *testing.T, rules ValidatorRules) *admitEnv {
	t.Helper()
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(1_000)); err != nil {
		t.Fatalf("minimum validator stake: %v", err)
	}
	sp.SetValidatorRules(rules)
	e := &admitEnv{t: t, sp: sp, manager: manager, now: admitT0.Add(30 * 24 * time.Hour)}
	e.v1[19], e.v2[19] = 0xA1, 0xA2
	sp.BeginBlock(5, admitT0)
	for _, v := range [][20]byte{e.v1, e.v2} {
		e.register(v, 20_000)
		sp.ValidatorSet[string(v[:])] = big.NewInt(20_000)
	}
	if err := sp.persistValidatorSet(); err != nil {
		t.Fatalf("persist validator set: %v", err)
	}
	return e
}

func (e *admitEnv) register(addr [20]byte, stake int64) {
	e.t.Helper()
	writeAccount(e.t, e.sp, addr, &types.Account{BalanceZNHB: big.NewInt(10_000_000)})
	if err := e.sp.applyStake(&types.Transaction{
		Value: big.NewInt(stake),
		Data:  mustEncodeStakePayload(e.t, stakePayload{RegisterValidator: true}),
	}, addr[:]); err != nil {
		e.t.Fatalf("register: %v", err)
	}
}

// newcomer registers a validator at registeredAt with stake and returns its address.
func (e *admitEnv) newcomer(seed byte, stake int64, registeredAt time.Time) [20]byte {
	e.t.Helper()
	var addr [20]byte
	addr[18], addr[19] = 0xC0, seed
	e.sp.BeginBlock(6, registeredAt)
	e.register(addr, stake)
	return addr
}

// beat gives every registered candidate a fresh heartbeat as of now, so the readiness
// gate of the selection passes for all of them.
func (e *admitEnv) beat(addrs ...[20]byte) {
	e.t.Helper()
	for _, a := range addrs {
		acct, err := e.manager.GetAccount(a[:])
		if err != nil {
			e.t.Fatal(err)
		}
		acct.EngagementLastHeartbeat = uint64(e.now.Unix())
		if err := e.manager.PutAccount(a[:], acct); err != nil {
			e.t.Fatal(err)
		}
	}
}

// selectAt runs the epoch boundary's validator selection at height and the clock, after
// refreshing every eligible validator's heartbeat.
func (e *admitEnv) selectAt(height uint64) {
	e.t.Helper()
	// In address order: writing accounts in another order leaves another state hash
	// (a property of the account store, not of the rule), and the two-node tests
	// compare hashes.
	keys := make([]string, 0, len(e.sp.EligibleValidators))
	for k := range e.sp.EligibleValidators {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var a [20]byte
		copy(a[:], k)
		e.beat(a)
	}
	e.sp.BeginBlock(height, e.now)
	e.epoch++
	if err := e.sp.applyValidatorSelection(epoch.Snapshot{Epoch: e.epoch, Height: height}, e.now); err != nil {
		e.t.Fatalf("selection at %d: %v", height, err)
	}
}

func (e *admitEnv) inSet(a [20]byte) bool {
	_, ok := e.sp.ValidatorSet[string(a[:])]
	return ok
}

// incumbentsCommitAlone reports whether v1 and v2 together hold a quorum of the set.
func (e *admitEnv) incumbentsCommitAlone() bool {
	signed := new(big.Int).Add(e.sp.ValidatorSet[string(e.v1[:])], e.sp.ValidatorSet[string(e.v2[:])])
	total := new(big.Int)
	for _, p := range e.sp.ValidatorSet {
		total.Add(total, p)
	}
	return types.HasQuorum(signed, total)
}

// Off (the shipped and live setting, a never-reached height, or a probation of zero)
// every eligible validator is admitted, and a newcomer of the incumbents' size takes
// the chain to the state the finding describes: the incumbents cannot commit alone.
func TestValidatorAdmission_OffAdmitsEveryone(t *testing.T) {
	for _, rules := range []ValidatorRules{
		admitRules(0, admitProbation),
		admitRules(1_000_000, admitProbation),
		admitRules(100, 0), // a height but no probation: no effect
	} {
		env := newAdmitEnv(t, rules)
		n := env.newcomer(1, 20_000, env.now.Add(-time.Hour))
		env.selectAt(200)
		if !env.inSet(n) {
			t.Fatalf("%+v: the newcomer was not admitted with the guard off", rules)
		}
		if env.incumbentsCommitAlone() {
			t.Fatalf("%+v: setup: the incumbents should not hold a quorum alone after a newcomer their own size joined", rules)
		}
	}
}

// On, a newcomer is admitted while the established validators keep strictly more than
// two thirds, and not beyond: 19,999 against 40,000 is in, 20,000 is deferred.
func TestValidatorAdmission_NewcomerBoundary(t *testing.T) {
	for _, tc := range []struct {
		stake int64
		in    bool
	}{{10_000, true}, {19_999, true}, {20_000, false}, {500_000, false}} {
		env := newAdmitEnv(t, admitRules(100, admitProbation))
		n := env.newcomer(1, tc.stake, env.now.Add(-time.Hour))
		env.selectAt(200)
		if env.inSet(n) != tc.in {
			t.Errorf("newcomer of %d: in set = %v, want %v", tc.stake, env.inSet(n), tc.in)
		}
		if !env.incumbentsCommitAlone() {
			t.Errorf("newcomer of %d: the incumbents lost their quorum", tc.stake)
		}
		if !env.inSet(env.v1) || !env.inSet(env.v2) {
			t.Errorf("newcomer of %d: an incumbent left the set", tc.stake)
		}
	}
}

// Newcomers share the budget first come, first served by registration time, then by
// address: three of 9,000 against 40,000 admit two (18,000 < 20,000, 27,000 is not).
func TestValidatorAdmission_NewcomersShareTheBudgetInRegistrationOrder(t *testing.T) {
	env := newAdmitEnv(t, admitRules(100, admitProbation))
	late := env.newcomer(1, 9_000, env.now.Add(-time.Hour))
	first := env.newcomer(2, 9_000, env.now.Add(-3*time.Hour))
	second := env.newcomer(3, 9_000, env.now.Add(-2*time.Hour))
	env.selectAt(200)
	if !env.inSet(first) || !env.inSet(second) || env.inSet(late) {
		t.Fatalf("admitted first=%v second=%v late=%v, want true true false", env.inSet(first), env.inSet(second), env.inSet(late))
	}
	if !env.incumbentsCommitAlone() {
		t.Fatalf("the incumbents lost their quorum")
	}
	// Equal registration times fall back to the address, whichever order they were added.
	env = newAdmitEnv(t, admitRules(100, admitProbation))
	at := env.now.Add(-time.Hour)
	hi := env.newcomer(9, 15_000, at)
	lo := env.newcomer(1, 15_000, at)
	env.selectAt(200)
	if !env.inSet(lo) || env.inSet(hi) {
		t.Fatalf("with equal registration times the lower address goes first: lo=%v hi=%v", env.inSet(lo), env.inSet(hi))
	}
}

// A validator that has been in the set for the whole probation counts with the
// established ones, so the budget grows as newcomers season. Until then it counts
// against the budget.
func TestValidatorAdmission_NewcomersSeasonAfterTheProbation(t *testing.T) {
	env := newAdmitEnv(t, admitRules(100, admitProbation))
	a := env.newcomer(1, 15_000, env.now.Add(-time.Hour))
	env.selectAt(200)
	if !env.inSet(a) {
		t.Fatalf("setup: the first newcomer was not admitted")
	}
	b := env.newcomer(2, 15_000, env.now.Add(-time.Minute))
	env.now = env.now.Add(time.Hour)
	env.selectAt(300)
	if env.inSet(b) {
		t.Fatalf("a second newcomer was admitted while the first is still inside its probation (15,000 + 15,000 against 40,000)")
	}
	// After the probation the first counts as established: 55,000 established, so the
	// budget is below 27,500 and the second fits.
	env.now = env.now.Add(admitProbation * time.Second)
	env.selectAt(400)
	if !env.inSet(b) {
		t.Fatalf("the second newcomer was still refused after the first had seasoned")
	}
}

// A validator that was in the set when the guard started is established for good: if
// it drops out (its heartbeat goes stale) and comes back, it is not a newcomer, even
// though its power is more than the budget. Otherwise a blip of one of two validators
// would keep it out for ever.
func TestValidatorAdmission_AnIncumbentThatReturnsIsNotANewcomer(t *testing.T) {
	env := newAdmitEnv(t, admitRules(100, admitProbation))
	env.selectAt(150) // the guard starts: the two in the set are recorded as established
	// v2 goes quiet for an epoch: only v1 is ready.
	env.sp.BeginBlock(200, env.now)
	env.beat(env.v1)
	acct, _ := env.manager.GetAccount(env.v2[:])
	acct.EngagementLastHeartbeat = uint64(env.now.Add(-2 * time.Hour).Unix())
	if err := env.manager.PutAccount(env.v2[:], acct); err != nil {
		t.Fatal(err)
	}
	if err := env.sp.applyValidatorSelection(epoch.Snapshot{Epoch: 9, Height: 200}, env.now); err != nil {
		t.Fatal(err)
	}
	if env.inSet(env.v2) {
		t.Fatalf("setup: v2 should have dropped out with a stale heartbeat")
	}
	env.now = env.now.Add(10 * time.Minute)
	env.selectAt(300)
	if !env.inSet(env.v2) {
		t.Fatalf("an established validator was kept out after a blip: 20,000 against a single other established 20,000")
	}
}

// The guard only admits; it never removes: a newcomer already in the set stays when the
// budget would no longer allow it (the established power fell).
func TestValidatorAdmission_NeverRemovesAMember(t *testing.T) {
	env := newAdmitEnv(t, admitRules(100, admitProbation))
	a := env.newcomer(1, 19_000, env.now.Add(-time.Hour))
	env.selectAt(200)
	if !env.inSet(a) {
		t.Fatalf("setup: not admitted")
	}
	// v2's stake falls, so 19,000 is more than half of what is established.
	acct, _ := env.manager.GetAccount(env.v2[:])
	acct.Stake = big.NewInt(1_000)
	acct.LockedZNHB = big.NewInt(1_000)
	if err := env.sp.setAccount(env.v2[:], acct); err != nil {
		t.Fatal(err)
	}
	env.now = env.now.Add(time.Hour)
	env.selectAt(300)
	if !env.inSet(a) {
		t.Fatalf("the guard removed a member")
	}
}

// With no established validator in the set (a young chain, or a set that emptied) there is
// nothing to protect and everyone is admitted.
func TestValidatorAdmission_NoEstablishedValidatorsMeansNoRestriction(t *testing.T) {
	env := newAdmitEnv(t, admitRules(100, admitProbation))
	env.sp.ValidatorSet = map[string]*big.Int{}
	if err := env.sp.persistValidatorSet(); err != nil {
		t.Fatal(err)
	}
	// Both former members are also gone from the eligible map, as if they never existed.
	delete(env.sp.EligibleValidators, string(env.v1[:]))
	delete(env.sp.EligibleValidators, string(env.v2[:]))
	a := env.newcomer(1, 500_000, env.now.Add(-time.Hour))
	b := env.newcomer(2, 500_000, env.now.Add(-time.Hour))
	env.selectAt(200)
	if !env.inSet(a) || !env.inSet(b) {
		t.Fatalf("a young chain refused its first validators: %v %v", env.inSet(a), env.inSet(b))
	}
}

// The guard starts at its height.
func TestValidatorAdmission_ActivationBoundary(t *testing.T) {
	before := newAdmitEnv(t, admitRules(200, admitProbation))
	n := before.newcomer(1, 20_000, before.now.Add(-time.Hour))
	before.selectAt(199)
	if !before.inSet(n) {
		t.Fatalf("the guard applied one block early")
	}
	at := newAdmitEnv(t, admitRules(200, admitProbation))
	n = at.newcomer(1, 20_000, at.now.Add(-time.Hour))
	at.selectAt(200)
	if at.inSet(n) {
		t.Fatalf("the guard did not apply at its height")
	}
}

// The records the guard keeps: the members at the start as established (zero), a newcomer
// at the time it was admitted, a deferred one not at all.
func TestValidatorAdmission_RecordsWhoWasAdmittedWhen(t *testing.T) {
	env := newAdmitEnv(t, admitRules(100, admitProbation))
	in := env.newcomer(1, 10_000, env.now.Add(-time.Hour))
	out := env.newcomer(2, 30_000, env.now.Add(-time.Hour))
	env.selectAt(200)
	rec, err := env.sp.loadAdmittedAt()
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range [][20]byte{env.v1, env.v2} {
		if at, ok := rec[string(v[:])]; !ok || at != 0 {
			t.Errorf("an incumbent is recorded as %v (present %v), want established (0)", at, ok)
		}
	}
	if at, ok := rec[string(in[:])]; !ok || at != uint64(env.now.Unix()) {
		t.Errorf("the admitted newcomer is recorded at %v (present %v), want %d", at, ok, env.now.Unix())
	}
	if _, ok := rec[string(out[:])]; ok {
		t.Errorf("a deferred newcomer was recorded")
	}
}

// Nothing is written while the guard is off: the state equals the state of a chain
// that does not have the rule.
func TestValidatorAdmission_OffWritesNothing(t *testing.T) {
	a := newAdmitEnv(t, admitRules(0, admitProbation))
	b := newAdmitEnv(t, admitRules(1_000_000, admitProbation))
	a.newcomer(1, 10_000, a.now.Add(-time.Hour))
	b.newcomer(1, 10_000, b.now.Add(-time.Hour))
	a.selectAt(200)
	b.selectAt(200)
	if a.sp.Trie.Hash() != b.sp.Trie.Hash() {
		t.Fatalf("two off configurations ended in different states")
	}
	if data, _ := a.sp.Trie.Get(validatorAdmittedKey); len(data) != 0 {
		t.Fatalf("the admission record was written with the guard off")
	}
}

// Two nodes that see the same history reach the same set and the same state.
func TestValidatorAdmission_TwoNodesAgree(t *testing.T) {
	run := func() *admitEnv {
		env := newAdmitEnv(t, admitRules(100, admitProbation))
		env.newcomer(1, 9_000, env.now.Add(-3*time.Hour))
		env.newcomer(2, 9_000, env.now.Add(-2*time.Hour))
		env.newcomer(3, 9_000, env.now.Add(-time.Hour))
		env.selectAt(200)
		return env
	}
	a, b := run(), run()
	if a.sp.Trie.Hash() != b.sp.Trie.Hash() {
		t.Fatalf("two nodes disagree on the admission outcome")
	}
}

// The rotating-set branch applies the same rule to the validators it selects.
func TestValidatorAdmission_RotationBranchAppliesTheSameRule(t *testing.T) {
	env := newAdmitEnv(t, admitRules(100, admitProbation))
	env.sp.epochConfig.RotationEnabled = true
	n := env.newcomer(1, 20_000, env.now.Add(-time.Hour))
	env.sp.BeginBlock(200, env.now)
	env.beat(env.v1, env.v2, n)
	selected := [][]byte{env.v1[:], env.v2[:], n[:]}
	if err := env.sp.applyValidatorSelection(epoch.Snapshot{Epoch: 3, Height: 200, Selected: selected}, env.now); err != nil {
		t.Fatal(err)
	}
	if env.inSet(n) {
		t.Fatalf("the rotating branch admitted a newcomer the budget does not allow")
	}
	if !env.inSet(env.v1) || !env.inSet(env.v2) {
		t.Fatalf("the rotating branch dropped an incumbent")
	}
}

// With only validators that are themselves still inside their probation in the set (a
// young chain), there is nothing established to protect: a further newcomer is admitted
// whatever its size.
func TestValidatorAdmission_ProbationaryOnlySetAdmitsFurtherNewcomers(t *testing.T) {
	env := newAdmitEnv(t, admitRules(100, admitProbation))
	env.sp.ValidatorSet = map[string]*big.Int{}
	if err := env.sp.persistValidatorSet(); err != nil {
		t.Fatal(err)
	}
	delete(env.sp.EligibleValidators, string(env.v1[:]))
	delete(env.sp.EligibleValidators, string(env.v2[:]))
	a := env.newcomer(1, 500_000, env.now.Add(-time.Hour))
	b := env.newcomer(2, 500_000, env.now.Add(-time.Hour))
	env.selectAt(200)
	if !env.inSet(a) || !env.inSet(b) {
		t.Fatalf("setup: the first two were not admitted")
	}
	// An hour on, all of the set is still probationary; a third, as large, is admitted.
	c := env.newcomer(3, 500_000, env.now)
	env.now = env.now.Add(time.Hour)
	env.selectAt(300)
	if !env.inSet(c) {
		t.Fatalf("a set with no established validator refused a newcomer")
	}
}

// A newcomer that leaves and returns inside its probation keeps the clock it was admitted
// on: it is established when the original probation ends, not a probation after it
// returned.
func TestValidatorAdmission_AReturningNewcomerKeepsItsClock(t *testing.T) {
	env := newAdmitEnv(t, admitRules(100, admitProbation))
	a := env.newcomer(1, 15_000, env.now.Add(-time.Hour))
	env.selectAt(200) // a admitted at env.now
	admitted := env.now
	if !env.inSet(a) {
		t.Fatalf("setup: not admitted")
	}
	// Three days on a has a stale heartbeat and drops out; a day later it returns.
	env.now = admitted.Add(3 * 24 * time.Hour)
	env.sp.BeginBlock(300, env.now)
	env.beat(env.v1, env.v2)
	acct, _ := env.manager.GetAccount(a[:])
	acct.EngagementLastHeartbeat = uint64(env.now.Add(-2 * time.Hour).Unix())
	if err := env.manager.PutAccount(a[:], acct); err != nil {
		t.Fatal(err)
	}
	if err := env.sp.applyValidatorSelection(epoch.Snapshot{Epoch: 20, Height: 300}, env.now); err != nil {
		t.Fatal(err)
	}
	if env.inSet(a) {
		t.Fatalf("setup: a should have dropped out")
	}
	env.now = admitted.Add(4 * 24 * time.Hour)
	env.selectAt(400)
	if !env.inSet(a) {
		t.Fatalf("setup: a did not return (15,000 against 40,000 established fits)")
	}
	// Just past the original probation another newcomer of 15,000 fits only if a is
	// counted as established: 55,000 established against 15,000 new, not 40,000 against 30,000.
	b := env.newcomer(2, 15_000, env.now)
	env.now = admitted.Add(admitProbation*time.Second + time.Hour)
	env.selectAt(500)
	if !env.inSet(b) {
		t.Fatalf("the returning newcomer was not counted as established after its original probation ended")
	}
}
