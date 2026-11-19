package core

import (
	"math/big"
	"strings"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
)

// W11 part B: a passed policy.feeParams proposal sets the transfer fee schedule. Every step here
// goes through sp.ApplyTransaction with a real signed envelope (propose, queue, execute), and the
// fee is measured on real transfers in the blocks just below and at the effective height.
//
// The base configuration is the one of the uniform-fee tests: allowance 1,000 wei, 20 basis
// points NHB and 10 ZNHB, a transfer of 100,000.

const feeGovActivation = uint64(100)

// payload builds a policy.feeParams JSON payload.
func feeGovPayload(nhb, znhb uint32, allowance string, zeroFlag bool) string {
	s := `{"nhbFeeBps":` + big.NewInt(int64(nhb)).String() + `,"znhbFeeBps":` + big.NewInt(int64(znhb)).String() + `,"freeAllowanceWei":"` + allowance + `"`
	if zeroFlag {
		s += `,"allowZeroAllowance":true`
	}
	return s + `}`
}

func newFeeGovEnv(t *testing.T, activation, delay uint64) *uniformEnv {
	t.Helper()
	e := newUniformEnv(t, 0, true)
	e.sp.SetTransferFeeGovernance(activation, delay)
	e.sp.SetGovernancePolicy(governance.ProposalPolicy{MinDepositWei: big.NewInt(0), VotingPeriodSeconds: 60, TimelockSeconds: 10})
	return e
}

// applyGov applies a signed governance transaction and returns the error, so a refusal can be tested
// (applySignedGovTx fails the test on one).
func (e *uniformEnv) applyGov(key *crypto.PrivateKey, txType types.TxType, data []byte) error {
	tx := &types.Transaction{ChainID: types.NHBChainID(), Type: txType, Nonce: 0, Data: data, GasLimit: 0, GasPrice: big.NewInt(0)}
	if err := tx.Sign(key.PrivateKey); err != nil {
		e.t.Fatalf("sign governance tx: %v", err)
	}
	return e.sp.ApplyTransaction(tx)
}

// propose submits a proposal at the given block height and returns its id (error, not fatal, so
// the refusals can be tested).
func (e *uniformEnv) propose(height uint64, payload string) (uint64, error) {
	e.t.Helper()
	e.at(height)
	key := freshGovAccountKey(e.t, e.sp, big.NewInt(0))
	data, err := rlpEncode(govProposePayload{Kind: governance.ProposalKindFeeParams, Payload: payload, Deposit: big.NewInt(0)})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.applyGov(key, types.TxTypeGovPropose, data); err != nil {
		return 0, err
	}
	return latestGovProposalID(e.t, e.sp), nil
}

// finish queues and executes a submitted proposal at the given block height.
func (e *uniformEnv) finish(id, height uint64) error {
	e.t.Helper()
	e.at(height)
	markGovProposalPassed(e.t, e.sp, id)
	queueKey := freshGovAccountKey(e.t, e.sp, big.NewInt(0))
	data, err := rlpEncode(govProposalIDPayload{ProposalID: id})
	if err != nil {
		e.t.Fatal(err)
	}
	if err := e.applyGov(queueKey, types.TxTypeGovQueue, data); err != nil {
		e.t.Fatalf("queue: %v", err)
	}
	clearGovProposalTimelock(e.t, e.sp, id)
	execKey := freshGovAccountKey(e.t, e.sp, big.NewInt(0))
	return e.applyGov(execKey, types.TxTypeGovExecute, data)
}

// run proposes at height and executes in the same block.
func (e *uniformEnv) run(height uint64, payload string) uint64 {
	e.t.Helper()
	id, err := e.propose(height, payload)
	if err != nil {
		e.t.Fatalf("propose %s: %v", payload, err)
	}
	if err := e.finish(id, height); err != nil {
		e.t.Fatalf("execute %s: %v", payload, err)
	}
	return id
}

func (e *uniformEnv) pending() *governance.FeeScheduleRecord {
	e.t.Helper()
	raw, ok, err := nhbstate.NewManager(e.sp.Trie).ParamStoreGet(governance.ParamKeyTransferFeeSchedule)
	if err != nil || !ok {
		e.t.Fatalf("no schedule record stored (found=%v, err=%v)", ok, err)
	}
	record, err := governance.ParseFeeScheduleRecord(raw)
	if err != nil {
		e.t.Fatalf("stored record: %v", err)
	}
	return record
}

