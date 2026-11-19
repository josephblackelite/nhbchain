package governance

import (
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"nhbchain/core/types"
)

// feeParamsState is the mock governance state with the three-value parameter read the
// production state has, so the engine can read the schedule record it wrote before.
type feeParamsState struct{ *mockGovernanceState }

func (s feeParamsState) ParamStoreGet(name string) ([]byte, bool, error) {
	v, ok := s.mockGovernanceState.ParamStoreGet(name)
	return v, ok, nil
}

const feeAllowance1000 = "1000000000000000000000"

type feeFixture struct {
	t        *testing.T
	state    *mockGovernanceState
	engine   *Engine
	proposer [20]byte
	now      time.Time
}

func newFeeFixture(t *testing.T, host FeeScheduleHost) *feeFixture {
	t.Helper()
	f := &feeFixture{t: t, now: time.Unix(1_800_000_000, 0).UTC()}
	f.proposer[3] = 7
	f.state = newMockGovernanceState(map[[20]byte]*types.Account{
		f.proposer: {BalanceZNHB: big.NewInt(1000), BalanceNHB: big.NewInt(0), Stake: big.NewInt(0)},
	})
	f.engine = NewEngine()
	f.engine.SetState(feeParamsState{f.state})
	f.engine.SetPolicy(ProposalPolicy{MinDepositWei: big.NewInt(50), VotingPeriodSeconds: 60, TimelockSeconds: 10})
	f.engine.SetNowFunc(func() time.Time { return f.now })
	f.engine.SetFeeSchedule(host)
	return f
}

// run takes one proposal through submit, pass, queue and execute at the given block height.
func (f *feeFixture) run(payload string, height uint64) (uint64, error) {
	f.t.Helper()
	id, err := f.engine.SubmitProposal(f.proposer, ProposalKindFeeParams, payload, big.NewInt(75))
	if err != nil {
		return 0, err
	}
	f.state.proposals[id].Status = ProposalStatusPassed
	if err := f.engine.QueueExecution(id); err != nil {
		f.t.Fatalf("queue %d: %v", id, err)
	}
	f.state.proposals[id].TimelockEnd = f.now.Add(-time.Second)
	host := f.engine.feeSchedule
	host.Height = height
	f.engine.SetFeeSchedule(host)
	return id, f.engine.Execute(id)
}

func (f *feeFixture) record() *FeeScheduleRecord {
	f.t.Helper()
	raw, ok := f.state.ParamStoreGet(ParamKeyTransferFeeSchedule)
	if !ok {
		return nil
	}
	record, err := ParseFeeScheduleRecord(raw)
	if err != nil {
		f.t.Fatalf("stored record does not parse: %v (%s)", err, raw)
	}
	return record
}

func feePayload(nhb, znhb uint32, allowance string, extra string) string {
	b, _ := json.Marshal(map[string]interface{}{"nhbFeeBps": nhb, "znhbFeeBps": znhb, "freeAllowanceWei": allowance})
	s := string(b)
	if extra != "" {
		s = s[:len(s)-1] + "," + extra + "}"
	}
	return s
}

func TestFeeParamsPayloadAcceptsTheBoundaries(t *testing.T) {
	for name, payload := range map[string]string{
		"live values":          feePayload(20, 10, feeAllowance1000, ""),
		"both rates zero":      feePayload(0, 0, feeAllowance1000, ""),
		"both at the ceiling":  feePayload(100, 100, feeAllowance1000, ""),
		"allowance at the cap": feePayload(20, 10, MaxFreeAllowanceWei().String(), ""),
		"allowance of one wei": feePayload(20, 10, "1", ""),
		"explicit zero":        feePayload(20, 10, "0", `"allowZeroAllowance":true`),
		"with a memo":          feePayload(20, 10, feeAllowance1000, `"memo":"flat 0.20%"`),
		"padded with space":    "  " + feePayload(20, 10, feeAllowance1000, "") + "\n",
	} {
		if _, err := parseFeeParamsPayload(payload); err != nil {
			t.Errorf("%s: refused: %v (%s)", name, err, payload)
		}
	}
}

