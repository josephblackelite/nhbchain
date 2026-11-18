package rpc

import (
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"strings"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/market"
)

// PL-BC-B2: amm_getTicker, market_getTicker and market_getRecentFills answer anonymously, report exact numbers from
// state, validate their parameters and never write.

func seedIndexedFill(t *testing.T, env *testEnv, tag byte, znhb, nhb int64, createdAt int64, height uint64) *market.Fill {
	t.Helper()
	var buyer, seller [20]byte
	buyer[0], seller[0], buyer[19], seller[19] = 0xB0, 0x5E, tag, tag
	fill := &market.Fill{
		ListingID:   [32]byte{0xAA, tag},
		Buyer:       crypto.MustNewAddress(crypto.NHBPrefix, buyer[:]),
		Seller:      crypto.MustNewAddress(crypto.NHBPrefix, seller[:]),
		ZNHBAmount:  big.NewInt(znhb),
		NHBAmount:   big.NewInt(nhb),
		FeeAmount:   big.NewInt(100),
		CreatedAt:   createdAt,
		BlockHeight: height,
	}
	fill.ID = [32]byte{0xF1, tag}
	fill.TxHash = [32]byte{0x7A, tag}
	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		if err := manager.AppendMarketFill(fill); err != nil {
			return err
		}
		return manager.AppendMarketFillGlobal(fill)
	}); err != nil {
		t.Fatalf("seed fill: %v", err)
	}
	return fill
}

func callRPC(t *testing.T, env *testEnv, handler func(*httptest.ResponseRecorder, *RPCRequest), params ...json.RawMessage) (json.RawMessage, *RPCError) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler(recorder, &RPCRequest{ID: 1, Params: params})
	return decodeRPCResponse(t, recorder)
}

func TestHandleMarketTickerOnAnEmptyMarket(t *testing.T) {
	env := newTestEnv(t)
	result, rpcErr := callRPC(t, env, func(w *httptest.ResponseRecorder, req *RPCRequest) {
		env.server.handleMarketTicker(w, env.newRequest(), req)
	})
	if rpcErr != nil {
		t.Fatalf("rpc error: %+v", rpcErr)
	}
	var got marketTickerResult
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatal(err)
	}
	if got.IndexActive || got.IndexStartHeight != 0 || got.OpenListings != 0 || got.OpenZnhbWei != "0" || got.BestAsk != "" || got.FillCount != 0 || got.LastPrice != "" || got.Fills24h != 0 {
		t.Fatalf("an empty market must report nothing: %+v", got)
	}
}

func TestHandleMarketTickerAndRecentFillsReportTheIndex(t *testing.T) {
	env := newTestEnv(t)
	seller := crypto.MustNewAddress(crypto.NHBPrefix, []byte("seller-addr-20-bytes"))
	seedMarketListing(t, env, seller, 1000, 900, 2, 1, true, 5) // rate 2 ZNHB per NHB: ask 0.5 NHB per ZNHB
	seedIndexedFill(t, env, 1, 150, 150, 1672530200, 10)        // price 1.0
	seedIndexedFill(t, env, 2, 100, 50, 1672530700, 11)         // price 0.5, the newest

	result, rpcErr := callRPC(t, env, func(w *httptest.ResponseRecorder, req *RPCRequest) {
		env.server.handleMarketTicker(w, env.newRequest(), req)
	})
	if rpcErr != nil {
		t.Fatalf("rpc error: %+v", rpcErr)
	}
	var tk marketTickerResult
	if err := json.Unmarshal(result, &tk); err != nil {
		t.Fatal(err)
	}
	if tk.OpenListings != 1 || tk.OpenZnhbWei != "900" || tk.BestAsk != "0.500000000000000000" || tk.VWAPAsk != "0.500000000000000000" {
		t.Fatalf("asks wrong: %+v", tk)
	}
	if tk.FillCount != 2 || tk.LastPrice != "0.500000000000000000" || tk.LastFillHeight != 11 || tk.LastFillTime != 1672530700 {
		t.Fatalf("last fill wrong: %+v", tk)
	}
	if tk.VolumeZnhb24h != "250" || tk.VolumeNhb24h != "200" || tk.Fills24h != 2 || tk.High24h != "1.000000000000000000" || tk.Low24h != "0.500000000000000000" {
		t.Fatalf("24 h numbers wrong: %+v", tk)
	}

	recent, rpcErr := callRPC(t, env, func(w *httptest.ResponseRecorder, req *RPCRequest) {
		env.server.handleMarketRecentFills(w, env.newRequest(), req)
	})
	if rpcErr != nil {
		t.Fatalf("rpc error: %+v", rpcErr)
	}
	var list struct {
		Fills []marketFillResult `json:"fills"`
	}
	if err := json.Unmarshal(recent, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Fills) != 2 || list.Fills[0].ZNHBAmount != "100" || list.Fills[1].ZNHBAmount != "150" || list.Fills[0].CreatedAt != 1672530700 {
		t.Fatalf("recent fills must be newest first with their times: %+v", list.Fills)
	}
	if !strings.HasPrefix(list.Fills[0].TxHash, "0x7a02") || !strings.HasPrefix(list.Fills[0].Buyer, "nhb1") {
		t.Fatalf("a fill carries its transaction hash and bech32 parties: %+v", list.Fills[0])
	}

	one, rpcErr := callRPC(t, env, func(w *httptest.ResponseRecorder, req *RPCRequest) {
		env.server.handleMarketRecentFills(w, env.newRequest(), req)
	}, json.RawMessage("1"))
	if rpcErr != nil {
		t.Fatalf("rpc error: %+v", rpcErr)
	}
	var limited struct {
		Fills []marketFillResult `json:"fills"`
	}
	if err := json.Unmarshal(one, &limited); err != nil || len(limited.Fills) != 1 || limited.Fills[0].ZNHBAmount != "100" {
		t.Fatalf("a limit of 1 must return only the newest fill: %s (%v)", one, err)
	}
}

