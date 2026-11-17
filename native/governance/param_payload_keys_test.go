package governance

import (
	"math/big"
	"strings"
	"testing"
	"time"

	"nhbchain/core/types"
)

// activationUnix is the block time used by the tests below as the point from
// which ambiguous parameter keys are refused.
const activationUnix = uint64(1_800_000_000)

func newParamKeysEngine(t *testing.T, state *mockGovernanceState, from uint64, now time.Time) *Engine {
	t.Helper()
	engine := NewEngine()
	if state != nil {
		engine.SetState(state)
	}
	engine.SetNowFunc(func() time.Time { return now })
	engine.SetPolicy(ProposalPolicy{
		MinDepositWei:                    big.NewInt(0),
		VotingPeriodSeconds:              600,
		TimelockSeconds:                  120,
		AllowedParams:                    []string{"fees.baseFee", "potso.weights.AlphaStakeBps", ParamKeyMinimumValidatorStake},
		RejectAmbiguousParamKeysFromUnix: from,
	})
	return engine
}

// ambiguousPayloads lists payloads in which one parameter appears under two
// keys that differ only in surrounding whitespace, with different values. The
// whitespace variants cover a space, a tab, a newline and a no-break space.
var ambiguousPayloads = map[string]string{
	"space":         `{"fees.baseFee":25,"fees.baseFee ":30}`,
	"leading space": `{" fees.baseFee":25,"fees.baseFee":30}`,
	"tab":           `{"fees.baseFee":25,"fees.baseFee\t":30}`,
	"newline":       `{"fees.baseFee":25,"\nfees.baseFee":30}`,
	"no-break":      "{\"fees.baseFee\":25,\"fees.baseFee \":30}",
	"legacy":        `{"update":{"fees.baseFee":25,"fees.baseFee ":30}}`,
	"legacy params": `{"Params":{"fees.baseFee":25," fees.baseFee":30}}`,
	"three keys":    `{"fees.baseFee":25,"fees.baseFee ":25," fees.baseFee":30}`,
}

func TestValidateParamPayloadRejectsAmbiguousKeysOnceActive(t *testing.T) {
	t.Parallel()

	now := time.Unix(int64(activationUnix)+10, 0).UTC()
	engine := newParamKeysEngine(t, nil, activationUnix, now)

	for name, payload := range ambiguousPayloads {
		name, payload := name, payload
		t.Run(name, func(t *testing.T) {
			// The verdict and the message must not depend on map iteration
			// order, so repeat enough times to cover several orders.
			want := `governance: parameter "fees.baseFee" given more than once with different values`
			for i := 0; i < 200; i++ {
				validated, err := engine.validateParamPayload(payload)
				if err == nil {
					t.Fatalf("run %d: expected rejection, stored %v", i, validated)
				}
				if err.Error() != want {
					t.Fatalf("run %d: unexpected error %q", i, err.Error())
				}
			}
		})
	}
}

func TestValidateParamPayloadAmbiguousKeysActivationBoundary(t *testing.T) {
	t.Parallel()

	payload := ambiguousPayloads["space"]
	activation := int64(activationUnix)

	// Before the activation time, and with no activation time at all, the
	// payload is admitted as it always was.
	for name, tc := range map[string]struct {
		from uint64
		now  int64
	}{
		"unset":                    {from: 0, now: activation + 1_000},
		"one second before":        {from: activationUnix, now: activation - 1},
		"long before":              {from: activationUnix, now: activation - 100_000},
		"unset at time zero":       {from: 0, now: 0},
		"activation in the future": {from: activationUnix + 1_000_000, now: activation},
		"negative block time":      {from: activationUnix, now: -5},
		"unset with negative now":  {from: 0, now: -5},
	} {
		engine := newParamKeysEngine(t, nil, tc.from, time.Unix(tc.now, 0).UTC())
		validated, err := engine.validateParamPayload(payload)
		if err != nil {
			t.Fatalf("%s: expected admission before activation, got %v", name, err)
		}
		got, ok := validated["fees.baseFee"]
		if !ok || (string(got) != "25" && string(got) != "30") {
			t.Fatalf("%s: unexpected validated value %q", name, string(got))
		}
	}

	// From the activation time onward the payload is refused.
	for _, now := range []int64{activation, activation + 1, activation + 10_000_000} {
		engine := newParamKeysEngine(t, nil, activationUnix, time.Unix(now, 0).UTC())
		if _, err := engine.validateParamPayload(payload); err == nil || !strings.Contains(err.Error(), "more than once") {
			t.Fatalf("now=%d: expected rejection, got %v", now, err)
		}
	}
}