func TestFeeParamsPayloadRefusesNonsense(t *testing.T) {
	for name, tc := range map[string]struct{ payload, want string }{
		"NHB rate above the ceiling":        {feePayload(101, 10, feeAllowance1000, ""), "above the ceiling"},
		"ZNHB rate above the ceiling":       {feePayload(20, 101, feeAllowance1000, ""), "above the ceiling"},
		"both far above":                    {feePayload(10000, 10000, feeAllowance1000, ""), "above the ceiling"},
		"allowance above the cap":           {feePayload(20, 10, new(big.Int).Add(MaxFreeAllowanceWei(), big.NewInt(1)).String(), ""), "above the cap"},
		"allowance zero without the flag":   {feePayload(20, 10, "0", ""), "allowZeroAllowance"},
		"flag with a positive allowance":    {feePayload(20, 10, feeAllowance1000, `"allowZeroAllowance":true`), "is not zero"},
		"flag false with zero":              {feePayload(20, 10, "0", `"allowZeroAllowance":false`), "allowZeroAllowance"},
		"allowance with a sign":             {feePayload(20, 10, "+1000", ""), "digits only"},
		"negative allowance":                {feePayload(20, 10, "-1", ""), "digits only"},
		"allowance with an exponent":        {feePayload(20, 10, "1000e18", ""), "digits only"},
		"allowance with a decimal point":    {feePayload(20, 10, "1.5", ""), "digits only"},
		"allowance with a space":            {feePayload(20, 10, "1000 ", ""), "digits only"},
		"allowance with a leading zero":     {feePayload(20, 10, "01000", ""), "leading zero"},
		"allowance empty":                   {feePayload(20, 10, "", ""), "required"},
		"allowance hex":                     {feePayload(20, 10, "0x10", ""), "digits only"},
		"memo too long":                     {feePayload(20, 10, feeAllowance1000, `"memo":"`+strings.Repeat("x", 257)+`"`), "memo"},
		"missing nhb rate":                  {`{"znhbFeeBps":10,"freeAllowanceWei":"1000"}`, "nhbFeeBps is required"},
		"missing znhb rate":                 {`{"nhbFeeBps":20,"freeAllowanceWei":"1000"}`, "znhbFeeBps is required"},
		"missing allowance":                 {`{"nhbFeeBps":20,"znhbFeeBps":10}`, "freeAllowanceWei is required"},
		"empty object":                      {`{}`, "required"},
		"null rate":                         {`{"nhbFeeBps":null,"znhbFeeBps":10,"freeAllowanceWei":"1000"}`, "must not be null"},
		"null allowance":                    {`{"nhbFeeBps":20,"znhbFeeBps":10,"freeAllowanceWei":null}`, "must not be null"},
		"rate as a string":                  {`{"nhbFeeBps":"20","znhbFeeBps":10,"freeAllowanceWei":"1000"}`, "whole number"},
		"rate with a fraction":              {`{"nhbFeeBps":20.5,"znhbFeeBps":10,"freeAllowanceWei":"1000"}`, "whole number"},
		"rate in exponent form":             {`{"nhbFeeBps":2e1,"znhbFeeBps":10,"freeAllowanceWei":"1000"}`, "whole number"},
		"negative rate":                     {`{"nhbFeeBps":-1,"znhbFeeBps":10,"freeAllowanceWei":"1000"}`, "whole number"},
		"rate beyond 32 bits":               {`{"nhbFeeBps":4294967296,"znhbFeeBps":10,"freeAllowanceWei":"1000"}`, "whole number"},
		"allowance as a number":             {`{"nhbFeeBps":20,"znhbFeeBps":10,"freeAllowanceWei":1000}`, "string of digits"},
		"unknown field":                     {feePayload(20, 10, feeAllowance1000, `"domainFeeBps":150`), "unknown field"},
		"key in the wrong case":             {`{"NHBFeeBps":20,"znhbFeeBps":10,"freeAllowanceWei":"1000"}`, "unknown field"},
		"repeated key":                      {`{"nhbFeeBps":20,"nhbFeeBps":30,"znhbFeeBps":10,"freeAllowanceWei":"1000"}`, "more than once"},
		"repeated key with equal values":    {`{"nhbFeeBps":20,"nhbFeeBps":20,"znhbFeeBps":10,"freeAllowanceWei":"1000"}`, "more than once"},
		"trailing data":                     {feePayload(20, 10, feeAllowance1000, "") + ` {}`, "trailing"},
		"a JSON array":                      {`[20,10]`, "object"},
		"a bare string":                     {`"20"`, "object"},
		"not JSON":                          {`nhbFeeBps=20`, "invalid payload"},
		"truncated":                         {`{"nhbFeeBps":20,`, "invalid payload"},
		"flag as a string":                  {feePayload(20, 10, "0", `"allowZeroAllowance":"true"`), "true or false"},
		"memo as a number":                  {feePayload(20, 10, feeAllowance1000, `"memo":5`), "memo"},
		"the lending-style rate field name": {`{"rateBps":20,"znhbFeeBps":10,"freeAllowanceWei":"1000"}`, "unknown field"},
	} {
		_, err := parseFeeParamsPayload(tc.payload)
		if err == nil {
			t.Errorf("%s: accepted %s", name, tc.payload)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not mention %q", name, err.Error(), tc.want)
		}
	}
}

