package rpc

import (
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http/httptest"
	"testing"

	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/market"
)

func seedMarketListing(t *testing.T, env *testEnv, seller crypto.Address, total, remaining, num, den int64, allowPartial bool, createdAt int64) *market.Listing {
	t.Helper()
	listing := &market.Listing{
		Seller:          seller,
		RateNumerator:   big.NewInt(num),
		RateDenominator: big.NewInt(den),
		TotalAmount:     big.NewInt(total),
		RemainingAmount: big.NewInt(remaining),
		AllowPartial:    allowPartial,
		Status:          market.ListingOpen,
		CreatedAt:       createdAt,
		UpdatedAt:       createdAt,
	}
	copy(listing.ID[:], []byte("listing-id-"+seller.String()))
	if err := env.node.WithState(func(manager *nhbstate.Manager) error {
		if err := manager.PutMarketListing(listing); err != nil {
			return err
		}
		return manager.AppendOpenMarketListing(listing.ID)
	}); err != nil {
		t.Fatalf("seed listing: %v", err)
	}
	return listing
}

func TestHandleMarketListOpenListingsEmpty(t *testing.T) {
	env := newTestEnv(t)
	req := &RPCRequest{ID: 1}
	recorder := httptest.NewRecorder()
	env.server.handleMarketListOpenListings(recorder, env.newRequest(), req)

	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var decoded struct {
		Listings []marketListingResult `json:"listings"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(decoded.Listings) != 0 {
		t.Fatalf("expected no listings, got %d", len(decoded.Listings))
	}
}

func TestHandleMarketListOpenListingsAndGetListing(t *testing.T) {
	env := newTestEnv(t)
	sellerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate seller: %v", err)
	}
	seller := crypto.MustNewAddress(crypto.NHBPrefix, sellerKey.PubKey().Address().Bytes())
	listing := seedMarketListing(t, env, seller, 400, 400, 3, 1, true, 1700000000)

	listReq := &RPCRequest{ID: 1}
	listRec := httptest.NewRecorder()
	env.server.handleMarketListOpenListings(listRec, env.newRequest(), listReq)
	listResult, rpcErr := decodeRPCResponse(t, listRec)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var listDecoded struct {
		Listings []marketListingResult `json:"listings"`
	}
	if err := json.Unmarshal(listResult, &listDecoded); err != nil {
		t.Fatalf("decode list result: %v", err)
	}
	if len(listDecoded.Listings) != 1 {
		t.Fatalf("expected exactly one open listing, got %d", len(listDecoded.Listings))
	}
	got := listDecoded.Listings[0]
	if got.Seller != seller.String() {
		t.Fatalf("seller mismatch: got %s want %s", got.Seller, seller.String())
	}
	if got.RateNumerator != "3" || got.RateDenominator != "1" {
		t.Fatalf("unexpected rate: %s/%s", got.RateNumerator, got.RateDenominator)
	}
	if got.TotalAmount != "400" || got.RemainingAmount != "400" {
		t.Fatalf("unexpected amounts: total=%s remaining=%s", got.TotalAmount, got.RemainingAmount)
	}
	if got.Status != "open" {
		t.Fatalf("unexpected status: %s", got.Status)
	}

	getReq := &RPCRequest{ID: 2, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"id": "0x" + hex.EncodeToString(listing.ID[:]),
	})}}
	getRec := httptest.NewRecorder()
	env.server.handleMarketGetListing(getRec, env.newRequest(), getReq)
	getResult, rpcErr := decodeRPCResponse(t, getRec)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error on get: %+v", rpcErr)
	}
	var getDecoded struct {
		Listing *marketListingResult `json:"listing"`
	}
	if err := json.Unmarshal(getResult, &getDecoded); err != nil {
		t.Fatalf("decode get result: %v", err)
	}
	if getDecoded.Listing == nil {
		t.Fatalf("expected listing, got nil")
	}
	if getDecoded.Listing.ID != got.ID {
		t.Fatalf("id mismatch: got %s want %s", getDecoded.Listing.ID, got.ID)
	}
}

func TestHandleMarketGetListingNotFound(t *testing.T) {
	env := newTestEnv(t)
	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"id": "0x" + hex.EncodeToString(make([]byte, 32)),
	})}}
	recorder := httptest.NewRecorder()
	env.server.handleMarketGetListing(recorder, env.newRequest(), req)
	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var decoded struct {
		Listing *marketListingResult `json:"listing"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if decoded.Listing != nil {
		t.Fatalf("expected nil listing for unknown id, got %+v", decoded.Listing)
	}
}

// TestHandleMarketGetMyListingsFiltersBySeller locks in the address-scoping
// logic in handleMarketGetMyListings: it must match listings by comparing
// raw address bytes (bytes.Equal(listing.Seller.Bytes(), addr[:])), not by
// an earlier draft's crypto.Address.String() comparison against a bare
// [20]byte -- a type mismatch that would never have compiled, let alone
// filtered correctly.
func TestHandleMarketGetMyListingsFiltersBySeller(t *testing.T) {
	env := newTestEnv(t)
	sellerAKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sellerA: %v", err)
	}
	sellerBKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate sellerB: %v", err)
	}
	sellerA := crypto.MustNewAddress(crypto.NHBPrefix, sellerAKey.PubKey().Address().Bytes())
	sellerB := crypto.MustNewAddress(crypto.NHBPrefix, sellerBKey.PubKey().Address().Bytes())
	seedMarketListing(t, env, sellerA, 100, 100, 1, 1, true, 1700000001)
	seedMarketListing(t, env, sellerB, 200, 200, 1, 1, true, 1700000002)

	req := &RPCRequest{ID: 1, Params: []json.RawMessage{marshalParam(t, map[string]string{
		"address": sellerA.String(),
	})}}
	recorder := httptest.NewRecorder()
	env.server.handleMarketGetMyListings(recorder, env.newRequest(), req)
	result, rpcErr := decodeRPCResponse(t, recorder)
	if rpcErr != nil {
		t.Fatalf("unexpected rpc error: %+v", rpcErr)
	}
	var decoded struct {
		Listings []marketListingResult `json:"listings"`
	}
	if err := json.Unmarshal(result, &decoded); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(decoded.Listings) != 1 {
		t.Fatalf("expected exactly one listing for sellerA, got %d", len(decoded.Listings))
	}
	if decoded.Listings[0].Seller != sellerA.String() {
		t.Fatalf("returned listing belongs to wrong seller: %s", decoded.Listings[0].Seller)
	}
	if decoded.Listings[0].TotalAmount != "100" {
		t.Fatalf("unexpected total amount: %s", decoded.Listings[0].TotalAmount)
	}
}