func TestValidateParamPayloadAcceptsEquivalentRepeatsOnceActive(t *testing.T) {
	t.Parallel()

	now := time.Unix(int64(activationUnix)+10, 0).UTC()
	engine := newParamKeysEngine(t, nil, activationUnix, now)

	// The same bytes under both keys store the same value whichever is kept,
	// so these were deterministic before and stay accepted.
	payloads := []string{
		`{"fees.baseFee":25,"fees.baseFee ":25}`,
		"{\"fees.baseFee\":25,\" fees.baseFee\":25}",
		`{"update":{"fees.baseFee":25,"fees.baseFee\t":25}}`,
	}
	for _, payload := range payloads {
		for i := 0; i < 50; i++ {
			validated, err := engine.validateParamPayload(payload)
			if err != nil {
				t.Fatalf("payload %q: unexpected rejection: %v", payload, err)
			}
			if len(validated) != 1 || string(validated["fees.baseFee"]) != "25" {
				t.Fatalf("payload %q: unexpected result %v", payload, validated)
			}
		}
	}

	// An exact repeat of the very same key never reaches the engine as two
	// entries; the decoder keeps the last one.
	validated, err := engine.validateParamPayload(`{"fees.baseFee":25,"fees.baseFee":30}`)
	if err != nil {
		t.Fatalf("exact repeat: %v", err)
	}
	if string(validated["fees.baseFee"]) != "30" {
		t.Fatalf("exact repeat: unexpected value %q", string(validated["fees.baseFee"]))
	}
}

func TestValidateParamPayloadMultiKeyUnchangedOnceActive(t *testing.T) {
	t.Parallel()

	now := time.Unix(int64(activationUnix)+10, 0).UTC()
	payload := `{"fees.baseFee":25,"potso.weights.AlphaStakeBps":7500,"staking.minimumValidatorStake":3500}`

	active := newParamKeysEngine(t, nil, activationUnix, now)
	legacy := newParamKeysEngine(t, nil, 0, now)

	got, err := active.validateParamPayload(payload)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	want, err := legacy.validateParamPayload(payload)
	if err != nil {
		t.Fatalf("legacy: %v", err)
	}
	if len(got) != len(want) || len(got) != 3 {
		t.Fatalf("unexpected result sizes: active=%d legacy=%d", len(got), len(want))
	}
	for key, value := range want {
		if string(got[key]) != string(value) {
			t.Fatalf("key %s: active %q, legacy %q", key, string(got[key]), string(value))
		}
	}
}

func TestValidateParamPayloadErrorIsStableOnceActive(t *testing.T) {
	t.Parallel()

	now := time.Unix(int64(activationUnix)+10, 0).UTC()
	engine := newParamKeysEngine(t, nil, activationUnix, now)

	// Two unknown keys next to a valid one: the reported key must be the same
	// on every run, so every validator returns the same error.
	payload := `{"fees.baseFee":25,"zeta.unknown":1,"alpha.unknown":2}`
	for i := 0; i < 200; i++ {
		_, err := engine.validateParamPayload(payload)
		if err == nil {
			t.Fatalf("run %d: expected rejection", i)
		}
		if want := `governance: parameter "alpha.unknown" not in allow-list`; err.Error() != want {
			t.Fatalf("run %d: unexpected error %q", i, err.Error())
		}
	}
}

