package core

import (
	"errors"
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/consensus/potso/evidence"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
)

// PL-R5T-09: the evidence index keeps at most 256 live records (4 per reporter). Only
// an EQUIVOCATION report carries a checked proof; a DOWNTIME or INVALID_BLOCK_PROPOSAL
// report needs only the reporter's signature and does nothing when it is processed,
// yet each fills a slot. An equivocation made against a throwaway key fills one too.
// The reporter's bond is any LockedZNHB at or above the minimum, and a point-of-sale
// hold raises LockedZNHB and can be voided at once. With the index full a genuine
// double-sign is refused. These tests drive the real intake.

const intakeHeight = 200

type intakeEnv struct {
	t       *testing.T
	sp      *StateProcessor
	manager *nhbstate.Manager
	nonce   map[[20]byte]uint64
}

func newIntakeEnv(t *testing.T, rules ValidatorRules) *intakeEnv {
	t.Helper()
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.SetMinimumValidatorStake(big.NewInt(1_000)); err != nil {
		t.Fatalf("minimum validator stake: %v", err)
	}
	sp.SetValidatorRules(rules)
	return &intakeEnv{t: t, sp: sp, manager: manager, nonce: map[[20]byte]uint64{}}
}

// register makes addr a registered validator staking amount of its own: Stake and
// LockedZNHB both equal amount.
func (e *intakeEnv) register(addr [20]byte, amount int64) {
	e.t.Helper()
	writeAccount(e.t, e.sp, addr, &types.Account{BalanceZNHB: big.NewInt(1_000_000)})
	if err := e.sp.applyStake(&types.Transaction{
		Value: big.NewInt(amount),
		Data:  mustEncodeStakePayload(e.t, stakePayload{RegisterValidator: true}),
	}, addr[:]); err != nil {
		e.t.Fatalf("register: %v", err)
	}
}

// lockOnly gives addr locked ZNHB that is not staking stake, as a point-of-sale hold does.
func (e *intakeEnv) lockOnly(addr [20]byte, amount int64) {
	e.t.Helper()
	writeAccount(e.t, e.sp, addr, &types.Account{BalanceZNHB: big.NewInt(1_000_000), LockedZNHB: big.NewInt(amount)})
}

// ownUnbond gives addr a pending unbond of amount made out of its stake in itself.
func (e *intakeEnv) ownUnbond(addr [20]byte, amount int64) {
	e.t.Helper()
	acct, err := e.manager.GetAccount(addr[:])
	if err != nil {
		e.t.Fatal(err)
	}
	acct.PendingUnbonds = append(acct.PendingUnbonds, types.StakeUnbond{ID: 1, Validator: addr[:], Amount: big.NewInt(amount), ReleaseTime: 1_900_000_000})
	if err := e.manager.PutAccount(addr[:], acct); err != nil {
		e.t.Fatal(err)
	}
}

// report builds a signed report of the given type against offender by reporter.
func (e *intakeEnv) report(typ evidence.Type, offender *evidenceTestKey, reporter *evidenceTestKey, seed uint64) evidence.Evidence {
	e.t.Helper()
	if typ == evidence.TypeEquivocation {
		ev, _ := buildGenuineEquivocationEvidence(e.t, offender, reporter, 50+seed)
		return ev
	}
	ev := evidence.Evidence{
		Type:      typ,
		Offender:  offender.address(),
		Heights:   []uint64{50 + seed},
		Reporter:  reporter.address(),
		Timestamp: int64(seed) + 1,
	}
	hash, err := ev.CanonicalHash()
	if err != nil {
		e.t.Fatal(err)
	}
	sig, err := ethcrypto.Sign(ev.SigningDigest(hash), reporter.priv)
	if err != nil {
		e.t.Fatal(err)
	}
	ev.ReporterSig = sig
	return ev
}

// submit applies a report as the reporter's transaction in a block at intakeHeight.
func (e *intakeEnv) submit(ev evidence.Evidence, reporter *evidenceTestKey) error {
	return e.submitAt(ev, reporter, intakeHeight)
}

func (e *intakeEnv) submitAt(ev evidence.Evidence, reporter *evidenceTestKey, height uint64) error {
	e.t.Helper()
	addr := reporter.address()
	tx := signedSubmitEvidenceTx(e.t, ev, reporter, e.nonce[addr])
	e.sp.BeginBlock(height, time.Unix(1_700_800_000, 0).UTC().Add(time.Duration(height)*time.Second))
	err := e.sp.applySubmitEvidenceTransaction(tx, addr[:])
	if err == nil {
		e.nonce[addr]++
	}
	return err
}

func (e *intakeEnv) held() int {
	e.t.Helper()
	entries, err := e.manager.PotsoEvidenceIndex()
	if err != nil {
		e.t.Fatalf("evidence index: %v", err)
	}
	return len(entries)
}

func guardAt(h uint64) ValidatorRules { return ValidatorRules{EvidenceIntakeGuardActivationHeight: h} }

