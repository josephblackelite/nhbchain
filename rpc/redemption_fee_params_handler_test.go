package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/governance"
)

type redemptionFeeParamsResponse struct {
	FeeBps      uint32 `json:"feeBps"`
	FeeFloorWei string `json:"feeFloorWei"`
	FeeCapWei   string `json:"feeCapWei"`
}

// TestHandleSwapGetRedemptionFeeParamsDefaults confirms the public
// swap_getRedemptionFeeParams method reports the built-in defaults
// (native/swap/redemption_fee.go's Default* constants) when no
// policy.redemptionFeeParams governance proposal has ever executed.
func TestHandleSwapGetRedemptionFeeParamsDefaults(t *testing.T) {
	env := newTestEnv(t)

	req := &RPCRequest{ID: 1}
	recorder := httptest.NewRecorder()
	env.server.handleSwapGetRedemptionFeeParams(recorder, env.newRequest(), req)

	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp redemptionFeeParamsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.FeeBps != 100 {
		t.Fatalf("expected default fee rate 100bps (1%%), got %d", resp.FeeBps)
	}
	if resp.FeeFloorWei != "1000000000000000000" {
		t.Fatalf("expected default fee floor 1e18 ($1), got %s", resp.FeeFloorWei)
	}
	if resp.FeeCapWei != "1000000000000000000000" {
		t.Fatalf("expected default fee cap 1000e18 ($1000), got %s", resp.FeeCapWei)
	}
}

// TestHandleSwapGetRedemptionFeeParamsReflectsGovernedValues confirms the
// method reflects a governed value the moment it is set in the param store
// (the same store a passed policy.redemptionFeeParams proposal writes to),
// rather than a cached snapshot.
func TestHandleSwapGetRedemptionFeeParamsReflectsGovernedValues(t *testing.T) {
	env := newTestEnv(t)

	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		return m.ParamStoreSet(governance.ParamKeyRedemptionFeeBps, []byte("250"))
	}); err != nil {
		t.Fatalf("seed governed redemption fee bps: %v", err)
	}

	req := &RPCRequest{ID: 2}
	recorder := httptest.NewRecorder()
	env.server.handleSwapGetRedemptionFeeParams(recorder, env.newRequest(), req)

	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp redemptionFeeParamsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.FeeBps != 250 {
		t.Fatalf("expected governed fee rate 250bps, got %d", resp.FeeBps)
	}
	// The floor/cap must still be at their untouched defaults.
	if resp.FeeFloorWei != "1000000000000000000" {
		t.Fatalf("expected fee floor to remain at its default 1e18, got %s", resp.FeeFloorWei)
	}
	if resp.FeeCapWei != "1000000000000000000000" {
		t.Fatalf("expected fee cap to remain at its default 1000e18, got %s", resp.FeeCapWei)
	}
}