// The kind does not exist below the activation height: submission and execution are refused
// as an unknown kind is, and nothing is written.
func TestFeeParamsKindIsUnavailableUntilTheEngineIsToldItIsActive(t *testing.T) {
	f := newFeeFixture(t, FeeScheduleHost{})
	_, err := f.engine.SubmitProposal(f.proposer, ProposalKindFeeParams, feePayload(20, 10, feeAllowance1000, ""), big.NewInt(75))
	if err == nil || !strings.Contains(err.Error(), "unsupported proposal kind") {
		t.Fatalf("submission with the kind unavailable: err = %v, want an unsupported kind error", err)
	}
	if len(f.state.proposals) != 0 {
		t.Fatalf("a refused submission stored %d proposals", len(f.state.proposals))
	}

	// A proposal that is already in the state (written while the kind was available)
	// is refused at execution when the engine says it is not.
	g := newFeeFixture(t, FeeScheduleHost{Enabled: true, Height: 10, DelayBlocks: 100})
	id, err := g.engine.SubmitProposal(g.proposer, ProposalKindFeeParams, feePayload(20, 10, feeAllowance1000, ""), big.NewInt(75))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	g.state.proposals[id].Status = ProposalStatusPassed
	if err := g.engine.QueueExecution(id); err != nil {
		t.Fatalf("queue: %v", err)
	}
	g.state.proposals[id].TimelockEnd = g.now.Add(-time.Second)
	g.engine.SetFeeSchedule(FeeScheduleHost{})
	if err := g.engine.Execute(id); err == nil || !strings.Contains(err.Error(), "unsupported target") {
		t.Fatalf("execution with the kind unavailable: err = %v, want an unsupported target error", err)
	}
	if _, ok := g.state.ParamStoreGet(ParamKeyTransferFeeSchedule); ok {
		t.Fatalf("a refused execution wrote the schedule")
	}
}

func TestFeeParamsExecutionStoresAPendingScheduleAtTheDelayedHeight(t *testing.T) {
	f := newFeeFixture(t, FeeScheduleHost{Enabled: true, DelayBlocks: 500})
	id, err := f.run(feePayload(25, 15, "2000000000000000000000", `"memo":"raise"`), 1000)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	record := f.record()
	if record == nil || record.Pending == nil {
		t.Fatalf("no pending schedule stored: %+v", record)
	}
	if record.Active != nil {
		t.Fatalf("an active schedule appeared before the pending one took effect: %+v", record.Active)
	}
	want := ScheduledFeeValues{FeeScheduleValues: FeeScheduleValues{NHBFeeBps: 25, ZNHBFeeBps: 15, FreeAllowanceWei: "2000000000000000000000"}, EffectiveHeight: 1500, ProposalID: id}
	if *record.Pending != want {
		t.Fatalf("pending = %+v, want %+v", *record.Pending, want)
	}
	if got := record.InForce(1499); got != nil {
		t.Fatalf("one block before the effective height the schedule is already in force: %+v", got)
	}
	if got := record.InForce(1500); got == nil || *got != want {
		t.Fatalf("at the effective height the schedule is not in force: %+v", got)
	}
	if got := record.InForce(9_000_000); got == nil || *got != want {
		t.Fatalf("long after the effective height the schedule is not in force: %+v", got)
	}
	if got := record.InForce(0); got != nil {
		t.Fatalf("height 0 sees the schedule: %+v", got)
	}
}

// The stored bytes are part of consensus state: pin them.
func TestFeeParamsStoredRecordIsByteStable(t *testing.T) {
	f := newFeeFixture(t, FeeScheduleHost{Enabled: true, DelayBlocks: 100})
	if _, err := f.run(feePayload(20, 10, feeAllowance1000, ""), 2_700_000); err != nil {
		t.Fatalf("execute: %v", err)
	}
	raw, _ := f.state.ParamStoreGet(ParamKeyTransferFeeSchedule)
	const want = `{"pending":{"nhbFeeBps":20,"znhbFeeBps":10,"freeAllowanceWei":"1000000000000000000000","effectiveHeight":2700100,"proposalId":1}}`
	if string(raw) != want {
		t.Fatalf("stored record changed shape:\n got %s\nwant %s", raw, want)
	}
}