func TestSubmitProposalRejectsAmbiguousKeysOnceActive(t *testing.T) {
	var proposer [20]byte
	proposer[3] = 9
	newState := func() *mockGovernanceState {
		return newMockGovernanceState(map[[20]byte]*types.Account{
			proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
		})
	}
	now := time.Unix(int64(activationUnix)+10, 0).UTC()

	state := newState()
	engine := newParamKeysEngine(t, state, activationUnix, now)
	for name, payload := range ambiguousPayloads {
		_, err := engine.SubmitProposal(proposer, ProposalKindParamUpdate, payload, big.NewInt(0))
		if err == nil || !strings.Contains(err.Error(), "given more than once with different values") {
			t.Fatalf("%s: expected rejection, got %v", name, err)
		}
	}
	if len(state.proposals) != 0 {
		t.Fatalf("expected no proposal stored, got %d", len(state.proposals))
	}

	// With the check off the same payload is still admitted, which is what
	// keeps already-committed blocks executing as they did.
	legacyState := newState()
	legacy := newParamKeysEngine(t, legacyState, 0, now)
	if _, err := legacy.SubmitProposal(proposer, ProposalKindParamUpdate, ambiguousPayloads["space"], big.NewInt(0)); err != nil {
		t.Fatalf("check off: expected admission, got %v", err)
	}
	if len(legacyState.proposals) != 1 {
		t.Fatalf("check off: expected one stored proposal, got %d", len(legacyState.proposals))
	}
}

func TestExecuteRefusesAmbiguousKeysOnceActive(t *testing.T) {
	now := time.Unix(int64(activationUnix)+10, 0).UTC()
	state := newMockGovernanceState(nil)
	if err := state.GovernancePutProposal(&Proposal{
		ID:             4,
		Status:         ProposalStatusPassed,
		TimelockEnd:    now.Add(-time.Second),
		Target:         ProposalKindParamUpdate,
		ProposedChange: ambiguousPayloads["tab"],
		Queued:         true,
	}); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}
	engine := newParamKeysEngine(t, state, activationUnix, now)

	for i := 0; i < 50; i++ {
		err := engine.Execute(4)
		if err == nil || !strings.Contains(err.Error(), "given more than once with different values") {
			t.Fatalf("run %d: expected rejection, got %v", i, err)
		}
	}
	if _, ok := state.ParamStoreGet("fees.baseFee"); ok {
		t.Fatalf("expected no parameter written")
	}
	stored, ok, err := state.GovernanceGetProposal(4)
	if err != nil || !ok {
		t.Fatalf("reload proposal: ok=%v err=%v", ok, err)
	}
	if stored.Status != ProposalStatusPassed {
		t.Fatalf("expected proposal to stay passed, got %s", stored.Status.StatusString())
	}
}

// A payload admitted before the activation time and executed after it is
// refused by Execute as well, so no validator ever stores a value picked by
// map order once the check is on.
func TestExecuteRefusesAmbiguousProposalAdmittedBeforeActivation(t *testing.T) {
	var proposer [20]byte
	proposer[4] = 2
	state := newMockGovernanceState(map[[20]byte]*types.Account{
		proposer: &types.Account{BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})
	before := time.Unix(int64(activationUnix)-1_000, 0).UTC()
	admit := newParamKeysEngine(t, state, activationUnix, before)
	id, err := admit.SubmitProposal(proposer, ProposalKindParamUpdate, ambiguousPayloads["space"], big.NewInt(0))
	if err != nil {
		t.Fatalf("admission before activation: %v", err)
	}

	proposal, ok, err := state.GovernanceGetProposal(id)
	if err != nil || !ok {
		t.Fatalf("load proposal: ok=%v err=%v", ok, err)
	}
	proposal.Status = ProposalStatusPassed
	proposal.Queued = true
	proposal.TimelockEnd = time.Unix(int64(activationUnix), 0).UTC()
	if err := state.GovernancePutProposal(proposal); err != nil {
		t.Fatalf("update proposal: %v", err)
	}

	after := time.Unix(int64(activationUnix)+1_000, 0).UTC()
	execute := newParamKeysEngine(t, state, activationUnix, after)
	if err := execute.Execute(id); err == nil || !strings.Contains(err.Error(), "given more than once with different values") {
		t.Fatalf("expected rejection at execution, got %v", err)
	}
	if _, ok := state.ParamStoreGet("fees.baseFee"); ok {
		t.Fatalf("expected no parameter written")
	}
}
