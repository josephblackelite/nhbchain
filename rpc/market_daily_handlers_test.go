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

// PL-BC-B2: market_getDailyStats answers anonymously from the per-day rows of the market-wide fill record.

func TestHandleMarketDailyStatsReportsDaysNewestFirst(t *testing.T) {
	env := newTestEnv(t)
	var buyer, seller [20]byte
	buyer[0], seller[0] = 0xB0, 0x5E
	put := func(tag byte, createdAt int64, znhb, nhb int64) {
		fill := &market.Fill{
			Buyer: crypto.MustNewAddress(crypto.NHBPrefix, buyer[:]), Seller: crypto.MustNewAddress(crypto.NHBPrefix, seller[:]),
			ZNHBAmount: big.NewInt(znhb), NHBAmount: big.NewInt(nhb), FeeAmount: big.NewInt(0), CreatedAt: createdAt,
		}
		fill.ID = [32]byte{0xDA, tag}
		if err := env.node.WithState(func(manager *nhbstate.Manager) error {
			if err := manager.AppendMarketFill(fill); err != nil {
				return err
			}
			return manager.AppendMarketFillGlobal(fill)
		}); err != nil {
			t.Fatalf("seed fill: %v", err)
		}
	}
	put(1, 20_000*86_400+10, 100, 40) // day 20000, price 0.4
	put(2, 20_000*86_400+20, 100, 50) // same day, price 0.5
	put(3, 20_002*86_400+5, 10, 3)    // day 20002 (20001 is quiet), price 0.3

	call := func(params ...json.RawMessage) (json.RawMessage, *RPCError) {
		recorder := httptest.NewRecorder()
		env.server.handleMarketDailyStats(recorder, env.newRequest(), &RPCRequest{ID: 1, Params: params})
		return decodeRPCResponse(t, recorder)
	}
	result, rpcErr := call()
	if rpcErr != nil {
		t.Fatalf("rpc error: %+v", rpcErr)
	}
	var got struct {
		Days []marketDayResult `json:"days"`
	}
	if err := json.Unmarshal(result, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Days) != 2 || got.Days[0].DayStart != 20_002*86_400 || got.Days[1].DayStart != 20_000*86_400 {
		t.Fatalf("two days with fills, newest first and quiet days skipped: %+v", got.Days)
	}
	d := got.Days[1]
	if d.Open != "0.400000000000000000" || d.High != "0.500000000000000000" || d.Low != "0.400000000000000000" || d.Close != "0.500000000000000000" ||
		d.VolumeZnhbWei != "200" || d.VolumeNhbWei != "90" || d.Fills != 2 {
		t.Fatalf("day 20000 wrong: %+v", d)
	}
	if got.Days[0].Open != "0.300000000000000000" || got.Days[0].Fills != 1 {
		t.Fatalf("day 20002 wrong: %+v", got.Days[0])
	}

	one, rpcErr := call(json.RawMessage("1"))
	if rpcErr != nil {
		t.Fatalf("rpc error: %+v", rpcErr)
	}
	var limited struct {
		Days []marketDayResult `json:"days"`
	}
	if err := json.Unmarshal(one, &limited); err != nil || len(limited.Days) != 1 {
		t.Fatalf("a request for one day returns one: %s", one)
	}
	for name, params := range map[string][]json.RawMessage{
		"zero days": {json.RawMessage("0")}, "negative": {json.RawMessage("-2")}, "text": {json.RawMessage(`"week"`)}, "two parameters": {json.RawMessage("1"), json.RawMessage("2")},
	} {
		if _, rpcErr := call(params...); rpcErr == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}

func TestHandleMarketDailyStatsOnAMarketWithNoRecordIsAnEmptyList(t *testing.T) {
	env := newTestEnv(t)
	recorder := httptest.NewRecorder()
	env.server.handleMarketDailyStats(recorder, env.newRequest(), &RPCRequest{ID: 1})
	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("rpc error: %+v", rpcErr)
	}
	if !strings.Contains(string(result), `"days":[]`) {
		t.Fatalf("an empty record is an empty list, not null: %s", result)
	}
}