// Off (the shipped and live setting, and a height this run never reaches), every
// report the intake accepts today is still accepted. Pinned so only the switch moves it.
func TestEvidenceIntakeGuard_OffAcceptsWhatItAcceptsToday(t *testing.T) {
	for _, from := range []uint64{0, 1_000_000} {
		env := newIntakeEnv(t, guardAt(from))
		reporter := newEvidenceTestKey(t)
		env.lockOnly(reporter.address(), 5_000) // a hold, not staking stake
		throwaway := newEvidenceTestKey(t)
		for i, typ := range []evidence.Type{evidence.TypeDowntime, evidence.TypeInvalidBlockProposal, evidence.TypeEquivocation} {
			if err := env.submit(env.report(typ, throwaway, reporter, uint64(i)), reporter); err != nil {
				t.Fatalf("height %d, %s report refused with the guard off: %v", from, typ, err)
			}
		}
		if env.held() != 3 {
			t.Fatalf("height %d: %d records held, want 3", from, env.held())
		}
	}
}

// On, a report of a kind that does nothing when processed is refused, and writes nothing.
func TestEvidenceIntakeGuard_RefusesReportsThatDoNothing(t *testing.T) {
	env := newIntakeEnv(t, guardAt(100))
	reporterKey := newEvidenceTestKey(t)
	env.register(reporterKey.address(), 2_000)
	offender := newEvidenceTestKey(t)
	env.register(offender.address(), 2_000)
	for i, typ := range []evidence.Type{evidence.TypeDowntime, evidence.TypeInvalidBlockProposal} {
		err := env.submit(env.report(typ, offender, reporterKey, uint64(i)), reporterKey)
		if !errors.Is(err, ErrEvidenceTypeNotAccepted) {
			t.Fatalf("%s report: err = %v, want ErrEvidenceTypeNotAccepted", typ, err)
		}
	}
	if env.held() != 0 {
		t.Fatalf("%d records held after refused reports", env.held())
	}
}

// On, the offender of an equivocation report must hold real staking stake of at least
// the minimum, so a proof made against a throwaway key is refused.
func TestEvidenceIntakeGuard_OffenderMustBeBonded(t *testing.T) {
	cases := []struct {
		name  string
		setup func(env *intakeEnv, offender [20]byte)
		ok    bool
	}{
		{"throwaway key with no account", func(env *intakeEnv, o [20]byte) {}, false},
		{"one below the minimum", func(env *intakeEnv, o [20]byte) { env.register(o, 999) }, false},
		{"exactly the minimum", func(env *intakeEnv, o [20]byte) { env.register(o, 1_000) }, true},
		{"well above the minimum", func(env *intakeEnv, o [20]byte) { env.register(o, 5_000) }, true},
		{"locked by a hold, no staking stake", func(env *intakeEnv, o [20]byte) { env.lockOnly(o, 5_000) }, false},
		{"all unbonded, the unbond still reachable", func(env *intakeEnv, o [20]byte) {
			writeAccount(env.t, env.sp, o, &types.Account{BalanceZNHB: big.NewInt(10)})
			env.ownUnbond(o, 1_000)
		}, true},
		{"unbond below the minimum", func(env *intakeEnv, o [20]byte) {
			writeAccount(env.t, env.sp, o, &types.Account{BalanceZNHB: big.NewInt(10)})
			env.ownUnbond(o, 999)
		}, false},
	}
	for _, tc := range cases {
		env := newIntakeEnv(t, guardAt(100))
		reporter := newEvidenceTestKey(t)
		env.register(reporter.address(), 2_000)
		offender := newEvidenceTestKey(t)
		tc.setup(env, offender.address())
		err := env.submit(env.report(evidence.TypeEquivocation, offender, reporter, 0), reporter)
		if tc.ok && err != nil {
			t.Errorf("%s: refused: %v", tc.name, err)
		}
		if !tc.ok && !errors.Is(err, ErrEvidenceOffenderNotBonded) {
			t.Errorf("%s: err = %v, want ErrEvidenceOffenderNotBonded", tc.name, err)
		}
		if !tc.ok && env.held() != 0 {
			t.Errorf("%s: a refused report left %d records", tc.name, env.held())
		}
	}
}

// On, the reporter's bond is measured in staking stake. Locked ZNHB that is not
// stake (a point-of-sale hold, which can be voided at once) no longer counts.
func TestEvidenceIntakeGuard_ReporterBondIsStakingStake(t *testing.T) {
	cases := []struct {
		name  string
		setup func(env *intakeEnv, reporter [20]byte)
		ok    bool
	}{
		{"hold only", func(env *intakeEnv, r [20]byte) { env.lockOnly(r, 5_000) }, false},
		{"stake one below the minimum", func(env *intakeEnv, r [20]byte) { env.register(r, 999) }, false},
		{"stake exactly the minimum", func(env *intakeEnv, r [20]byte) { env.register(r, 1_000) }, true},
	}
	for _, tc := range cases {
		env := newIntakeEnv(t, guardAt(100))
		offender := newEvidenceTestKey(t)
		env.register(offender.address(), 2_000)
		reporter := newEvidenceTestKey(t)
		tc.setup(env, reporter.address())
		err := env.submit(env.report(evidence.TypeEquivocation, offender, reporter, 0), reporter)
		if tc.ok && err != nil {
			t.Errorf("%s: refused: %v", tc.name, err)
		}
		if !tc.ok && !errors.Is(err, ErrEvidenceReporterNotBonded) {
			t.Errorf("%s: err = %v, want ErrEvidenceReporterNotBonded", tc.name, err)
		}
	}
}

