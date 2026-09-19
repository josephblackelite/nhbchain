package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/governance"
)

// DC-02 regression: market.flatFeeWei is persisted exactly as the param.update
// proposal spelled it (a quoted decimal string passes proposal validation), and
// market_getFlatFee must report the amount, not the quoted spelling.
func TestHandleMarketGetFlatFeeAcceptsQuotedGovernedValue(t *testing.T) {
	for _, stored := range []string{
		"250000000000000000",
		`"250000000000000000"`,
		` "250000000000000000" `,
		`"+250000000000000000"`,
	} {
		env := newTestEnv(t)
		if err := env.node.WithState(func(manager *nhbstate.Manager) error {
			return manager.ParamStoreSet(governance.ParamKeyMarketFlatFeeWei, []byte(stored))
		}); err != nil {
			t.Fatalf("seed governed flat fee %q: %v", stored, err)
		}

		recorder := httptest.NewRecorder()
		env.server.handleMarketGetFlatFee(recorder, env.newRequest(), &RPCRequest{ID: 1})
		result, rpcErr := decodeRPCResponse(t, recorder)
		if rpcErr != nil {
			t.Fatalf("stored %q: unexpected rpc error: %+v", stored, rpcErr)
		}
		var decoded struct {
			FlatFeeWei string `json:"flatFeeWei"`
		}
		if err := json.Unmarshal(result, &decoded); err != nil {
			t.Fatalf("stored %q: decode result: %v", stored, err)
		}
		if decoded.FlatFeeWei != "250000000000000000" {
			t.Fatalf("stored %q: expected flat fee 250000000000000000, got %q", stored, decoded.FlatFeeWei)
		}
	}
}
