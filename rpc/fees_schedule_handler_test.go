package rpc

import (
	"bytes"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/governance"
)

// fees_getTransferSchedule: the fee schedule the next block will charge, where it comes from, and a
// change that is waiting for its block. An open read.

func callFeesSchedule(t *testing.T, env *testEnv) feesTransferScheduleResult {
	t.Helper()
	body := buildRPCRequestBody(t, 9, "fees_getTransferSchedule", []json.RawMessage{})
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	env.server.ServeHTTP(rec, req) // no credential at all
	if rec.Code != http.StatusOK {
		t.Fatalf("anonymous fees_getTransferSchedule returned %d: %s", rec.Code, rec.Body.String())
	}
	raw, rpcErr := decodeRPCResponse(t, rec)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var result feesTransferScheduleResult
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode: %v (%s)", err, raw)
	}
	return result
}

func plantFeeSchedule(t *testing.T, env *testEnv, record governance.FeeScheduleRecord) {
	t.Helper()
	raw, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		return m.ParamStoreSet(governance.ParamKeyTransferFeeSchedule, raw)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFeesGetTransferScheduleReportsTheConfiguredScheduleByDefault(t *testing.T) {
	env := newTestEnv(t)
	got := callFeesSchedule(t, env)
	policy := env.node.TransferGasPolicy()
	if got.InForce.Source != "config" || got.InForce.NHBFeeBps != policy.FeeBps || got.InForce.ZNHBFeeBps != policy.FeeBpsZNHB || got.InForce.FreeAllowanceWei != policy.FreeSpendLimitWei.String() {
		t.Fatalf("in force: %+v, configured %+v", got.InForce, policy)
	}
	if got.Pending != nil || got.BlocksUntilPending != 0 {
		t.Fatalf("a change is pending on a fresh node: %+v", got.Pending)
	}
	if got.UniformDomainFee.Active || got.UniformDomainFee.ActivationHeight != 0 || got.Governance.Active || got.Governance.ActivationHeight != 0 {
		t.Fatalf("both rules must be off by default: %+v %+v", got.UniformDomainFee, got.Governance)
	}
	if got.Governance.MaxFeeBps != governance.MaxTransferFeeBps || got.Governance.MaxFreeAllowanceWei != governance.MaxFreeAllowanceWei().String() || got.Governance.DelayBlocks != governance.MinFeeScheduleDelayBlocks {
		t.Fatalf("bounds: %+v", got.Governance)
	}
	if got.NextBlockHeight != env.node.GetHeight()+1 {
		t.Fatalf("next block height %d, chain at %d", got.NextBlockHeight, env.node.GetHeight())
	}
}

func TestFeesGetTransferSchedulePendingAndGovernedSchedule(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetTransferFeeGovernance(1, 2_000)
	env.node.SetUniformTransferFee(1)
	next := env.node.GetHeight() + 1
	plantFeeSchedule(t, env, governance.FeeScheduleRecord{
		Active:  &governance.ScheduledFeeValues{FeeScheduleValues: governance.FeeScheduleValues{NHBFeeBps: 25, ZNHBFeeBps: 15, FreeAllowanceWei: "500"}, EffectiveHeight: 1, ProposalID: 3},
		Pending: &governance.ScheduledFeeValues{FeeScheduleValues: governance.FeeScheduleValues{NHBFeeBps: 35, ZNHBFeeBps: 5, FreeAllowanceWei: "700"}, EffectiveHeight: next + 90, ProposalID: 4},
	})
	got := callFeesSchedule(t, env)
	if got.InForce.Source != "governance" || got.InForce.NHBFeeBps != 25 || got.InForce.ZNHBFeeBps != 15 || got.InForce.FreeAllowanceWei != "500" || got.InForce.ProposalID != 3 {
		t.Fatalf("in force: %+v", got.InForce)
	}
	if got.Pending == nil || got.Pending.NHBFeeBps != 35 || got.Pending.ZNHBFeeBps != 5 || got.Pending.FreeAllowanceWei != "700" || got.Pending.FromHeight != next+90 || got.BlocksUntilPending != 90 || got.Pending.Source != "governance" {
		t.Fatalf("pending: %+v, blocks left %d", got.Pending, got.BlocksUntilPending)
	}
	if !got.Governance.Active || got.Governance.DelayBlocks != 2_000 || !got.UniformDomainFee.Active || got.UniformDomainFee.ActivationHeight != 1 {
		t.Fatalf("rules: %+v %+v", got.Governance, got.UniformDomainFee)
	}
	// With the governance rule off the stored record is not shown as the schedule in force.
	env.node.SetTransferFeeGovernance(0, 2_000)
	off := callFeesSchedule(t, env)
	if off.InForce.Source != "config" || off.Pending != nil || off.Governance.Active {
		t.Fatalf("rule off: %+v pending %+v", off.InForce, off.Pending)
	}
}

func TestFeesGetTransferScheduleChangesNoState(t *testing.T) {
	env := newTestEnv(t)
	env.node.SetTransferFeeGovernance(1, 2_000)
	plantFeeSchedule(t, env, governance.FeeScheduleRecord{
		Pending: &governance.ScheduledFeeValues{FeeScheduleValues: governance.FeeScheduleValues{NHBFeeBps: 35, ZNHBFeeBps: 5, FreeAllowanceWei: "700"}, EffectiveHeight: 99, ProposalID: 4},
	})
	root := func() string { return string(env.node.PendingStateRoot()) }
	before := root()
	for i := 0; i < 3; i++ {
		callFeesSchedule(t, env)
	}
	if root() != before {
		t.Fatal("reading fees_getTransferSchedule changed the state root")
	}
}

func TestFeesGetTransferScheduleIsOpenAndKnown(t *testing.T) {
	if IsOperatorOnlyMethod("fees_getTransferSchedule") {
		t.Fatal("fees_getTransferSchedule is operator-only")
	}
	if !isKnownRPCMethod("fees_getTransferSchedule") {
		t.Fatal("fees_getTransferSchedule is not in the table of known methods (its metrics would be labelled \"unknown\")")
	}
}

// A quote is the fee a transfer will really pay: once a governed schedule is in force the quote and the
// status read its rate and its allowance, not the configuration's.
func TestFeesQuoteFollowsTheGovernedSchedule(t *testing.T) {
	server, wallet := newFeesQuoteTestServer(t)
	node := server.node
	amount := big.NewInt(1_000_000)
	quote := func(asset string) feesTransferQuoteResult {
		t.Helper()
		params, _ := json.Marshal(map[string]string{"address": wallet, "asset": asset, "amountWei": amount.String()})
		recorder := httptest.NewRecorder()
		server.handleFeesGetTransferQuote(recorder, httptest.NewRequest(http.MethodPost, "/", nil), &RPCRequest{JSONRPC: jsonRPCVersion, ID: 1, Method: "fees_getTransferQuote", Params: []json.RawMessage{params}})
		return decodeFeesTransferQuoteResult(t, recorder)
	}
	configured := node.TransferGasPolicy()
	before := quote("NHB")
	if before.FeeBps != configured.FeeBps || before.FeeWei != configured.ComputeFee("NHB", amount).String() {
		t.Fatalf("configured quote: %+v", before)
	}

	node.SetTransferFeeGovernance(1, 500)
	raw, _ := json.Marshal(governance.FeeScheduleRecord{
		Active: &governance.ScheduledFeeValues{FeeScheduleValues: governance.FeeScheduleValues{NHBFeeBps: 77, ZNHBFeeBps: 33, FreeAllowanceWei: "0"}, EffectiveHeight: 1, ProposalID: 1},
	})
	if err := node.WithState(func(m *nhbstate.Manager) error {
		return m.ParamStoreSet(governance.ParamKeyTransferFeeSchedule, raw)
	}); err != nil {
		t.Fatal(err)
	}
	nhb, znhb := quote("NHB"), quote("ZNHB")
	if nhb.FeeBps != 77 || nhb.Eligible || nhb.FeeWei != big.NewInt(1_000_000*77/10_000).String() {
		t.Fatalf("governed NHB quote: %+v", nhb)
	}
	if znhb.FeeBps != 33 || znhb.FeeWei != big.NewInt(1_000_000*33/10_000).String() {
		t.Fatalf("governed ZNHB quote: %+v", znhb)
	}

	// The allowance is the governed one too: a large governed allowance makes a fresh wallet eligible.
	raw, _ = json.Marshal(governance.FeeScheduleRecord{
		Active: &governance.ScheduledFeeValues{FeeScheduleValues: governance.FeeScheduleValues{NHBFeeBps: 77, ZNHBFeeBps: 33, FreeAllowanceWei: "5000000000"}, EffectiveHeight: 1, ProposalID: 2},
	})
	if err := node.WithState(func(m *nhbstate.Manager) error {
		return m.ParamStoreSet(governance.ParamKeyTransferFeeSchedule, raw)
	}); err != nil {
		t.Fatal(err)
	}
	if got := quote("NHB"); !got.Eligible || got.FeeWei != "0" {
		t.Fatalf("under a governed allowance of 5,000,000,000 a fresh wallet is free: %+v", got)
	}
}

// ---- the strict free allowance: reported by fees_getTransferSchedule, quoted by fees_getTransferQuote, flagged by status

func newStrictRPCServer(t *testing.T, strictAt uint64) (*Server, string) {
	t.Helper()
	server, wallet := newFeesQuoteTestServer(t)
	node := server.node
	policy := node.TransferGasPolicy()
	policy.Enabled = true
	policy.FreeSpendLimitWei, _ = new(big.Int).SetString("1000000000000000000000", 10)
	policy.FeeBps, policy.FeeBpsZNHB = 20, 10
	node.SetTransferGasPolicy(policy)
	node.SetTransferFeeStrictAllowance(strictAt)
	return server, wallet
}

func quoteOf(t *testing.T, server *Server, wallet, asset, amount string) feesTransferQuoteResult {
	t.Helper()
	params, _ := json.Marshal(map[string]string{"address": wallet, "asset": asset, "amountWei": amount})
	recorder := httptest.NewRecorder()
	server.handleFeesGetTransferQuote(recorder, httptest.NewRequest(http.MethodPost, "/", nil), &RPCRequest{JSONRPC: jsonRPCVersion, ID: 1, Method: "fees_getTransferQuote", Params: []json.RawMessage{params}})
	return decodeFeesTransferQuoteResult(t, recorder)
}

func TestFeesQuoteReportsTheStrictAllowance(t *testing.T) {
	const amount = "1100000000000000000000" // 1,100 tokens against an allowance of 1,000
	off, wallet := newStrictRPCServer(t, 0)
	got := quoteOf(t, off, wallet, "NHB", amount)
	if got.StrictAllowance || !got.Eligible || got.FeeWei != "0" || got.FreeWei != amount || got.ChargeableWei != "0" || got.FeeBps != 20 {
		t.Fatalf("rule off: %+v", got)
	}
	on, wallet := newStrictRPCServer(t, 1)
	got = quoteOf(t, on, wallet, "NHB", amount)
	if !got.StrictAllowance || got.Eligible || got.FreeWei != "1000000000000000000000" || got.ChargeableWei != "100000000000000000000" || got.FeeWei != "200000000000000000" {
		t.Fatalf("rule on, NHB: %+v", got)
	}
	got = quoteOf(t, on, wallet, "ZNHB", amount)
	if !got.StrictAllowance || got.FeeBps != 10 || got.FeeWei != "100000000000000000" {
		t.Fatalf("rule on, ZNHB: %+v", got)
	}
	// Fully inside the allowance: eligible, nothing to charge, under either rule.
	got = quoteOf(t, on, wallet, "NHB", "500000000000000000000")
	if !got.Eligible || got.FeeWei != "0" || got.ChargeableWei != "0" || got.FreeWei != "500000000000000000000" {
		t.Fatalf("inside the allowance: %+v", got)
	}
	// A first transfer of 1,000,000 tokens pays on 999,000 of them.
	got = quoteOf(t, on, wallet, "NHB", "1000000000000000000000000")
	if got.ChargeableWei != "999000000000000000000000" || got.FeeWei != "1998000000000000000000" {
		t.Fatalf("1,000,000 tokens: %+v", got)
	}
}

func TestFeesStatusAndScheduleReportTheStrictAllowance(t *testing.T) {
	for _, strictAt := range []uint64{0, 1} {
		server, wallet := newStrictRPCServer(t, strictAt)
		params, _ := json.Marshal(map[string]string{"address": wallet})
		recorder := httptest.NewRecorder()
		server.handleFeesGetTransferStatus(recorder, httptest.NewRequest(http.MethodPost, "/", nil), &RPCRequest{JSONRPC: jsonRPCVersion, ID: 1, Method: "fees_getTransferStatus", Params: []json.RawMessage{params}})
		var resp RPCResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil || resp.Error != nil {
			t.Fatalf("status response: %v %+v", err, resp.Error)
		}
		raw, _ := json.Marshal(resp.Result)
		var status feesTransferStatusResult
		if err := json.Unmarshal(raw, &status); err != nil {
			t.Fatal(err)
		}
		if status.StrictAllowance != (strictAt != 0) || status.RemainingWei != "1000000000000000000000" || !status.Eligible {
			t.Fatalf("strictAt %d: status %+v", strictAt, status)
		}
		view, err := server.node.TransferFeeSchedule()
		if err != nil {
			t.Fatal(err)
		}
		if view.StrictAllowanceActive != (strictAt != 0) || view.StrictAllowanceActivationHeight != strictAt {
			t.Fatalf("strictAt %d: view %+v", strictAt, view)
		}
	}
}

func TestFeesGetTransferScheduleReportsTheStrictAllowance(t *testing.T) {
	env := newTestEnv(t)
	if got := callFeesSchedule(t, env); got.StrictAllowance.Active || got.StrictAllowance.ActivationHeight != 0 {
		t.Fatalf("off by default: %+v", got.StrictAllowance)
	}
	env.node.SetTransferFeeStrictAllowance(env.node.GetHeight() + 5)
	got := callFeesSchedule(t, env)
	if got.StrictAllowance.Active || got.StrictAllowance.ActivationHeight != env.node.GetHeight()+5 {
		t.Fatalf("scheduled ahead: %+v", got.StrictAllowance)
	}
	env.node.SetTransferFeeStrictAllowance(env.node.GetHeight() + 1)
	if got := callFeesSchedule(t, env); !got.StrictAllowance.Active {
		t.Fatalf("the next block is the activation height: %+v", got.StrictAllowance)
	}
}
