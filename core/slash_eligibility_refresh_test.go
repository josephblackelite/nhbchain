package core

import (
	"bytes"
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/consensus/potso/evidence"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

// PL-R5T-25: the equivocation slasher debits the offender's stake straight through
// the state manager, which does not go through setAccount, the one place the
// eligible-validator map and the validator set are re-evaluated. After a slash the
// persisted eligibility entry keeps the stake it had before, and the next epoch
// selection and the POTSO reward epoch copy that stale figure. These tests build a
// real registered validator, put a genuine double-sign report in state and process
// it the way a block does.

var slashNow = time.Unix(1_700_700_000, 0).UTC()

// fixedEvidenceTestKey is a key derived from seed alone, so a test can build the same
// world twice and compare the two states byte for byte.
func fixedEvidenceTestKey(t *testing.T, seed byte) *evidenceTestKey {
	t.Helper()
	raw := make([]byte, 32)
	for i := range raw {
		raw[i] = seed
	}
	priv, err := ethcrypto.ToECDSA(raw)
	if err != nil {
		t.Fatalf("fixed key: %v", err)
	}
	return &evidenceTestKey{priv: priv}
}

type slashEnv struct {
	t       *testing.T
	sp      *StateProcessor
	manager *nhbstate.Manager
	// offender is the validator that double-signed, other a second validator that did not.
	offender [20]byte
	other    [20]byte
	ev       evidence.Evidence
	hash     [32]byte
	reporter [20]byte
}

// newSlashEnv registers the offender with ownStake of its own and delegatedIn from
// a delegator, registers a second validator, puts both in the validator set, and
// stores a genuine equivocation report against the offender.
func newSlashEnv(t *testing.T, rules ValidatorRules, ownStake, delegatedIn int64) *slashEnv {
	t.Helper()
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(1_000)); err != nil {
		t.Fatalf("minimum validator stake: %v", err)
	}
	// Fixed keys: two environments built the same way must end with the same state, so
	// their addresses cannot be random.
	offenderKey := fixedEvidenceTestKey(t, 0x71)
	reporterKey := fixedEvidenceTestKey(t, 0x72)
	env := &slashEnv{t: t, sp: sp, manager: manager, offender: offenderKey.address(), reporter: reporterKey.address()}
	env.other[19] = 0xA9

	for _, v := range [][20]byte{env.offender, env.other} {
		writeAccount(t, sp, v, &types.Account{BalanceZNHB: big.NewInt(1_000_000)})
		if err := sp.applyStake(&types.Transaction{
			Value: big.NewInt(ownStake),
			Data:  mustEncodeStakePayload(t, stakePayload{RegisterValidator: true}),
		}, v[:]); err != nil {
			t.Fatalf("register validator: %v", err)
		}
	}
	if delegatedIn > 0 {
		var delegator [20]byte
		delegator[19] = 0xD1
		writeAccount(t, sp, delegator, &types.Account{BalanceZNHB: big.NewInt(1_000_000)})
		if _, err := sp.StakeDelegate(delegator[:], env.offender[:], big.NewInt(delegatedIn)); err != nil {
			t.Fatalf("delegate: %v", err)
		}
	}
	// Both are in the set that votes, at the basis they were eligible with.
	for _, v := range [][20]byte{env.offender, env.other} {
		basis, ok := sp.EligibleValidators[string(v[:])]
		if !ok {
			t.Fatalf("validator %x is not eligible after registering", v[:2])
		}
		sp.ValidatorSet[string(v[:])] = new(big.Int).Set(basis)
	}
	if err := sp.persistValidatorSet(); err != nil {
		t.Fatalf("persist validator set: %v", err)
	}
	sp.SetValidatorRules(rules)

	ev, hash := buildGenuineEquivocationEvidence(t, offenderKey, reporterKey, 50)
	env.ev, env.hash = ev, hash
	record := &evidence.Record{Hash: hash, Evidence: ev.Clone(), ReceivedAt: slashNow.Unix()}
	if err := manager.PotsoEvidencePutRecord(record); err != nil {
		t.Fatalf("store the report: %v", err)
	}
	return env
}

// process runs the penalty pass of a block at height.
func (e *slashEnv) process(height uint64) {
	e.t.Helper()
	e.sp.BeginBlock(height, slashNow.Add(time.Duration(height)*time.Second))
	if err := e.sp.processPendingEvidence(height); err != nil {
		e.t.Fatalf("process evidence at %d: %v", height, err)
	}
}

func (e *slashEnv) root() []byte {
	e.t.Helper()
	return append([]byte(nil), e.sp.Trie.Hash().Bytes()...)
}