// feeOf sends one plain transfer at the given height and returns the fee the collector received.
func (e *uniformEnv) feeOf(a uniformAsset, height uint64, value int64) int64 {
	e.t.Helper()
	e.at(height)
	return e.send(a, value, "", false).collector
}

// A schedule executed at block H with a delay D charges its rates from block H+D and not before,
// in both assets, on the same sender, with the old rates up to the block before.
func TestGovernedFeeScheduleTakesEffectAtTheDelayedHeightAndNotBefore(t *testing.T) {
	e := newFeeGovEnv(t, feeGovActivation, 500)
	e.feeOf(uniformAssets[0], 900, uniformValue) // use up the allowance in both assets
	e.feeOf(uniformAssets[1], 900, uniformValue)

	e.run(1000, feeGovPayload(50, 30, "1000", false))
	record := e.pending()
	if record.Pending == nil || record.Pending.EffectiveHeight != 1500 || record.Active != nil {
		t.Fatalf("record after execution at 1000 with delay 500: %+v", record)
	}
	for _, a := range uniformAssets {
		governedBps := map[string]int64{"NHB": 50, "ZNHB": 30}[a.name]
		for _, h := range []uint64{1000, 1001, 1499} {
			if got, want := e.feeOf(a, h, uniformValue), a.ordinary(); got != want {
				t.Fatalf("%s at %d (before the effective height): fee %d, want the configured %d", a.name, h, got, want)
			}
		}
		for _, h := range []uint64{1500, 1501, 9_000_000} {
			if got, want := e.feeOf(a, h, uniformValue), uniformValue*governedBps/10_000; got != want {
				t.Fatalf("%s at %d (from the effective height): fee %d, want the governed %d", a.name, h, got, want)
			}
		}
	}
}

// The delay is raised to the floor when the configuration carries less.
func TestGovernedFeeScheduleDelayIsRaisedToTheFloor(t *testing.T) {
	e := newFeeGovEnv(t, feeGovActivation, 3)
	e.run(1000, feeGovPayload(50, 30, "1000", false))
	if got := e.pending().Pending.EffectiveHeight; got != 1000+governance.MinFeeScheduleDelayBlocks {
		t.Fatalf("effective height %d, want %d", got, 1000+governance.MinFeeScheduleDelayBlocks)
	}
}

// The governed allowance replaces the configured one, in both directions.
func TestGovernedAllowanceReplacesTheConfiguredOne(t *testing.T) {
	a := uniformAssets[0]
	// A larger allowance: a sender that has spent past the configured allowance becomes free again.
	e := newFeeGovEnv(t, feeGovActivation, 200)
	e.feeOf(a, 900, uniformValue) // spent 100,000, above the configured 1,000
	e.run(1000, feeGovPayload(20, 10, "10000000", false))
	if got := e.feeOf(a, 1199, 1_000); got != 2 {
		t.Fatalf("before the height: fee %d, want 2 (20 bps of 1,000, the allowance is spent)", got)
	}
	if got := e.feeOf(a, 1200, uniformValue); got != 0 {
		t.Fatalf("from the height under a 10,000,000 allowance a sender that has spent 101,000 must be free, fee %d", got)
	}

	// A zero allowance (with the flag): even a fresh sender pays from the height.
	z := newFeeGovEnv(t, feeGovActivation, 200)
	z.run(1000, feeGovPayload(20, 10, "0", true))
	if got := z.feeOf(a, 1199, uniformValue); got != 0 {
		t.Fatalf("a fresh sender before the height is inside the configured allowance, fee %d", got)
	}
	z2 := newFeeGovEnv(t, feeGovActivation, 200)
	z2.run(1000, feeGovPayload(20, 10, "0", true))
	if got := z2.feeOf(a, 1200, uniformValue); got != a.ordinary() {
		t.Fatalf("from the height under a zero allowance a fresh sender pays, fee %d, want %d", got, a.ordinary())
	}
}

