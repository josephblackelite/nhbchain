package rpc

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"

	nhbstate "nhbchain/core/state"
	"nhbchain/native/market"
)

// defaultMarketListLimit/maxMarketListLimit bound every market_* list
// method's result size -- an unbounded listing/fill scan would let a client
// force the node to walk an ever-growing on-chain index on every call.
const (
	defaultMarketListLimit = 100
	maxMarketListLimit     = 500
)

// marketListingResult is the JSON-tagged view of a market.Listing returned
// over RPC, matching nhbportal's market2.ts MarketListing contract exactly
// (id, seller, rateNumerator, rateDenominator, totalAmount, remainingAmount,
// allowPartial, status, createdAt). market.Listing itself has no JSON tags
// and its Seller field is an unexported-field crypto.Address that would
// serialize as "{}", so it can never be returned to clients directly --
// mirrors rpc/lending_handlers.go's lendingAccountResult precedent.
type marketListingResult struct {
	ID              string `json:"id"`
	Seller          string `json:"seller"`
	RateNumerator   string `json:"rateNumerator"`
	RateDenominator string `json:"rateDenominator"`
	TotalAmount     string `json:"totalAmount"`
	RemainingAmount string `json:"remainingAmount"`
	AllowPartial    bool   `json:"allowPartial"`
	Status          string `json:"status"`
	CreatedAt       int64  `json:"createdAt"`
}

// marketFillResult is the JSON-tagged view of a market.Fill, matching
// market2.ts's MarketFill contract exactly (id, listingId, buyer, seller,
// znhbAmount, nhbAmount, feeAmount, createdAt, txHash).
type marketFillResult struct {
	ID         string `json:"id"`
	ListingID  string `json:"listingId"`
	Buyer      string `json:"buyer"`
	Seller     string `json:"seller"`
	ZNHBAmount string `json:"znhbAmount"`
	NHBAmount  string `json:"nhbAmount"`
	FeeAmount  string `json:"feeAmount"`
	CreatedAt  int64  `json:"createdAt"`
	TxHash     string `json:"txHash"`
}

func newMarketListingResult(listing *market.Listing) *marketListingResult {
	if listing == nil {
		return nil
	}
	return &marketListingResult{
		ID:              "0x" + hex.EncodeToString(listing.ID[:]),
		Seller:          listing.Seller.String(),
		RateNumerator:   bigIntStringOrZero(listing.RateNumerator),
		RateDenominator: bigIntStringOrZero(listing.RateDenominator),
		TotalAmount:     bigIntStringOrZero(listing.TotalAmount),
		RemainingAmount: bigIntStringOrZero(listing.RemainingAmount),
		AllowPartial:    listing.AllowPartial,
		Status:          listing.Status.String(),
		CreatedAt:       listing.CreatedAt,
	}
}

func newMarketFillResult(fill *market.Fill) *marketFillResult {
	if fill == nil {
		return nil
	}
	return &marketFillResult{
		ID:         "0x" + hex.EncodeToString(fill.ID[:]),
		ListingID:  "0x" + hex.EncodeToString(fill.ListingID[:]),
		Buyer:      fill.Buyer.String(),
		Seller:     fill.Seller.String(),
		ZNHBAmount: bigIntStringOrZero(fill.ZNHBAmount),
		NHBAmount:  bigIntStringOrZero(fill.NHBAmount),
		FeeAmount:  bigIntStringOrZero(fill.FeeAmount),
		CreatedAt:  fill.CreatedAt,
		TxHash:     "0x" + hex.EncodeToString(fill.TxHash[:]),
	}
}

// bigIntStringOrZero renders a possibly-nil *big.Int as a decimal string.
// Deliberately takes *big.Int directly rather than a String()-string
// interface: boxing a nil *big.Int into an interface produces a non-nil
// interface value (the type is set, only the pointer is nil), so an
// interface-typed nil check here would miss it and v.String() would panic.
func bigIntStringOrZero(v *big.Int) string {
	if v == nil {
		return "0"
	}
	return v.String()
}

// clampMarketListLimit normalizes a client-supplied limit: <=0 becomes the
// default, and anything above maxMarketListLimit is capped rather than
// rejected, matching the defensive-normalization style market2.ts's own
// normalizeListing/normalizeFill already apply on the response side.
func clampMarketListLimit(limit int) int {
	if limit <= 0 {
		return defaultMarketListLimit
	}
	if limit > maxMarketListLimit {
		return maxMarketListLimit
	}
	return limit
}

var errMarketInvalidID = errors.New("market: id must be exactly 32 bytes")

func decodeMarketID32(raw string) ([32]byte, error) {
	var id [32]byte
	trimmed := strings.TrimPrefix(strings.TrimSpace(raw), "0x")
	trimmed = strings.TrimPrefix(trimmed, "0X")
	decoded, err := hex.DecodeString(trimmed)
	if err != nil {
		return id, err
	}
	if len(decoded) != len(id) {
		return id, errMarketInvalidID
	}
	copy(id[:], decoded)
	return id, nil
}

