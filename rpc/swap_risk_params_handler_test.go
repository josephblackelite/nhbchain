package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/governance"
)

type swapRiskParamsSideResponse struct {
	PerTxMinWei             string `json:"perTxMinWei"`
	PerTxMaxWei             string `json:"perTxMaxWei"`
	PerAddressDailyCapWei   string `json:"perAddressDailyCapWei"`
	PerAddressMonthlyCapWei string `json:"perAddressMonthlyCapWei"`
}

type swapRiskParamsResponse struct {
	Redeem swapRiskParamsSideResponse `json:"redeem"`
}

// TestHandleSwapGetRiskParamsDefaults confirms the public swap_getRiskParams
// method reports the conservative built-in defaults
// (native/swap/redeem_risk.go's DefaultRedeem*) when no
// policy.swapRiskParams governance proposal has ever executed.
func TestHandleSwapGetRiskParamsDefaults(t *testing.T) {
	env := newTestEnv(t)

	req := &RPCRequest{ID: 1}
	recorder := httptest.NewRecorder()
	env.server.handleSwapGetRiskParams(recorder, env.newRequest(), req)

	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp swapRiskParamsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Redeem.PerTxMaxWei != "1000000000000000000000" {
		t.Fatalf("expected default redeem per-tx max 1000e18, got %s", resp.Redeem.PerTxMaxWei)
	}
	if resp.Redeem.PerAddressMonthlyCapWei != "20000000000000000000000" {
		t.Fatalf("expected default redeem monthly cap 20000e18, got %s", resp.Redeem.PerAddressMonthlyCapWei)
	}
}

// TestHandleSwapGetRiskParamsReflectsGovernedValues confirms the method
// reflects a governed value the moment it is set in the param store (the
// same store a passed policy.swapRiskParams proposal writes to), rather than
// a cached snapshot.
func TestHandleSwapGetRiskParamsReflectsGovernedValues(t *testing.T) {
	env := newTestEnv(t)

	if err := env.node.WithState(func(m *nhbstate.Manager) error {
		return m.ParamStoreSet(governance.ParamKeySwapRiskRedeemPerTxMaxWei, []byte("12345"))
	}); err != nil {
		t.Fatalf("seed governed redeem per-tx max: %v", err)
	}

	req := &RPCRequest{ID: 2}
	recorder := httptest.NewRecorder()
	env.server.handleSwapGetRiskParams(recorder, env.newRequest(), req)

	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp swapRiskParamsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Redeem.PerTxMaxWei != "12345" {
		t.Fatalf("expected governed redeem per-tx max 12345, got %s", resp.Redeem.PerTxMaxWei)
	}
	// Every other value must still be at its untouched default.
	if resp.Redeem.PerAddressDailyCapWei != "2000000000000000000000" {
		t.Fatalf("expected redeem daily cap to remain at its default 2000e18, got %s", resp.Redeem.PerAddressDailyCapWei)
	}
}