// A schedule that has taken effect is the base for the next one: until the second one's height the
// first governs, then the second.
func TestGovernedFeeScheduleChain(t *testing.T) {
	a := uniformAssets[0]
	e := newFeeGovEnv(t, feeGovActivation, 200)
	e.feeOf(a, 900, uniformValue)
	e.run(1000, feeGovPayload(40, 10, "1000", false)) // from 1200
	e.run(1300, feeGovPayload(60, 10, "1000", false)) // from 1500; the first took effect at 1200
	record := e.pending()
	if record.Active == nil || record.Active.NHBFeeBps != 40 || record.Pending == nil || record.Pending.NHBFeeBps != 60 || record.Pending.EffectiveHeight != 1500 {
		t.Fatalf("record: active %+v pending %+v", record.Active, record.Pending)
	}
	// Blocks only move forward: ascending heights.
	for _, step := range []struct {
		height uint64
		bps    int64
	}{{1301, 40}, {1499, 40}, {1500, 60}, {1501, 60}} {
		if got, want := e.feeOf(a, step.height, uniformValue), uniformValue*step.bps/10_000; got != want {
			t.Fatalf("height %d: fee %d, want %d (%d bps)", step.height, got, want, step.bps)
		}
	}
}

// The kind does not exist below the activation height, and the old values rule: a proposal is
// refused as an unknown kind, and a record that is somehow in the store is not read.
func TestFeeParamsProposalIsRefusedBelowTheActivationHeight(t *testing.T) {
	e := newFeeGovEnv(t, feeGovActivation, 200)
	if _, err := e.propose(feeGovActivation-1, feeGovPayload(50, 30, "1000", false)); err == nil || !strings.Contains(err.Error(), "unsupported proposal kind") {
		t.Fatalf("one block below the activation height: err = %v, want an unsupported kind refusal", err)
	}
	if _, err := e.propose(feeGovActivation, feeGovPayload(50, 30, "1000", false)); err != nil {
		t.Fatalf("at the activation height: %v", err)
	}
	off := newFeeGovEnv(t, 0, 200)
	if _, err := off.propose(1_000_000, feeGovPayload(50, 30, "1000", false)); err == nil || !strings.Contains(err.Error(), "unsupported proposal kind") {
		t.Fatalf("with the setting off: err = %v, want an unsupported kind refusal", err)
	}
}

