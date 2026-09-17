package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/governance"
)

type lendingRateScheduleEntryResponse struct {
	TenureDays uint64 `json:"tenureDays"`
	RateBps    uint64 `json:"rateBps"`
}

type lendingRateScheduleResponse struct {
	Schedule []lendingRateScheduleEntryResponse `json:"schedule"`
}

// TestHandleLendingGetRateScheduleDefaults confirms the public
// lending_getRateSchedule method reports the conservative built-in default
// (native/lending.DefaultFixedTermRateSchedule) when no
// policy.lendingRateSchedule governance proposal has ever executed, sorted
// by tenureDays for a stable response shape.
func TestHandleLendingGetRateScheduleDefaults(t *testing.T) {
	env := newTestEnv(t)

	req := &RPCRequest{ID: 1}
	recorder := httptest.NewRecorder()
	env.server.handleLendingGetRateSchedule(recorder, env.newRequest(), req)

	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp lendingRateScheduleResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Schedule) != 2 {
		t.Fatalf("expected 2 default tenure entries, got %d", len(resp.Schedule))
	}
	if resp.Schedule[0].TenureDays != 30 || resp.Schedule[0].RateBps != 1200 {
		t.Fatalf("expected first entry 30d/1200bps, got %+v", resp.Schedule[0])
	}
	if resp.Schedule[1].TenureDays != 90 || resp.Schedule[1].RateBps != 1600 {
		t.Fatalf("expected second entry 90d/1600bps, got %+v", resp.Schedule[1])
	}
}

// TestHandleLendingGetRateScheduleReflectsGovernedValues confirms the
// method reflects a governed schedule the moment it is set in the param
// store (the same store a passed policy.lendingRateSchedule proposal writes
// to), rather than a cached snapshot -- and that entries are still returned
// sorted by tenureDays even when stored out of order.
func TestHandleLendingGetRateScheduleReflectsGovernedValues(t *testing.T) {
	env := newTestEnv(t)

	governed := governance.LendingRateSchedulePayload{
		Schedule: []governance.LendingTenureRate{
			{TenureDays: 90, RateBps: 2500},
			{TenureDays: 30, RateBps: 900},
			{TenureDays: 7, RateBps: 150},
		},
	}
	encoded, err := json.Marshal(governed)
	if err != nil {
		t.Fatalf("encode governed schedule: %v", err)
	}
	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		return m.ParamStoreSet(governance.ParamKeyLendingFixedTermRateSchedule, encoded)
	}); err != nil {
		t.Fatalf("seed governed rate schedule: %v", err)
	}

	req := &RPCRequest{ID: 2}
	recorder := httptest.NewRecorder()
	env.server.handleLendingGetRateSchedule(recorder, env.newRequest(), req)

	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp lendingRateScheduleResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	want := []lendingRateScheduleEntryResponse{
		{TenureDays: 7, RateBps: 150},
		{TenureDays: 30, RateBps: 900},
		{TenureDays: 90, RateBps: 2500},
	}
	if len(resp.Schedule) != len(want) {
		t.Fatalf("expected %d entries, got %d: %+v", len(want), len(resp.Schedule), resp.Schedule)
	}
	for i, entry := range want {
		if resp.Schedule[i] != entry {
			t.Fatalf("entry %d: expected %+v, got %+v", i, entry, resp.Schedule[i])
		}
	}
}

// TestHandleLendingGetRateScheduleRejectsParams confirms the method takes
// no parameters, matching handleSwapGetRiskParams's own precedent.
func TestHandleLendingGetRateScheduleRejectsParams(t *testing.T) {
	env := newTestEnv(t)

	req := &RPCRequest{ID: 3, Params: []json.RawMessage{json.RawMessage(`1`)}}
	recorder := httptest.NewRecorder()
	env.server.handleLendingGetRateSchedule(recorder, env.newRequest(), req)

	if _, rpcErr := decodeRPCResponse(t, recorder); rpcErr == nil {
		t.Fatalf("expected an error when parameters are supplied")
	}
}
