package governance

import (
	"encoding/json"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"nhbchain/core/types"
)

// These tests cover the buyback price guard (PL-EXT-28): from a configured height
// governance refuses a buyback discount or safety margin of 10000 basis points,
// which makes the highest buyback price exactly zero. Before the height, and with
// the setting at zero, it behaves exactly as it did.

const guardTestFromHeight = 100

type guardFixture struct {
	t        *testing.T
	state    *mockGovernanceState
	engine   *Engine
	proposer [20]byte
	now      time.Time
	height   uint64
}

// newGuardFixture builds an engine whose guard starts at fromHeight (zero is off),
// reading the current height from the fixture so a test can move it.
func newGuardFixture(t *testing.T, fromHeight uint64, withHeightFn bool) *guardFixture {
	t.Helper()
	f := &guardFixture{t: t, now: time.Unix(1_700_300_000, 0).UTC()}
	f.proposer[3] = 21
	f.state = newMockGovernanceState(map[[20]byte]*types.Account{
		f.proposer: {BalanceZNHB: big.NewInt(100000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})
	f.engine = NewEngine()
	f.engine.SetState(f.state)
	f.engine.SetPolicy(ProposalPolicy{
		MinDepositWei:       big.NewInt(50),
		VotingPeriodSeconds: 60,
		TimelockSeconds:     10,
	})
	f.engine.SetNowFunc(func() time.Time { return f.now })
	if withHeightFn {
		f.engine.SetBuybackPriceGuard(fromHeight, func() uint64 { return f.height })
	} else {
		f.engine.SetBuybackPriceGuard(fromHeight, nil)
	}
	return f
}

func buybackPayload(feeShare, discount, margin uint32) string {
	return fmt.Sprintf(`{"feeShareBps":%d,"discountBps":%d,"safetyMarginBps":%d}`, feeShare, discount, margin)
}

func (f *guardFixture) submit(payload string) (uint64, error) {
	f.t.Helper()
	return f.engine.SubmitProposal(f.proposer, ProposalKindBuybackParams, payload, big.NewInt(75))
}

// passAndQueue forces a stored proposal through the vote, as the other tests of
// this package do, and queues it.
func (f *guardFixture) passAndQueue(id uint64) {
	f.t.Helper()
	proposal := f.state.proposals[id]
	proposal.Status = ProposalStatusPassed
	if err := f.engine.QueueExecution(id); err != nil {
		f.t.Fatalf("queue proposal %d: %v", id, err)
	}
	f.state.proposals[id].TimelockEnd = f.now.Add(-time.Second)
}

func TestBuybackPriceGuardSubmission(t *testing.T) {
	type payloadCase struct {
		name    string
		payload string
		refused bool // true when a guard that is on must refuse it
	}
	payloads := []payloadCase{
		{"discount at 10000", buybackPayload(2000, 10000, 0), true},
		{"safety margin at 10000", buybackPayload(2000, 0, 10000), true},
		{"both at 10000", buybackPayload(2000, 10000, 10000), true},
		{"discount at 10000 with the other fields at their top", buybackPayload(10000, 10000, 9999), true},
		{"discount at 9999", buybackPayload(2000, 9999, 0), false},
		{"safety margin at 9999", buybackPayload(2000, 0, 9999), false},
		{"both at 9999", buybackPayload(2000, 9999, 9999), false},
		{"fee share alone at 10000", buybackPayload(10000, 0, 0), false},
		{"all zero", buybackPayload(0, 0, 0), false},
		{"ordinary values", buybackPayload(3000, 750, 250), false},
	}
	settings := []struct {
		name         string
		fromHeight   uint64
		withHeightFn bool
		height       uint64
		guardOn      bool
	}{
		{"setting at zero (off)", 0, true, 500, false},
		{"setting at zero, height zero", 0, true, 0, false},
		{"height before the activation height", guardTestFromHeight, true, guardTestFromHeight - 1, false},
		{"height equal to the activation height", guardTestFromHeight, true, guardTestFromHeight, true},
		{"height after the activation height", guardTestFromHeight, true, guardTestFromHeight + 400, true},
		{"no height function configured", guardTestFromHeight, false, 500, false},
	}
	for _, setting := range settings {
		for _, pc := range payloads {
			setting, pc := setting, pc
			t.Run(setting.name+"/"+pc.name, func(t *testing.T) {
				f := newGuardFixture(t, setting.fromHeight, setting.withHeightFn)
				f.height = setting.height
				_, err := f.submit(pc.payload)
				wantRefused := setting.guardOn && pc.refused
				if wantRefused && err == nil {
					t.Fatalf("submission was accepted, want it refused (guard on, %s)", pc.name)
				}
				if !wantRefused && err != nil {
					t.Fatalf("submission was refused: %v (guard on=%v, %s)", err, setting.guardOn, pc.name)
				}
				if wantRefused && !strings.Contains(err.Error(), "highest buyback price") {
					t.Fatalf("refusal does not say why: %v", err)
				}
			})
		}
	}
}

// A proposal stored before the height is not looked at again, but executing it
// from the height refuses the write and leaves every parameter as it was. The
// three fields are written all or none.
func TestBuybackPriceGuardExecutionOfAProposalStoredBeforeTheHeight(t *testing.T) {
	cases := []struct {
		name        string
		payload     string
		execHeight  uint64
		fromHeight  uint64
		wantWritten bool
	}{
		{"discount 10000, executed before the height", buybackPayload(3000, 10000, 250), guardTestFromHeight - 1, guardTestFromHeight, true},
		{"discount 10000, executed at the height", buybackPayload(3000, 10000, 250), guardTestFromHeight, guardTestFromHeight, false},
		{"discount 10000, executed after the height", buybackPayload(3000, 10000, 250), guardTestFromHeight + 7, guardTestFromHeight, false},
		{"margin 10000, executed at the height", buybackPayload(3000, 750, 10000), guardTestFromHeight, guardTestFromHeight, false},
		{"both 10000, executed at the height", buybackPayload(3000, 10000, 10000), guardTestFromHeight, guardTestFromHeight, false},
		{"discount 10000, setting at zero", buybackPayload(3000, 10000, 250), guardTestFromHeight + 7, 0, true},
		{"fine values, executed after the height", buybackPayload(3000, 9999, 9999), guardTestFromHeight + 7, guardTestFromHeight, true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			// Stored while the guard was not yet in force.
			f := newGuardFixture(t, tc.fromHeight, true)
			f.height = 1
			id, err := f.submit(tc.payload)
			if err != nil {
				t.Fatalf("submit before the height: %v", err)
			}
			f.passAndQueue(id)
			f.now = f.now.Add(time.Minute)
			f.height = tc.execHeight
			err = f.engine.Execute(id)
			if tc.wantWritten {
				if err != nil {
					t.Fatalf("execute: %v", err)
				}
				for _, key := range []string{ParamKeyBuybackFeeShareBps, ParamKeyBuybackDiscountBps, ParamKeyBuybackSafetyMarginBps} {
					if _, ok := f.state.params[key]; !ok {
						t.Fatalf("param %s not written", key)
					}
				}
				return
			}
			if err == nil {
				t.Fatalf("execute succeeded, want the write refused")
			}
			if !strings.Contains(err.Error(), "highest buyback price") {
				t.Fatalf("refusal does not say why: %v", err)
			}
			if len(f.state.params) != 0 {
				t.Fatalf("a refused execution left parameters behind: %v", f.state.params)
			}
			if got := f.state.proposals[id].Status; got == ProposalStatusExecuted {
				t.Fatalf("a refused execution marked the proposal executed")
			}
		})
	}
}

// Every write the engine makes to the param store goes through paramStoreSet, so a
// route that is added later (or reached by a key with spaces around it) meets the
// same refusal.
func TestBuybackPriceGuardParamStoreWrite(t *testing.T) {
	keys := []string{
		ParamKeyBuybackDiscountBps,
		ParamKeyBuybackSafetyMarginBps,
		" " + ParamKeyBuybackDiscountBps,
		ParamKeyBuybackSafetyMarginBps + "\t",
	}
	for _, key := range keys {
		for _, tc := range []struct {
			value   string
			refused bool
		}{
			{"10000", true},
			{"10001", true},
			{"4294967295", true},
			{"010000", true},
			{"9999", false},
			{"0", false},
			// the reader of the stored value ignores anything it cannot parse as an
			// unsigned 32-bit number, so these cannot make the price zero
			{"4294967296", false},
			{"10000.0", false},
			{"-1", false},
			{"", false},
		} {
			key, tc := key, tc
			t.Run(fmt.Sprintf("key %q value %q", key, tc.value), func(t *testing.T) {
				f := newGuardFixture(t, guardTestFromHeight, true)
				f.height = guardTestFromHeight
				err := f.engine.paramStoreSet(key, []byte(tc.value))
				if tc.refused && err == nil {
					t.Fatalf("write of %q was accepted", tc.value)
				}
				if !tc.refused && err != nil {
					t.Fatalf("write of %q was refused: %v", tc.value, err)
				}
				if tc.refused && len(f.state.params) != 0 {
					t.Fatalf("a refused write left a value behind: %v", f.state.params)
				}
				if !tc.refused && len(f.state.params) != 1 {
					t.Fatalf("an accepted write stored %d values, want 1", len(f.state.params))
				}
			})
		}
	}
	t.Run("guard off writes everything as before", func(t *testing.T) {
		f := newGuardFixture(t, 0, true)
		f.height = 5000
		for _, v := range []string{"10000", "20000", "7"} {
			if err := f.engine.paramStoreSet(ParamKeyBuybackDiscountBps, []byte(v)); err != nil {
				t.Fatalf("write of %q refused with the guard off: %v", v, err)
			}
		}
	})
	t.Run("other keys are never judged", func(t *testing.T) {
		f := newGuardFixture(t, guardTestFromHeight, true)
		f.height = guardTestFromHeight + 1
		for _, key := range []string{ParamKeyBuybackFeeShareBps, ParamKeyMinimumValidatorStake, "fees.baseFee"} {
			if err := f.engine.paramStoreSet(key, []byte("10000")); err != nil {
				t.Fatalf("write of %s refused: %v", key, err)
			}
		}
	})
}

// The generic parameter update proposal cannot reach the two buyback keys at all:
// a key needs an allow-list entry and a validation rule, and neither of the two
// keys has a rule. The test records that, with the guard off and on, so a rule
// added later is a visible change; paramStoreSet is then what stops it.
func TestBuybackKeysAreNotReachableThroughTheGenericParameterUpdate(t *testing.T) {
	for _, fromHeight := range []uint64{0, guardTestFromHeight} {
		for _, key := range []string{ParamKeyBuybackDiscountBps, ParamKeyBuybackSafetyMarginBps} {
			f := newGuardFixture(t, fromHeight, true)
			f.height = guardTestFromHeight + 1
			f.engine.SetPolicy(ProposalPolicy{
				MinDepositWei:       big.NewInt(50),
				VotingPeriodSeconds: 60,
				TimelockSeconds:     10,
				AllowedParams:       []string{key},
			})
			payload := fmt.Sprintf(`{%q: 10000}`, key)
			_, err := f.engine.SubmitProposal(f.proposer, ProposalKindParamUpdate, payload, big.NewInt(75))
			if err == nil {
				t.Fatalf("a generic update of %s was accepted (guard from %d)", key, fromHeight)
			}
			if !strings.Contains(err.Error(), "missing validation rule") {
				t.Fatalf("generic update of %s refused for another reason: %v", key, err)
			}
		}
	}
}

// If a validation rule for a buyback key were ever added, the generic parameter
// update would reach the key. The test gives the two keys a rule that accepts
// anything and checks that executing such an update meets the same refusal as a
// buyback parameters proposal, all keys of the payload written or none.
func TestBuybackKeysReachedByAGenericUpdateMeetTheSameRefusal(t *testing.T) {
	acceptAnything := func(json.RawMessage) error { return nil }
	cases := []struct {
		name    string
		fromH   uint64
		height  uint64
		payload string
		refused bool
	}{
		{"discount at 10000, guard on", guardTestFromHeight, guardTestFromHeight, `{"aaa.first":1,"buyback.discountBps":10000,"fees.baseFee":7}`, true},
		{"margin at 10000, guard on", guardTestFromHeight, guardTestFromHeight + 3, `{"buyback.safetyMarginBps":10000}`, true},
		{"discount at 9999, guard on", guardTestFromHeight, guardTestFromHeight, `{"buyback.discountBps":9999}`, false},
		{"discount at 10000, before the height", guardTestFromHeight, guardTestFromHeight - 1, `{"buyback.discountBps":10000}`, false},
		{"discount at 10000, guard off", 0, guardTestFromHeight, `{"buyback.discountBps":10000}`, false},
		// a quoted number is not read by the reader of the stored value, which ignores it
		{"discount quoted, guard on", guardTestFromHeight, guardTestFromHeight, `{"buyback.discountBps":"10000"}`, false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			f := newGuardFixture(t, tc.fromH, true)
			f.height = tc.height
			f.engine.SetPolicy(ProposalPolicy{
				MinDepositWei:       big.NewInt(50),
				VotingPeriodSeconds: 60,
				TimelockSeconds:     10,
				AllowedParams:       []string{"aaa.first", ParamKeyBuybackDiscountBps, ParamKeyBuybackSafetyMarginBps, "fees.baseFee"},
			})
			// "aaa.first" sorts before the buyback keys: it is written first unless
			// every key is checked before any is written
			f.engine.paramValidators["aaa.first"] = acceptAnything
			f.engine.paramValidators[ParamKeyBuybackDiscountBps] = acceptAnything
			f.engine.paramValidators[ParamKeyBuybackSafetyMarginBps] = acceptAnything
			_, err := f.engine.applyParamUpdates(tc.payload)
			if tc.refused {
				if err == nil || !strings.Contains(err.Error(), "highest buyback price") {
					t.Fatalf("generic update was not refused as it must be: %v", err)
				}
				if len(f.state.params) != 0 {
					t.Fatalf("a refused update left values behind: %v", f.state.params)
				}
				return
			}
			if err != nil {
				t.Fatalf("generic update refused: %v", err)
			}
		})
	}
}