func TestFeeParamsProposalRefusesNonsenseThroughTheTransactionPath(t *testing.T) {
	e := newFeeGovEnv(t, feeGovActivation, 200)
	for name, payload := range map[string]string{
		"rate above the ceiling": feeGovPayload(101, 10, "1000", false),
		"zero without the flag":  feeGovPayload(20, 10, "0", false),
		"missing allowance":      `{"nhbFeeBps":20,"znhbFeeBps":10}`,
		"unknown field":          `{"nhbFeeBps":20,"znhbFeeBps":10,"freeAllowanceWei":"1000","domainFeeBps":150}`,
	} {
		if _, err := e.propose(500, payload); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// With the setting off a record that is in the store (planted: no proposal can write one) changes
// nothing, and no state is read for it.
func TestStoredScheduleIsNotReadWhileTheRuleIsOff(t *testing.T) {
	a := uniformAssets[0]
	e := newFeeGovEnv(t, 0, 200)
	record := `{"active":{"nhbFeeBps":90,"znhbFeeBps":90,"freeAllowanceWei":"5","effectiveHeight":1,"proposalId":1}}`
	if err := nhbstate.NewManager(e.sp.Trie).ParamStoreSet(governance.ParamKeyTransferFeeSchedule, []byte(record)); err != nil {
		t.Fatal(err)
	}
	e.feeOf(a, 500, uniformValue)
	if got := e.feeOf(a, 600, uniformValue); got != a.ordinary() {
		t.Fatalf("a planted record was read with the rule off: fee %d, want the configured %d", got, a.ordinary())
	}
	policy, err := e.sp.effectiveTransferGasPolicy()
	if err != nil || policy.FeeBps != 20 || policy.FreeSpendLimitWei.Cmp(big.NewInt(uniformAllowance)) != 0 {
		t.Fatalf("effective policy with the rule off: %+v, %v", policy, err)
	}
}

// A stored record that is not an acceptable schedule is not applied: the configured values stay.
func TestAnUnacceptableStoredScheduleIsIgnored(t *testing.T) {
	a := uniformAssets[0]
	for name, record := range map[string]string{
		"rate above the ceiling": `{"active":{"nhbFeeBps":9999,"znhbFeeBps":1,"freeAllowanceWei":"1000","effectiveHeight":1,"proposalId":1}}`,
		"allowance not digits":   `{"active":{"nhbFeeBps":90,"znhbFeeBps":1,"freeAllowanceWei":"1e3","effectiveHeight":1,"proposalId":1}}`,
		"not JSON":               `nope`,
	} {
		e := newFeeGovEnv(t, feeGovActivation, 200)
		if err := nhbstate.NewManager(e.sp.Trie).ParamStoreSet(governance.ParamKeyTransferFeeSchedule, []byte(record)); err != nil {
			t.Fatal(err)
		}
		e.feeOf(a, 500, uniformValue)
		if got := e.feeOf(a, 600, uniformValue); got != a.ordinary() {
			t.Fatalf("%s: fee %d, want the configured %d", name, got, a.ordinary())
		}
	}
}

// The governed rate does not touch what a transfer naming a domain pays under the uniform rule: it
// pays the governed ordinary fee, routed to the domain.
func TestGovernedFeeAppliesToDomainTransfersUnderTheUniformRule(t *testing.T) {
	a := uniformAssets[0]
	e := newFeeGovEnv(t, feeGovActivation, 200)
	e.sp.SetUniformTransferFee(50)
	e.at(900)
	e.send(a, uniformValue, "pos", false)
	e.run(1000, feeGovPayload(60, 10, "1000", false))
	e.at(1200)
	got := e.send(a, uniformValue, "pos", false)
	want := flows{sender: -(uniformValue + uniformValue*60/10_000), recipient: uniformValue, route: uniformValue * 60 / 10_000}
	if got != want {
		t.Fatalf("domain transfer under governed 60 bps: %+v, want %+v", got, want)
	}
}

func TestFeeGovernanceActiveBoundaries(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetTransferFeeGovernance(600, 5)
	for height, want := range map[uint64]bool{0: false, 599: false, 600: true, 601: true, 1 << 50: true} {
		sp.BeginBlock(height, time.Unix(1_800_000_000, 0))
		if got := sp.feeGovernanceActive(); got != want {
			t.Errorf("height %d: active = %v, want %v", height, got, want)
		}
		host := sp.feeScheduleHost()
		if host.Enabled != want || host.Height != height || host.DelayBlocks != 5 {
			t.Errorf("height %d: host %+v", height, host)
		}
	}
	sp.SetTransferFeeGovernance(0, 5)
	sp.BeginBlock(1<<40, time.Unix(1_800_000_000, 0))
	if sp.feeGovernanceActive() || sp.feeScheduleHost().Enabled {
		t.Error("setting 0 is not off")
	}
	var nilSP *StateProcessor
	nilSP.SetTransferFeeGovernance(1, 1)
	if nilSP.feeGovernanceActive() || nilSP.feeScheduleHost().Enabled {
		t.Error("a nil state processor is active")
	}
}

func TestFeeGovernanceSettingsAreCopiedAndSurviveARebuild(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetTransferFeeGovernance(777, 888)
	cp, err := sp.Copy()
	if err != nil {
		t.Fatal(err)
	}
	if cp.feeGovFromHeight != 777 || cp.feeGovDelayBlocks != 888 {
		t.Fatalf("the copy holds %d and %d, want 777 and 888", cp.feeGovFromHeight, cp.feeGovDelayBlocks)
	}
	node := newTestNode(t)
	node.SetTransferFeeGovernance(9, 11)
	node.stateMu.Lock()
	gotH, gotD := node.state.feeGovFromHeight, node.state.feeGovDelayBlocks
	err = node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuiltH, rebuiltD := node.state.feeGovFromHeight, node.state.feeGovDelayBlocks
	node.stateMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if gotH != 9 || gotD != 11 || rebuiltH != 9 || rebuiltD != 11 {
		t.Fatalf("setter reached the processor at %d/%d, rebuilt at %d/%d, want 9/11 and 9/11", gotH, gotD, rebuiltH, rebuiltD)
	}
}

// The read view the RPC serves: configured values and nothing pending until a proposal executes, then
// the pending schedule with the blocks left, then the governed schedule in force.
func TestNodeTransferFeeScheduleView(t *testing.T) {
	node := newTestNode(t)
	node.SetTransferFeeGovernance(1, 300)
	node.SetUniformTransferFee(0)
	view, err := node.TransferFeeSchedule()
	if err != nil {
		t.Fatal(err)
	}
	base := node.TransferGasPolicy()
	if view.InForce.Source != "config" || view.InForce.NHBFeeBps != base.FeeBps || view.InForce.ZNHBFeeBps != base.FeeBpsZNHB || view.Pending != nil || view.BlocksUntilPending != 0 {
		t.Fatalf("fresh view: %+v", view)
	}
	if view.GovernanceDelayBlocks != 300 || view.MaxFeeBps != governance.MaxTransferFeeBps || view.UniformDomainFeeActive || !view.GovernanceActive {
		t.Fatalf("rules in the view: %+v", view)
	}
	next := view.NextBlockHeight

	record := governance.FeeScheduleRecord{
		Active:  &governance.ScheduledFeeValues{FeeScheduleValues: governance.FeeScheduleValues{NHBFeeBps: 25, ZNHBFeeBps: 15, FreeAllowanceWei: "500"}, EffectiveHeight: 1, ProposalID: 3},
		Pending: &governance.ScheduledFeeValues{FeeScheduleValues: governance.FeeScheduleValues{NHBFeeBps: 35, ZNHBFeeBps: 5, FreeAllowanceWei: "700"}, EffectiveHeight: next + 40, ProposalID: 4},
	}
	raw := mustJSON(t, record)
	if err := node.WithState(func(m *nhbstate.Manager) error { return m.ParamStoreSet(governance.ParamKeyTransferFeeSchedule, raw) }); err != nil {
		t.Fatal(err)
	}
	view, err = node.TransferFeeSchedule()
	if err != nil {
		t.Fatal(err)
	}
	if view.InForce.Source != "governance" || view.InForce.NHBFeeBps != 25 || view.InForce.ProposalID != 3 || view.InForce.FreeAllowanceWei != "500" {
		t.Fatalf("in force: %+v", view.InForce)
	}
	if view.Pending == nil || view.Pending.NHBFeeBps != 35 || view.Pending.FromHeight != next+40 || view.BlocksUntilPending != 40 || view.Pending.ProposalID != 4 {
		t.Fatalf("pending: %+v until %d", view.Pending, view.BlocksUntilPending)
	}

	// With the rule off the view shows the configured values whatever is stored.
	node.SetTransferFeeGovernance(0, 300)
	view, err = node.TransferFeeSchedule()
	if err != nil {
		t.Fatal(err)
	}
	if view.InForce.Source != "config" || view.Pending != nil || view.GovernanceActive {
		t.Fatalf("rule off: %+v", view)
	}
}

// The overlay makes the policy enabled exactly when the governed allowance is positive, whatever the
// configured policy was: a node whose file has no allowance (policy disabled) follows a governed one.
func TestGovernedOverlayDerivesEnabledFromTheAllowance(t *testing.T) {
	sched := func(allowance string) *governance.ScheduledFeeValues {
		return &governance.ScheduledFeeValues{FeeScheduleValues: governance.FeeScheduleValues{NHBFeeBps: 33, ZNHBFeeBps: 44, FreeAllowanceWei: allowance}, EffectiveHeight: 1, ProposalID: 1}
	}
	disabled := TransferGasPolicy{Enabled: false, FreeSpendLimitWei: big.NewInt(0), Window: TransferGasWindowLifetime, FeeBps: 1, FeeBpsZNHB: 2}
	enabled := TransferGasPolicy{Enabled: true, FreeSpendLimitWei: big.NewInt(1000), Window: TransferGasWindowMonthly, FeeBps: 1, FeeBpsZNHB: 2}
	collector := [20]byte{9}
	enabled.FeeCollector = collector

	got := overlayGovernedFees(disabled, sched("5000"))
	if !got.Enabled || got.FreeSpendLimitWei.Cmp(big.NewInt(5000)) != 0 || got.FeeBps != 33 || got.FeeBpsZNHB != 44 {
		t.Fatalf("disabled base + positive allowance: %+v", got)
	}
	got = overlayGovernedFees(enabled, sched("0"))
	if got.Enabled || got.FreeSpendLimitWei.Sign() != 0 {
		t.Fatalf("enabled base + zero allowance: %+v", got)
	}
	// What the schedule does not govern stays as configured.
	if got.FeeCollector != collector || got.Window != TransferGasWindowMonthly {
		t.Fatalf("collector or window changed by the overlay: %+v", got)
	}
	// No schedule: the base, untouched.
	if got := overlayGovernedFees(enabled, nil); got.FeeBps != 1 || got.FreeSpendLimitWei.Cmp(big.NewInt(1000)) != 0 || !got.Enabled {
		t.Fatalf("no schedule: %+v", got)
	}
	// The overlay never aliases the base's allowance.
	base := enabled.Clone()
	over := overlayGovernedFees(base, sched("7"))
	over.FreeSpendLimitWei.SetInt64(99)
	if base.FreeSpendLimitWei.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("the overlay shares the base's allowance: %s", base.FreeSpendLimitWei)
	}
}

// The node reads the next block's height: the rule is in force for the view and for the quote policy
// exactly when the next block is at or above the activation height.
func TestNodeFeeGovernanceActivationBoundaryForReaders(t *testing.T) {
	node := newTestNode(t)
	next := node.GetHeight() + 1
	record := governance.FeeScheduleRecord{Active: &governance.ScheduledFeeValues{FeeScheduleValues: governance.FeeScheduleValues{NHBFeeBps: 61, ZNHBFeeBps: 62, FreeAllowanceWei: "9"}, EffectiveHeight: 1, ProposalID: 1}}
	raw := mustJSON(t, record)
	if err := node.WithState(func(m *nhbstate.Manager) error { return m.ParamStoreSet(governance.ParamKeyTransferFeeSchedule, raw) }); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		activation uint64
		governed   bool
	}{{0, false}, {next + 1, false}, {next + 1000, false}, {next, true}} {
		node.SetTransferFeeGovernance(tc.activation, 100)
		policy, err := node.EffectiveTransferGasPolicy()
		if err != nil {
			t.Fatal(err)
		}
		view, err := node.TransferFeeSchedule()
		if err != nil {
			t.Fatal(err)
		}
		if got := policy.FeeBps == 61; got != tc.governed {
			t.Errorf("activation %d (next block %d): effective policy governed = %v, want %v", tc.activation, next, got, tc.governed)
		}
		if got := view.InForce.Source == "governance"; got != tc.governed || view.GovernanceActive != tc.governed {
			t.Errorf("activation %d (next block %d): view governed = %v active = %v, want %v", tc.activation, next, got, view.GovernanceActive, tc.governed)
		}
	}
}