// persistedMatchesMemory checks that what the trie holds for the eligible-validator
// map and the validator set is what the processor holds in memory.
func (e *slashEnv) persistedMatchesMemory() {
	e.t.Helper()
	for name, c := range map[string]struct {
		key []byte
		set map[string]*big.Int
	}{
		"eligible validators": {validatorEligibleKey, e.sp.EligibleValidators},
		"validator set":       {validatorSetKey, e.sp.ValidatorSet},
	} {
		want, err := nhbstate.EncodeValidatorSet(c.set)
		if err != nil {
			e.t.Fatalf("encode %s: %v", name, err)
		}
		got, err := e.sp.Trie.Get(c.key)
		if err != nil {
			e.t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			e.t.Fatalf("the persisted %s differs from the in-memory one", name)
		}
	}
}

// Off (the shipped and live setting), nothing changes: the stale entry stays. This
// pins today's behaviour so the switch is the only thing that moves it.
func TestSlashEligibilityRefresh_OffLeavesTheStaleEntry(t *testing.T) {
	for _, from := range []uint64{0, 1_000_000} { // zero, and a height this run never reaches
		env := newSlashEnv(t, ValidatorRules{SlashEligibilityRefreshActivationHeight: from}, 2_000, 0)
		env.process(200)
		acct, err := env.manager.GetAccount(env.offender[:])
		if err != nil {
			t.Fatal(err)
		}
		if acct.Stake.Sign() != 0 {
			t.Fatalf("height %d: the offender kept stake %s: the test did not slash", from, acct.Stake)
		}
		if got := env.sp.EligibleValidators[string(env.offender[:])]; got == nil || got.Cmp(big.NewInt(2_000)) != 0 {
			t.Fatalf("height %d: expected the stale eligibility entry of 2000, got %v", from, got)
		}
		if _, ok := env.sp.ValidatorSet[string(env.offender[:])]; !ok {
			t.Fatalf("height %d: the slashed validator left the set with the setting off", from)
		}
	}
}

// A validator slashed to nothing stops being eligible and stops voting in the same
// block, in memory and in the persisted maps.
func TestSlashEligibilityRefresh_FullSlashRemovesTheValidator(t *testing.T) {
	env := newSlashEnv(t, ValidatorRules{SlashEligibilityRefreshActivationHeight: 100}, 2_000, 0)
	env.process(200)
	if _, ok := env.sp.EligibleValidators[string(env.offender[:])]; ok {
		t.Fatalf("a validator with no stake left is still eligible")
	}
	if _, ok := env.sp.ValidatorSet[string(env.offender[:])]; ok {
		t.Fatalf("a validator with no stake left still has voting power")
	}
	// The other validator is untouched.
	if got := env.sp.EligibleValidators[string(env.other[:])]; got == nil || got.Cmp(big.NewInt(2_000)) != 0 {
		t.Fatalf("the other validator's eligibility changed: %v", got)
	}
	if _, ok := env.sp.ValidatorSet[string(env.other[:])]; !ok {
		t.Fatalf("the other validator left the set")
	}
	env.persistedMatchesMemory()
}

// A slash that leaves the offender above the minimum keeps it eligible, at the
// stake it has now and not at the one it had.
func TestSlashEligibilityRefresh_PartialSlashLowersTheBasis(t *testing.T) {
	// 2,000 of its own and 3,000 delegated in: the slasher can reach only the 2,000.
	env := newSlashEnv(t, ValidatorRules{SlashEligibilityRefreshActivationHeight: 100}, 2_000, 3_000)
	if got := env.sp.EligibleValidators[string(env.offender[:])]; got.Cmp(big.NewInt(5_000)) != 0 {
		t.Fatalf("setup: eligible at %v, want 5000", got)
	}
	env.process(200)
	got := env.sp.EligibleValidators[string(env.offender[:])]
	if got == nil || got.Cmp(big.NewInt(3_000)) != 0 {
		t.Fatalf("eligible at %v after the slash, want 3000", got)
	}
	env.persistedMatchesMemory()
}

// The rule starts at its height and not before: the same report processed one block
// earlier leaves the stale entry.
func TestSlashEligibilityRefresh_ActivationBoundary(t *testing.T) {
	before := newSlashEnv(t, ValidatorRules{SlashEligibilityRefreshActivationHeight: 200}, 2_000, 0)
	before.process(199)
	if _, ok := before.sp.EligibleValidators[string(before.offender[:])]; !ok {
		t.Fatalf("the refresh ran one block before its height")
	}
	at := newSlashEnv(t, ValidatorRules{SlashEligibilityRefreshActivationHeight: 200}, 2_000, 0)
	at.process(200)
	if _, ok := at.sp.EligibleValidators[string(at.offender[:])]; ok {
		t.Fatalf("the refresh did not run at its height")
	}
}