// The attack: 64 reporters (4 reports each) fill all 256 slots with reports that do
// nothing, and a genuine double-sign is then refused. With the guard on the fillers are
// refused and the genuine report goes in.
func TestEvidenceIntakeGuard_TheIndexCannotBeFilledWithInertReports(t *testing.T) {
	fill := func(env *intakeEnv) (accepted int) {
		for r := 0; r < evidence.MaxPendingRecords/evidence.MaxRecordsPerReporter; r++ {
			reporter := newEvidenceTestKey(t)
			env.register(reporter.address(), 1_000)
			throwaway := newEvidenceTestKey(t)
			for i := 0; i < evidence.MaxRecordsPerReporter; i++ {
				typ := evidence.TypeDowntime
				if i%2 == 1 {
					typ = evidence.TypeEquivocation
				}
				if err := env.submit(env.report(typ, throwaway, reporter, uint64(i)), reporter); err == nil {
					accepted++
				}
			}
		}
		return accepted
	}
	genuine := func(env *intakeEnv) error {
		reporter := newEvidenceTestKey(t)
		env.register(reporter.address(), 1_000)
		offender := newEvidenceTestKey(t)
		env.register(offender.address(), 2_000)
		return env.submit(env.report(evidence.TypeEquivocation, offender, reporter, 0), reporter)
	}

	off := newIntakeEnv(t, guardAt(0))
	if got := fill(off); got != evidence.MaxPendingRecords {
		t.Fatalf("setup: the fillers got %d records in with the guard off, want %d", got, evidence.MaxPendingRecords)
	}
	if err := genuine(off); !errors.Is(err, evidence.ErrIndexFull) {
		t.Fatalf("guard off: a genuine report with the index full: err = %v, want ErrIndexFull", err)
	}

	on := newIntakeEnv(t, guardAt(100))
	if got := fill(on); got != 0 {
		t.Fatalf("guard on: %d filler reports were accepted", got)
	}
	if err := genuine(on); err != nil {
		t.Fatalf("guard on: a genuine report was refused: %v", err)
	}
	if on.held() != 1 {
		t.Fatalf("guard on: %d records held, want only the genuine one", on.held())
	}
}

// The guard starts at its height and not before.
func TestEvidenceIntakeGuard_ActivationBoundary(t *testing.T) {
	for _, tc := range []struct {
		height uint64
		refuse bool
	}{{199, false}, {200, true}} {
		env := newIntakeEnv(t, guardAt(200))
		reporter := newEvidenceTestKey(t)
		env.lockOnly(reporter.address(), 5_000)
		throwaway := newEvidenceTestKey(t)
		err := env.submitAt(env.report(evidence.TypeDowntime, throwaway, reporter, 0), reporter, tc.height)
		if tc.refuse && err == nil {
			t.Fatalf("height %d: the guard did not apply", tc.height)
		}
		if !tc.refuse && err != nil {
			t.Fatalf("height %d: the guard applied early: %v", tc.height, err)
		}
	}
}

// Two nodes that take the same reports end with the same index.
func TestEvidenceIntakeGuard_TwoNodesAgree(t *testing.T) {
	build := func() []error {
		env := newIntakeEnv(t, guardAt(100))
		reporter := newEvidenceTestKey(t)
		env.register(reporter.address(), 2_000)
		bonded := newEvidenceTestKey(t)
		env.register(bonded.address(), 2_000)
		throwaway := newEvidenceTestKey(t)
		return []error{
			env.submit(env.report(evidence.TypeDowntime, bonded, reporter, 0), reporter),
			env.submit(env.report(evidence.TypeEquivocation, throwaway, reporter, 0), reporter),
			env.submit(env.report(evidence.TypeEquivocation, bonded, reporter, 0), reporter),
		}
	}
	a, b := build(), build()
	for i := range a {
		if (a[i] == nil) != (b[i] == nil) {
			t.Fatalf("report %d: nodes disagree: %v vs %v", i, a[i], b[i])
		}
	}
	if a[0] == nil || a[1] == nil || a[2] != nil {
		t.Fatalf("outcomes %v, want refused, refused, accepted", a)
	}
}

// How the block builder treats the two new refusals: a report of a kind that is never
// accepted can never become valid (dropped); an offender can still bond more, so that
// one is kept and offered again, like the reporter's bond.
func TestEvidenceIntakeGuard_Dispositions(t *testing.T) {
	if got := classifyProposalError(ErrEvidenceTypeNotAccepted); got != proposalDispositionPrune {
		t.Errorf("ErrEvidenceTypeNotAccepted: %v, want prune", got)
	}
	if got := classifyProposalError(ErrEvidenceOffenderNotBonded); got != proposalDispositionSkip {
		t.Errorf("ErrEvidenceOffenderNotBonded: %v, want skip", got)
	}
}