// The view at the effective height: a pending schedule whose block is the next block is the schedule in
// force (not still pending); one a block later is pending with one block to go; one already reached but
// not yet moved to the active slot (nothing has executed since) is in force too.
func TestNodeTransferFeeScheduleViewAtTheEffectiveHeight(t *testing.T) {
	node := newTestNode(t)
	node.SetTransferFeeGovernance(1, 100)
	next := node.GetHeight() + 1
	vals := func(nhb uint32, allowance string, id, at uint64) *governance.ScheduledFeeValues {
		return &governance.ScheduledFeeValues{FeeScheduleValues: governance.FeeScheduleValues{NHBFeeBps: nhb, ZNHBFeeBps: nhb, FreeAllowanceWei: allowance}, EffectiveHeight: at, ProposalID: id}
	}
	for name, tc := range map[string]struct {
		pendingAt   uint64
		wantID      uint64
		wantPending bool
		wantBlocks  uint64
	}{
		"effective at the next block": {next, 2, false, 0},
		"effective one block later":   {next + 1, 1, true, 1},
		"already reached, unsettled":  {1, 2, false, 0},
		"far in the future":           {next + 5000, 1, true, 5000},
	} {
		record := governance.FeeScheduleRecord{Active: vals(25, "500", 1, 1), Pending: vals(35, "700", 2, tc.pendingAt)}
		raw := mustJSON(t, record)
		if err := node.WithState(func(m *nhbstate.Manager) error { return m.ParamStoreSet(governance.ParamKeyTransferFeeSchedule, raw) }); err != nil {
			t.Fatal(err)
		}
		view, err := node.TransferFeeSchedule()
		if err != nil {
			t.Fatal(err)
		}
		if view.InForce.ProposalID != tc.wantID || (view.Pending != nil) != tc.wantPending || view.BlocksUntilPending != tc.wantBlocks {
			t.Errorf("%s: in force proposal %d (want %d), pending shown %v (want %v), blocks %d (want %d)", name, view.InForce.ProposalID, tc.wantID, view.Pending != nil, tc.wantPending, view.BlocksUntilPending, tc.wantBlocks)
		}
		policy, err := node.EffectiveTransferGasPolicy()
		if err != nil {
			t.Fatal(err)
		}
		wantBps := map[uint64]uint32{1: 25, 2: 35}[tc.wantID]
		if policy.FeeBps != wantBps {
			t.Errorf("%s: effective policy rate %d, want %d", name, policy.FeeBps, wantBps)
		}
	}
}
