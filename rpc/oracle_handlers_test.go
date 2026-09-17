package rpc

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"nhbchain/stablequote"
)

// fakeOraclePriceEngine implements stablequote.Engine using only
// CurrentPrice's return values -- handleGetOraclePrice never calls the
// other four methods, and this test only needs to control staleness, so
// a real engine (now living in the private nhbchain-services repo,
// unreachable from this repo's own tests) is unnecessary. Price/Reserve/
// CashOut/Status are unused stubs to satisfy the interface.
type fakeOraclePriceEngine struct {
	rate      float64
	updatedAt time.Time
	stale     bool
	ok        bool
}

func (f *fakeOraclePriceEngine) Price(context.Context, stablequote.QuoteRequest) (stablequote.QuoteResponse, error) {
	return stablequote.QuoteResponse{}, nil
}

func (f *fakeOraclePriceEngine) Reserve(context.Context, stablequote.ReserveRequest) (stablequote.ReserveResponse, error) {
	return stablequote.ReserveResponse{}, nil
}

func (f *fakeOraclePriceEngine) CashOut(context.Context, stablequote.CashOutRequest) (stablequote.CashOutResponse, error) {
	return stablequote.CashOutResponse{}, nil
}

func (f *fakeOraclePriceEngine) Status(context.Context) stablequote.Status {
	return stablequote.Status{}
}

func (f *fakeOraclePriceEngine) CurrentPrice(base, quote string) (float64, time.Time, bool, bool) {
	return f.rate, f.updatedAt, f.stale, f.ok
}

func TestHandleGetOraclePriceReportsStaleness(t *testing.T) {
	env := newTestEnv(t)

	assets := []stablequote.Asset{
		{Symbol: "ZNHB", BasePair: "ZNHB", QuotePair: "USD", QuoteTTL: time.Minute, MaxSlippageBps: 50, SoftInventory: 1000},
	}
	now := time.Unix(1700000000, 0).UTC()
	// Record a quote that is already older than the freshness window so the
	// refresh loop is effectively "frozen" (the bug being fixed).
	engine := &fakeOraclePriceEngine{rate: 0.05, updatedAt: now.Add(-2 * time.Minute), stale: true, ok: true}
	env.server.ConfigureStableEngine(engine, stablequote.Limits{}, assets, func() time.Time { return now })

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
	engine.rate, engine.updatedAt, engine.stale = 0.06, now, false
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