// handleMarketListOpenListings backs market_listOpenListings([{ limit }]) ->
// { listings }. Unauthenticated and read-only, matching
// lending_getMarket/lending_getPools's convention -- the open order book is
// public market data by design (that's the whole point of a marketplace).
func (s *Server) handleMarketListOpenListings(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) > 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "too many parameters", nil)
		return
	}
	limit := defaultMarketListLimit
	if len(req.Params) == 1 {
		var params struct {
			Limit int `json:"limit"`
		}
		if err := json.Unmarshal(req.Params[0], &params); err != nil {
			writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
			return
		}
		limit = clampMarketListLimit(params.Limit)
	}

	var listings []*market.Listing
	err := s.node.WithStateView(func(manager *nhbstate.Manager) error {
		open, err := manager.ListOpenMarketListings()
		if err != nil {
			return err
		}
		listings = open
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load open listings", err.Error())
		return
	}
	if len(listings) > limit {
		listings = listings[:limit]
	}
	results := make([]*marketListingResult, 0, len(listings))
	for _, listing := range listings {
		results = append(results, newMarketListingResult(listing))
	}
	writeResult(w, req.ID, map[string]interface{}{"listings": results})
}

// handleMarketGetListing backs market_getListing([{ id }]) -> { listing }.
func (s *Server) handleMarketGetListing(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected id parameter", nil)
		return
	}
	var params struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	listingID, err := decodeMarketID32(params.ID)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid listing id", err.Error())
		return
	}

	var listing *market.Listing
	err = s.node.WithStateView(func(manager *nhbstate.Manager) error {
		stored, ok, err := manager.GetMarketListing(listingID)
		if err != nil {
			return err
		}
		if ok {
			listing = stored
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load listing", err.Error())
		return
	}
	writeResult(w, req.ID, map[string]interface{}{"listing": newMarketListingResult(listing)})
}

// handleMarketGetMyListings backs market_getMyListings([{ address, limit }])
// -> { listings }. Unauthenticated and address-scoped, matching
// lending_getUserAccount's convention: every balance and position on this
// chain is public by nature, so a caller who already knows an address can
// query its listings without proving ownership.
func (s *Server) handleMarketGetMyListings(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected address parameter", nil)
		return
	}
	var params struct {
		Address string `json:"address"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	trimmed := strings.TrimSpace(params.Address)
	if trimmed == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address required", nil)
		return
	}
	addr, err := decodeBech32(trimmed)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	limit := clampMarketListLimit(params.Limit)

	// There is no seller-owned-listings index distinct from the open-listing
	// index (a listing is only ever discoverable market-wide while it's
	// open -- see native/market/engine.go's engineState doc comment), so
	// "my listings, any status" is resolved by scanning every open listing
	// for this seller. Filled/cancelled listings are still individually
	// fetchable by ID (via market_getListing) but are not enumerable here;
	// this matches native/market's own design (Listing.Status doc comment:
	// "still readable by ID -- just no longer discoverable in the open
	// list") and keeps this handler O(open listings), not O(all listings
	// ever created).
	var mine []*market.Listing
	err = s.node.WithStateView(func(manager *nhbstate.Manager) error {
		open, err := manager.ListOpenMarketListings()
		if err != nil {
			return err
		}
		for _, listing := range open {
			if listing == nil {
				continue
			}
			if bytes.Equal(listing.Seller.Bytes(), addr[:]) {
				mine = append(mine, listing)
			}
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load listings", err.Error())
		return
	}
	if len(mine) > limit {
		mine = mine[:limit]
	}
	results := make([]*marketListingResult, 0, len(mine))
	for _, listing := range mine {
		results = append(results, newMarketListingResult(listing))
	}
	writeResult(w, req.ID, map[string]interface{}{"listings": results})
}

// handleMarketGetMyFills backs
// market_getMyFills([{ address, role, limit }]) -> { fills }. role selects
// "buyer" (backs My Purchases) or "seller" (backs the seller-side fill
// history); any other/omitted role defaults to "buyer".
func (s *Server) handleMarketGetMyFills(w http.ResponseWriter, _ *http.Request, req *RPCRequest) {
	if s == nil || s.node == nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "node unavailable", nil)
		return
	}
	if len(req.Params) != 1 {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "expected address parameter", nil)
		return
	}
	var params struct {
		Address string `json:"address"`
		Role    string `json:"role"`
		Limit   int    `json:"limit"`
	}
	if err := json.Unmarshal(req.Params[0], &params); err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid parameters", err.Error())
		return
	}
	trimmed := strings.TrimSpace(params.Address)
	if trimmed == "" {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "address required", nil)
		return
	}
	addr, err := decodeBech32(trimmed)
	if err != nil {
		writeError(w, http.StatusBadRequest, req.ID, codeInvalidParams, "invalid address", err.Error())
		return
	}
	limit := clampMarketListLimit(params.Limit)
	asSeller := strings.EqualFold(strings.TrimSpace(params.Role), "seller")

	var fills []*market.Fill
	err = s.node.WithStateView(func(manager *nhbstate.Manager) error {
		if asSeller {
			found, err := manager.ListMarketFillsBySeller(addr)
			if err != nil {
				return err
			}
			fills = found
			return nil
		}
		found, err := manager.ListMarketFillsByBuyer(addr)
		if err != nil {
			return err
		}
		fills = found
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, req.ID, codeServerError, "failed to load fills", err.Error())
		return
	}
	if len(fills) > limit {
		fills = fills[:limit]
	}
	results := make([]*marketFillResult, 0, len(fills))
	for _, fill := range fills {
		results = append(results, newMarketFillResult(fill))
	}
	writeResult(w, req.ID, map[string]interface{}{"fills": results})
}