// Slashing an ordinary account (one that is not a validator) writes nothing new: the
// state root with the rule on equals the root with it off.
func TestSlashEligibilityRefresh_NonValidatorWritesNothing(t *testing.T) {
	build := func(rules ValidatorRules) *slashEnv {
		env := newSlashEnv(t, rules, 2_000, 0)
		// The offender unregisters before the report is processed: an ordinary
		// account with locked ZNHB that is not in either set.
		acct, err := env.manager.GetAccount(env.offender[:])
		if err != nil {
			t.Fatal(err)
		}
		acct.ValidatorRegistered = false
		if err := env.manager.PutAccount(env.offender[:], acct); err != nil {
			t.Fatal(err)
		}
		delete(env.sp.EligibleValidators, string(env.offender[:]))
		delete(env.sp.ValidatorSet, string(env.offender[:]))
		if err := env.sp.persistEligibleValidatorSet(); err != nil {
			t.Fatal(err)
		}
		if err := env.sp.persistValidatorSet(); err != nil {
			t.Fatal(err)
		}
		return env
	}
	off := build(ValidatorRules{})
	on := build(ValidatorRules{SlashEligibilityRefreshActivationHeight: 100})
	off.process(200)
	on.process(200)
	if !bytes.Equal(off.root(), on.root()) {
		t.Fatalf("slashing a non-validator wrote different state with the rule on")
	}
}

// Two nodes that process the same report reach the same state, with the rule on.
func TestSlashEligibilityRefresh_TwoNodesAgree(t *testing.T) {
	rules := ValidatorRules{SlashEligibilityRefreshActivationHeight: 100}
	a := newSlashEnv(t, rules, 2_000, 3_000)
	b := newSlashEnv(t, rules, 2_000, 3_000)
	a.process(200)
	b.process(200)
	if !bytes.Equal(a.root(), b.root()) {
		t.Fatalf("two nodes that processed the same report ended in different states")
	}
	if ga := a.sp.EligibleValidators[string(a.offender[:])]; ga == nil || ga.Cmp(big.NewInt(3_000)) != 0 {
		t.Fatalf("eligible at %v, want 3000", ga)
	}
	// Reprocessing the same report on the same node changes nothing (the offense is
	// already penalised).
	before := a.root()
	a.process(201)
	if !bytes.Equal(before, a.root()) {
		t.Fatalf("a second pass over an already penalised report changed state")
	}
}

// The setting is carried by the copies the node makes of its state processor, and the
// processor's rebuild path: a copy that dropped it would apply the rule from height 0.
func TestValidatorRulesAreCarriedByCopies(t *testing.T) {
	sp := newStakingStateProcessor(t)
	want := ValidatorRules{SlashEligibilityRefreshActivationHeight: 12345}
	sp.SetValidatorRules(want)
	cp, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	if got := cp.ValidatorRulesInForce(); got != want {
		t.Fatalf("copy holds %+v, want %+v", got, want)
	}
}

// A validator that is in the set that votes but missing from the eligible map (the
// two can drift apart through the operator recovery primitives) is still taken out
// when it is slashed to nothing.
func TestSlashEligibilityRefresh_InTheSetButNotInTheEligibleMap(t *testing.T) {
	env := newSlashEnv(t, ValidatorRules{SlashEligibilityRefreshActivationHeight: 100}, 2_000, 0)
	delete(env.sp.EligibleValidators, string(env.offender[:]))
	env.process(200)
	if _, ok := env.sp.ValidatorSet[string(env.offender[:])]; ok {
		t.Fatalf("a slashed validator that was only in the voting set kept its voting power")
	}
}

// Slashing one validator never touches another's place.
func TestSlashEligibilityRefresh_OnlyTheOffenderMoves(t *testing.T) {
	env := newSlashEnv(t, ValidatorRules{SlashEligibilityRefreshActivationHeight: 100}, 2_000, 0)
	otherBefore := new(big.Int).Set(env.sp.ValidatorSet[string(env.other[:])])
	env.process(200)
	if got := env.sp.ValidatorSet[string(env.other[:])]; got == nil || got.Cmp(otherBefore) != 0 {
		t.Fatalf("another validator's power changed from %s to %v", otherBefore, got)
	}
}

// An eligible validator that has not yet entered the voting set (it enters at the next
// epoch boundary) and is slashed to nothing must not enter it on the stale figure.
func TestSlashEligibilityRefresh_EligibleButNotYetVoting(t *testing.T) {
	env := newSlashEnv(t, ValidatorRules{SlashEligibilityRefreshActivationHeight: 100}, 2_000, 0)
	delete(env.sp.ValidatorSet, string(env.offender[:]))
	env.process(200)
	if _, ok := env.sp.EligibleValidators[string(env.offender[:])]; ok {
		t.Fatalf("a slashed validator that was not yet voting stays eligible and would enter the set at the next epoch")
	}
}