func TestMarketDataHandlersRejectBadParameters(t *testing.T) {
	env := newTestEnv(t)
	bad := func(name string, handler func(*httptest.ResponseRecorder, *RPCRequest), params ...json.RawMessage) {
		t.Helper()
		if _, rpcErr := callRPC(t, env, handler, params...); rpcErr == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
	bad("market_getTicker with a parameter", func(w *httptest.ResponseRecorder, req *RPCRequest) {
		env.server.handleMarketTicker(w, env.newRequest(), req)
	}, json.RawMessage("1"))
	bad("amm_getTicker with a parameter", func(w *httptest.ResponseRecorder, req *RPCRequest) {
		env.server.handleAmmTicker(w, env.newRequest(), req)
	}, json.RawMessage("1"))
	recent := func(w *httptest.ResponseRecorder, req *RPCRequest) {
		env.server.handleMarketRecentFills(w, env.newRequest(), req)
	}
	bad("a limit of 0", recent, json.RawMessage("0"))
	bad("a negative limit", recent, json.RawMessage("-3"))
	bad("a limit that is not a number", recent, json.RawMessage(`"ten"`))
	bad("two parameters", recent, json.RawMessage("1"), json.RawMessage("2"))
}

func TestHandleAmmTickerAndTradesWhenThePoolIsOff(t *testing.T) {
	env := newTestEnv(t)
	result, rpcErr := callRPC(t, env, func(w *httptest.ResponseRecorder, req *RPCRequest) {
		env.server.handleAmmTicker(w, env.newRequest(), req)
	})
	if rpcErr != nil {
		t.Fatalf("rpc error: %+v", rpcErr)
	}
	var tk ammTickerResult
	if err := json.Unmarshal(result, &tk); err != nil {
		t.Fatal(err)
	}
	if tk.Active || tk.SwapsOpen || tk.PriceNhbPerZnhb != "" || tk.LastTradePrice != "" || tk.TradeCount != 0 || tk.Trades24h != 0 {
		t.Fatalf("an unconfigured pool reports nothing: %+v", tk)
	}
	if !strings.Contains(string(result), `"ladder":[]`) {
		t.Fatalf("the ladder must be an empty list, not null: %s", result)
	}

	trades, rpcErr := callRPC(t, env, func(w *httptest.ResponseRecorder, req *RPCRequest) {
		env.server.handleAmmTrades(w, env.newRequest(), req)
	})
	if rpcErr != nil {
		t.Fatalf("rpc error: %+v", rpcErr)
	}
	if strings.TrimSpace(string(trades)) != "[]" {
		t.Fatalf("an unused pool has no trades: %s", trades)
	}
}
