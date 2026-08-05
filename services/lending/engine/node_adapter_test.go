package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"nhbchain/services/lending/engine/rpcclient"
)

// TestGetHealthDerivesFromWorkingMethods proves the actual fix: GetHealth
// used to call lending_getHealth, a JSON-RPC method that has never existed
// on the node (rpc/http.go's method switch has no such case -- every call
// failed with "unknown method"). This test runs a fake node that only
// understands the two real methods GetHealth now composes itself from
// (lending_getUserAccount, lending_getMarket) and asserts the combined
// result carries a correctly computed health factor -- collateral 200,
// debt 50 -> health factor 4.
func TestGetHealthDerivesFromWorkingMethods(t *testing.T) {
	type rpcRequestBody struct {
		Method string `json:"method"`
		Params any    `json:"params"`
		ID     int    `json:"id"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body rpcRequestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		switch body.Method {
		case "lending_getUserAccount":
			_, _ = w.Write([]byte(`{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"account": {
						"address": "nhb1testaddress",
						"collateralZNHB": "200",
						"supplyShares": "0",
						"debtNHB": "50"
					}
				}
			}`))
		case "lending_getMarket":
			_, _ = w.Write([]byte(`{
				"jsonrpc": "2.0",
				"id": 1,
				"result": {
					"market": {"key": {"symbol": "default"}, "baseAsset": "NHB"},
					"riskParameters": {"maxLTV": 7500, "liquidationThreshold": 8500}
				}
			}`))
		default:
			t.Fatalf("unexpected method called: %s", body.Method)
		}
	}))
	defer server.Close()

	client, err := rpcclient.NewClient(rpcclient.Config{BaseURL: server.URL, AllowInsecure: true})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	adapter := NewNodeAdapter(client)

	health, err := adapter.GetHealth(context.Background(), "nhb1testaddress")
	if err != nil {
		t.Fatalf("get health: %v", err)
	}
	if health.Account == nil {
		t.Fatalf("expected account to be populated")
	}
	if health.HealthFactor != "4" {
		t.Fatalf("expected health factor 4 (200/50), got %q", health.HealthFactor)
	}
	if health.Market == nil {
		t.Fatalf("expected market to be populated")
	}
	if health.RiskParameters.MaxLTV != 7500 {
		t.Fatalf("expected risk parameters to be populated, got %+v", health.RiskParameters)
	}
}
