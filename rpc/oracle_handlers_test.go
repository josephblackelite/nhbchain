package rpc

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"nhbchain/services/swapd/stable"
)

func TestHandleGetOraclePriceReportsStaleness(t *testing.T) {
	env := newTestEnv(t)

	assets := []stable.Asset{
		{Symbol: "ZNHB", BasePair: "ZNHB", QuotePair: "USD", QuoteTTL: time.Minute, MaxSlippageBps: 50, SoftInventory: 1000},
	}
	engine, err := stable.NewEngine(assets, stable.Limits{}, nil)
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	now := time.Unix(1700000000, 0).UTC()
	engine.WithClock(func() time.Time { return now })
	engine.SetPriceMaxAge(time.Minute)
	// Record a quote that is already older than the freshness window so the
	// refresh loop is effectively "frozen" (the bug being fixed).
	engine.RecordPrice("USD", "ZNHB", 0.05, now.Add(-2*time.Minute))
	env.server.ConfigureStableEngine(engine, stable.Limits{}, assets, func() time.Time { return now })

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, "ZNHB")}}
	recorder := httptest.NewRecorder()
	env.server.handleGetOraclePrice(recorder, env.newRequest(), req)
	raw, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var resp struct {
		Price float64 `json:"price"`
		Stale bool    `json:"stale"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !resp.Stale {
		t.Fatalf("expected stale=true for a quote older than the freshness window")
	}
	if resp.Price != 0.05 {
		t.Fatalf("expected the stale price value to still be returned unchanged, got %v", resp.Price)
	}

	// A fresh observation should report stale=false.
	engine.RecordPrice("USD", "ZNHB", 0.06, now)
	recorder2 := httptest.NewRecorder()
	env.server.handleGetOraclePrice(recorder2, env.newRequest(), req)
	raw2, rpcErr2 := decodeRPCResponse(t, recorder2)
	if rpcErr2 != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr2)
	}
	var resp2 struct {
		Price float64 `json:"price"`
		Stale bool    `json:"stale"`
	}
	if err := json.Unmarshal(raw2, &resp2); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp2.Stale {
		t.Fatalf("expected stale=false for a fresh quote")
	}
	if resp2.Price != 0.06 {
		t.Fatalf("expected updated price, got %v", resp2.Price)
	}
}