func TestFeeParamsDelayBelowTheFloorIsRaisedToIt(t *testing.T) {
	for _, delay := range []uint64{0, 1, MinFeeScheduleDelayBlocks - 1} {
		f := newFeeFixture(t, FeeScheduleHost{Enabled: true, DelayBlocks: delay})
		if _, err := f.run(feePayload(20, 10, feeAllowance1000, ""), 5000); err != nil {
			t.Fatalf("delay %d: %v", delay, err)
		}
		if got := f.record().Pending.EffectiveHeight; got != 5000+MinFeeScheduleDelayBlocks {
			t.Errorf("delay %d: effective height %d, want %d", delay, got, 5000+MinFeeScheduleDelayBlocks)
		}
	}
	f := newFeeFixture(t, FeeScheduleHost{Enabled: true, DelayBlocks: MinFeeScheduleDelayBlocks})
	if _, err := f.run(feePayload(20, 10, feeAllowance1000, ""), 5000); err != nil {
		t.Fatal(err)
	}
	if got := f.record().Pending.EffectiveHeight; got != 5100 {
		t.Errorf("a delay at the floor: effective height %d, want 5100", got)
	}
}

func TestFeeParamsHeightOverflowIsRefused(t *testing.T) {
	f := newFeeFixture(t, FeeScheduleHost{Enabled: true, DelayBlocks: 100})
	if _, err := f.run(feePayload(20, 10, feeAllowance1000, ""), ^uint64(0)-10); err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("err = %v, want an overflow refusal", err)
	}
	if _, ok := f.state.ParamStoreGet(ParamKeyTransferFeeSchedule); ok {
		t.Fatal("an overflowing execution wrote the schedule")
	}
}

// A schedule that has taken effect is kept as Active when the next proposal executes.
func TestFeeParamsNextProposalAfterTheFirstTookEffectKeepsTheFirstActive(t *testing.T) {
	f := newFeeFixture(t, FeeScheduleHost{Enabled: true, DelayBlocks: 200})
	first, err := f.run(feePayload(20, 10, feeAllowance1000, ""), 1000)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.run(feePayload(30, 20, "5000000000000000000000", ""), 1200) // first took effect at 1200
	if err != nil {
		t.Fatal(err)
	}
	record := f.record()
	if record.Active == nil || record.Active.ProposalID != first || record.Active.NHBFeeBps != 20 {
		t.Fatalf("the schedule that took effect was lost: active = %+v", record.Active)
	}
	if record.Pending == nil || record.Pending.ProposalID != second || record.Pending.EffectiveHeight != 1400 {
		t.Fatalf("pending = %+v, want proposal %d at 1400", record.Pending, second)
	}
	// Until the second one's height the first governs; from it, the second.
	if got := record.InForce(1399); got == nil || got.ProposalID != first {
		t.Fatalf("height 1399: %+v, want proposal %d", got, first)
	}
	if got := record.InForce(1400); got == nil || got.ProposalID != second {
		t.Fatalf("height 1400: %+v, want proposal %d", got, second)
	}
}

