package engine

import (
	"context"
	"encoding/json"
	"errors"
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

// TestMutationsRelaySignedTxVerbatim proves the trust-model fix: Supply,
// Withdraw, Borrow, Repay, DepositCollateral, WithdrawCollateral, and
// Liquidate no longer call the disabled lending_* RPC methods with a bare
// account string (rpc/lending_handlers.go, HTTP 410) -- they submit the
// caller's own pre-signed transaction via nhb_sendTransaction, relayed
// byte-for-byte, and return the mempool-accepted hash the node responds
// with. The fake node asserts the exact signed tx JSON survives the trip
// unmodified.
func TestMutationsRelaySignedTxVerbatim(t *testing.T) {
	type rpcRequestBody struct {
		Method string            `json:"method"`
		Params []json.RawMessage `json:"params"`
		ID     int               `json:"id"`
	}

	const signedTx = `{"chainId":"5124680","type":19,"nonce":3,"to":"","value":"1000","data":"","gasLimit":50000,"gasPrice":"1","r":"1","s":"2","v":"27"}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body rpcRequestBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Method != "nhb_sendTransaction" {
			t.Fatalf("expected nhb_sendTransaction, got %q", body.Method)
		}
		if len(body.Params) != 1 {
			t.Fatalf("expected exactly one param, got %d", len(body.Params))
		}
		if string(body.Params[0]) != signedTx {
			t.Fatalf("signed tx was not relayed verbatim:\n got: %s\nwant: %s", body.Params[0], signedTx)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0xabc123"}`))
	}))
	defer server.Close()

	client, err := rpcclient.NewClient(rpcclient.Config{BaseURL: server.URL, AllowInsecure: true})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	adapter := NewNodeAdapter(client)
	ctx := context.Background()

	cases := []struct {
		name string
		call func() (string, error)
	}{
		{"Supply", func() (string, error) { return adapter.Supply(ctx, "nhb1acct", "default", "1000", signedTx) }},
		{"Withdraw", func() (string, error) { return adapter.Withdraw(ctx, "nhb1acct", "default", "1000", signedTx) }},
		{"Borrow", func() (string, error) { return adapter.Borrow(ctx, "nhb1acct", "default", "1000", signedTx) }},
		{"Repay", func() (string, error) { return adapter.Repay(ctx, "nhb1acct", "default", "1000", signedTx) }},
		{"DepositCollateral", func() (string, error) {
			return adapter.DepositCollateral(ctx, "nhb1acct", "default", "1000", signedTx)
		}},
		{"WithdrawCollateral", func() (string, error) {
			return adapter.WithdrawCollateral(ctx, "nhb1acct", "default", "1000", signedTx)
		}},
		{"Liquidate", func() (string, error) {
			return adapter.Liquidate(ctx, "nhb1liquidator", "nhb1borrower", "default", signedTx)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hash, err := tc.call()
			if err != nil {
				t.Fatalf("expected success, got %v", err)
			}
			if hash != "0xabc123" {
				t.Fatalf("expected relayed tx hash, got %q", hash)
			}
		})
	}
}

// TestMutationsRejectEmptyOrInvalidSignedTx proves lendingd fails fast on a
// missing or malformed signed transaction instead of forwarding garbage to
// the node.
func TestMutationsRejectEmptyOrInvalidSignedTx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatalf("node should never be called with an invalid signed tx")
	}))
	defer server.Close()

	client, err := rpcclient.NewClient(rpcclient.Config{BaseURL: server.URL, AllowInsecure: true})
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	adapter := NewNodeAdapter(client)
	ctx := context.Background()

	if _, err := adapter.Supply(ctx, "nhb1acct", "default", "1000", ""); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("expected ErrInvalidAmount for empty signed tx, got %v", err)
	}
	if _, err := adapter.Supply(ctx, "nhb1acct", "default", "1000", "not json"); !errors.Is(err, ErrInvalidAmount) {
		t.Fatalf("expected ErrInvalidAmount for malformed signed tx, got %v", err)
	}
}