// A proposal executed while an earlier one is still waiting replaces it.
func TestFeeParamsLaterProposalSupersedesAWaitingOne(t *testing.T) {
	f := newFeeFixture(t, FeeScheduleHost{Enabled: true, DelayBlocks: 200})
	first, err := f.run(feePayload(20, 10, feeAllowance1000, ""), 1000)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.run(feePayload(40, 30, feeAllowance1000, ""), 1100) // first still waits for 1200
	if err != nil {
		t.Fatal(err)
	}
	record := f.record()
	if record.Active != nil {
		t.Fatalf("a superseded schedule became active: %+v", record.Active)
	}
	if record.Pending == nil || record.Pending.ProposalID != second || record.Pending.NHBFeeBps != 40 {
		t.Fatalf("pending = %+v, want proposal %d", record.Pending, second)
	}
	if first == second {
		t.Fatal("proposal ids repeat")
	}
	// The audit record of the second execution names the one it replaced.
	found := false
	for _, entry := range f.state.audit {
		if entry.Event == AuditEventExecuted && entry.ProposalID == second && strings.Contains(entry.Details, `"supersededProposal":1`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("the execution audit does not name the superseded proposal: %+v", f.state.audit)
	}
}

// The execution path checks the payload again: a proposal whose stored payload is not
// acceptable (written before a bound was tightened, or directly into state) writes nothing.
func TestFeeParamsExecutionValidatesTheStoredPayloadAgain(t *testing.T) {
	for name, payload := range map[string]string{
		"rate above the ceiling": `{"nhbFeeBps":5000,"znhbFeeBps":10,"freeAllowanceWei":"1000"}`,
		"missing field":          `{"nhbFeeBps":20,"znhbFeeBps":10}`,
		"zero without the flag":  `{"nhbFeeBps":20,"znhbFeeBps":10,"freeAllowanceWei":"0"}`,
		"garbage":                `not json`,
	} {
		f := newFeeFixture(t, FeeScheduleHost{Enabled: true, DelayBlocks: 100})
		id, err := f.engine.SubmitProposal(f.proposer, ProposalKindFeeParams, feePayload(20, 10, feeAllowance1000, ""), big.NewInt(75))
		if err != nil {
			t.Fatal(err)
		}
		f.state.proposals[id].Status = ProposalStatusPassed
		if err := f.engine.QueueExecution(id); err != nil {
			t.Fatal(err)
		}
		f.state.proposals[id].TimelockEnd = f.now.Add(-time.Second)
		f.state.proposals[id].ProposedChange = payload
		f.engine.SetFeeSchedule(FeeScheduleHost{Enabled: true, Height: 10, DelayBlocks: 100})
		if err := f.engine.Execute(id); err == nil {
			t.Errorf("%s: executed", name)
		}
		if _, ok := f.state.ParamStoreGet(ParamKeyTransferFeeSchedule); ok {
			t.Errorf("%s: a refused execution wrote the schedule", name)
		}
		if f.state.proposals[id].Status == ProposalStatusExecuted {
			t.Errorf("%s: the proposal is marked executed", name)
		}
	}
}

// A stored record that does not parse is no record: the next proposal replaces it.
func TestFeeParamsAnUnreadableStoredRecordIsTreatedAsNone(t *testing.T) {
	f := newFeeFixture(t, FeeScheduleHost{Enabled: true, DelayBlocks: 100})
	_ = f.state.ParamStoreSet(ParamKeyTransferFeeSchedule, []byte(`{"active":{"nhbFeeBps":9999,"znhbFeeBps":1,"freeAllowanceWei":"5"}}`))
	if _, err := f.run(feePayload(20, 10, feeAllowance1000, ""), 100); err != nil {
		t.Fatalf("execute over an unreadable record: %v", err)
	}
	record := f.record()
	if record.Active != nil || record.Pending == nil {
		t.Fatalf("record = %+v, want only the new pending schedule", record)
	}
}

func TestParseFeeScheduleRecord(t *testing.T) {
	if record, err := ParseFeeScheduleRecord(nil); record != nil || err != nil {
		t.Fatalf("nil: %v %v", record, err)
	}
	if record, err := ParseFeeScheduleRecord([]byte("  \n")); record != nil || err != nil {
		t.Fatalf("blank: %v %v", record, err)
	}
	good := `{"active":{"nhbFeeBps":20,"znhbFeeBps":10,"freeAllowanceWei":"5","effectiveHeight":1,"proposalId":1},"pending":{"nhbFeeBps":100,"znhbFeeBps":0,"freeAllowanceWei":"0","effectiveHeight":9,"proposalId":2}}`
	if record, err := ParseFeeScheduleRecord([]byte(good)); err != nil || record.Active == nil || record.Pending == nil {
		t.Fatalf("a good record: %v %+v", err, record)
	}
	for name, raw := range map[string]string{
		"rate above the ceiling":  `{"active":{"nhbFeeBps":101,"znhbFeeBps":10,"freeAllowanceWei":"5","effectiveHeight":1,"proposalId":1}}`,
		"znhb rate above":         `{"pending":{"nhbFeeBps":1,"znhbFeeBps":101,"freeAllowanceWei":"5","effectiveHeight":1,"proposalId":1}}`,
		"allowance over the cap":  `{"active":{"nhbFeeBps":1,"znhbFeeBps":1,"freeAllowanceWei":"1000000000000000000000000001","effectiveHeight":1,"proposalId":1}}`,
		"allowance not digits":    `{"active":{"nhbFeeBps":1,"znhbFeeBps":1,"freeAllowanceWei":"1e3","effectiveHeight":1,"proposalId":1}}`,
		"unknown field":           `{"active":{"nhbFeeBps":1,"znhbFeeBps":1,"freeAllowanceWei":"5","effectiveHeight":1,"proposalId":1,"extra":1}}`,
		"unknown top-level field": `{"later":{}}`,
		"trailing data":           good + `{}`,
		"not JSON":                `nope`,
	} {
		if record, err := ParseFeeScheduleRecord([]byte(raw)); err == nil {
			t.Errorf("%s: accepted: %+v", name, record)
		}
	}
}

// An active schedule governs from its own height, never before it.
func TestFeeScheduleRecordActiveGovernsOnlyFromItsOwnHeight(t *testing.T) {
	r := &FeeScheduleRecord{Active: &ScheduledFeeValues{FeeScheduleValues: FeeScheduleValues{NHBFeeBps: 5, FreeAllowanceWei: "1"}, EffectiveHeight: 50, ProposalID: 1}}
	if got := r.InForce(49); got != nil {
		t.Fatalf("height 49: %+v, want none", got)
	}
	if got := r.InForce(50); got == nil || got.ProposalID != 1 {
		t.Fatalf("height 50: %+v", got)
	}
}

func TestFeeScheduleRecordSettle(t *testing.T) {
	v := func(id uint64, at uint64) *ScheduledFeeValues {
		return &ScheduledFeeValues{FeeScheduleValues: FeeScheduleValues{NHBFeeBps: uint32(id), FreeAllowanceWei: "1"}, EffectiveHeight: at, ProposalID: id}
	}
	r := &FeeScheduleRecord{Active: v(1, 10), Pending: v(2, 50)}
	r.Settle(49)
	if r.Active.ProposalID != 1 || r.Pending == nil {
		t.Fatalf("settled one block early: %+v %+v", r.Active, r.Pending)
	}
	r.Settle(50)
	if r.Active.ProposalID != 2 || r.Pending != nil {
		t.Fatalf("not settled at the height: %+v %+v", r.Active, r.Pending)
	}
	var nilRecord *FeeScheduleRecord
	nilRecord.Settle(1)
	if nilRecord.InForce(1) != nil {
		t.Fatal("a nil record has a schedule")
	}
}

// param.update cannot write the schedule key even if an operator lists it: the generic
// parameter engine has no validation rule for it, so a proposal of that kind is refused,
// and the delay and the ceiling can not be walked around.
func TestFeeScheduleKeyIsNotReachableByAParamUpdate(t *testing.T) {
	engine := NewEngine()
	engine.SetPolicy(ProposalPolicy{MinDepositWei: big.NewInt(0), VotingPeriodSeconds: 60, TimelockSeconds: 10, AllowedParams: []string{ParamKeyTransferFeeSchedule}})
	payload := `{"` + ParamKeyTransferFeeSchedule + `":"{\"active\":{\"nhbFeeBps\":1,\"znhbFeeBps\":1,\"freeAllowanceWei\":\"1\"}}"}`
	if _, err := engine.validateParamPayload(payload); err == nil {
		t.Fatal("a param.update payload for the fee schedule key was accepted")
	}
}

// The bounds are consensus values: a validator that carried another ceiling, floor or cap would accept
// or refuse a proposal the others do not. Pin the literals.
func TestFeeScheduleConsensusConstantsArePinned(t *testing.T) {
	if MaxTransferFeeBps != 100 {
		t.Errorf("MaxTransferFeeBps = %d, want 100", MaxTransferFeeBps)
	}
	if MinFeeScheduleDelayBlocks != 100 {
		t.Errorf("MinFeeScheduleDelayBlocks = %d, want 100", MinFeeScheduleDelayBlocks)
	}
	if got := MaxFreeAllowanceWei().String(); got != "1000000000000000000000000000" {
		t.Errorf("MaxFreeAllowanceWei = %s, want 1e27", got)
	}
	if ProposalKindFeeParams != "policy.feeParams" {
		t.Errorf("ProposalKindFeeParams = %q", ProposalKindFeeParams)
	}
	if ParamKeyTransferFeeSchedule != "fees.transferSchedule" {
		t.Errorf("ParamKeyTransferFeeSchedule = %q", ParamKeyTransferFeeSchedule)
	}
	if maxFeeScheduleMemoLength != 256 {
		t.Errorf("memo limit = %d, want 256", maxFeeScheduleMemoLength)
	}
}
