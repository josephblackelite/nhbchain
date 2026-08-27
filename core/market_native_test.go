package core

import (
	"math/big"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/governance"
)

// This file rehearses the full P2P market feature exactly the way a real
// client does: RLP-encode the documented payload shape, sign a real
// *types.Transaction, and drive it through sp.ApplyTransaction -- the same
// dispatch-switch + apply-function path (core/state_transition.go,
// core/market_native.go) a live block would use. This is the local
// rehearsal for the market chain-side integration before any binary
// build/deploy.

func marketCreateListingTx(t *testing.T, nonce uint64, znhbAmount, rateNumerator, rateDenominator *big.Int, allowPartial bool) *types.Transaction {
	t.Helper()
	payload := struct {
		RateNumerator   *big.Int
		RateDenominator *big.Int
		AllowPartial    bool
	}{RateNumerator: rateNumerator, RateDenominator: rateDenominator, AllowPartial: allowPartial}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode create-listing payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeMarketCreateListing,
		Nonce:    nonce,
		Value:    znhbAmount,
		GasLimit: 50_000,
		GasPrice: big.NewInt(1),
		Data:     data,
	}
}

func marketFillListingTx(t *testing.T, nonce uint64, listingID [32]byte, znhbAmount *big.Int) *types.Transaction {
	t.Helper()
	payload := struct {
		ListingID  []byte
		ZNHBAmount *big.Int
	}{ListingID: listingID[:], ZNHBAmount: znhbAmount}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode fill-listing payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeMarketFillListing,
		Nonce:    nonce,
		Value:    big.NewInt(0),
		GasLimit: 50_000,
		GasPrice: big.NewInt(1),
		Data:     data,
	}
}

func marketCancelListingTx(t *testing.T, nonce uint64, listingID [32]byte) *types.Transaction {
	t.Helper()
	payload := struct {
		ListingID []byte
	}{ListingID: listingID[:]}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		t.Fatalf("encode cancel-listing payload: %v", err)
	}
	return &types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeMarketCancelListing,
		Nonce:    nonce,
		Value:    big.NewInt(0),
		GasLimit: 50_000,
		GasPrice: big.NewInt(1),
		Data:     data,
	}
}

// soleOpenListingID fetches the single open listing this test expects to
// exist and returns its ID, so tests never need to hand-predict
// newListingID's derivation.
func soleOpenListingID(t *testing.T, sp *StateProcessor) [32]byte {
	t.Helper()
	manager := nhbstate.NewManager(sp.Trie)
	listings, err := manager.ListOpenMarketListings()
	if err != nil {
		t.Fatalf("list open listings: %v", err)
	}
	if len(listings) != 1 {
		t.Fatalf("expected exactly one open listing, got %d", len(listings))
	}
	return listings[0].ID
}

func TestApplyMarketCreateListing_EscrowsAndIndexes(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sellerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate seller key: %v", err)
	}
	sellerAddr := sellerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(sellerAddr, &types.Account{
		BalanceNHB:  big.NewInt(0),
		BalanceZNHB: big.NewInt(1_000),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed seller: %v", err)
	}

	tx := marketCreateListingTx(t, 0, big.NewInt(400), big.NewInt(3), big.NewInt(1), true)
	if err := tx.Sign(sellerKey.PrivateKey); err != nil {
		t.Fatalf("sign create listing: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("apply create listing: %v", err)
	}

	seller, err := sp.getAccount(sellerAddr)
	if err != nil {
		t.Fatalf("load seller: %v", err)
	}
	if seller.BalanceZNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("expected seller ZNHB 600 after escrow, got %s", seller.BalanceZNHB)
	}
	if seller.Nonce != 1 {
		t.Fatalf("expected seller nonce incremented, got %d", seller.Nonce)
	}

	escrow, err := sp.getAccount(sp.marketEscrowAddr.Bytes())
	if err != nil {
		t.Fatalf("load escrow: %v", err)
	}
	if escrow.BalanceZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected escrow ZNHB 400, got %s", escrow.BalanceZNHB)
	}

	listingID := soleOpenListingID(t, sp)
	manager := nhbstate.NewManager(sp.Trie)
	listing, ok, err := manager.GetMarketListing(listingID)
	if err != nil || !ok {
		t.Fatalf("expected listing persisted, ok=%v err=%v", ok, err)
	}
	if listing.TotalAmount.Cmp(big.NewInt(400)) != 0 || listing.RemainingAmount.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("unexpected listing amounts: total=%s remaining=%s", listing.TotalAmount, listing.RemainingAmount)
	}
	if !listing.AllowPartial {
		t.Fatalf("expected allowPartial=true")
	}
}

// TestApplyMarketFillListing_PartialThenFullSettlesCorrectly exercises the
// full lifecycle: a partial fill (verifying exact fee math against the
// ungoverned default of 0.1 NHB and the real tx hash landing on the fill
// record), then a second fill draining the remainder, which must flip the
// listing to filled and drop it from the open index.
func TestApplyMarketFillListing_PartialThenFullSettlesCorrectly(t *testing.T) {
	sp := newStakingStateProcessor(t)

	sellerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate seller key: %v", err)
	}
	sellerAddr := sellerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(sellerAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(1_000), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed seller: %v", err)
	}

	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	buyerAddr := buyerKey.PubKey().Address().Bytes()
	// 1000 NHB is comfortably enough for two fills at a 1:1 rate (400 ZNHB
	// total) plus two 0.1 NHB flat fees.
	buyerFunds := new(big.Int).Mul(big.NewInt(1000), big.NewInt(1_000000000000000000))
	if err := sp.setAccount(buyerAddr, &types.Account{BalanceNHB: buyerFunds, BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	// List 400 ZNHB at an exact 1:1 rate (rateNumerator=rateDenominator=1)
	// so NHB cost equals the ZNHB amount requested, keeping the assertions
	// below simple.
	createTx := marketCreateListingTx(t, 0, big.NewInt(400), big.NewInt(1), big.NewInt(1), true)
	if err := createTx.Sign(sellerKey.PrivateKey); err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := sp.ApplyTransaction(createTx); err != nil {
		t.Fatalf("apply create: %v", err)
	}
	listingID := soleOpenListingID(t, sp)

	defaultFeeWei, ok := new(big.Int).SetString(defaultMarketFlatFeeWei, 10)
	if !ok {
		t.Fatalf("parse defaultMarketFlatFeeWei")
	}

	// --- First fill: 150 of 400 ZNHB ---
	fill1 := marketFillListingTx(t, 0, listingID, big.NewInt(150))
	if err := fill1.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign fill1: %v", err)
	}
	if err := sp.ApplyTransaction(fill1); err != nil {
		t.Fatalf("apply fill1: %v", err)
	}
	fill1Hash, err := fill1.Hash()
	if err != nil {
		t.Fatalf("hash fill1: %v", err)
	}

	seller, err := sp.getAccount(sellerAddr)
	if err != nil {
		t.Fatalf("load seller after fill1: %v", err)
	}
	if seller.BalanceNHB.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("expected seller NHB 150 after fill1, got %s", seller.BalanceNHB)
	}

	buyer, err := sp.getAccount(buyerAddr)
	if err != nil {
		t.Fatalf("load buyer after fill1: %v", err)
	}
	wantBuyerNHBAfterFill1 := new(big.Int).Sub(buyerFunds, new(big.Int).Add(big.NewInt(150), defaultFeeWei))
	if buyer.BalanceNHB.Cmp(wantBuyerNHBAfterFill1) != 0 {
		t.Fatalf("expected buyer NHB %s after fill1, got %s", wantBuyerNHBAfterFill1, buyer.BalanceNHB)
	}
	if buyer.BalanceZNHB.Cmp(big.NewInt(150)) != 0 {
		t.Fatalf("expected buyer ZNHB 150 after fill1, got %s", buyer.BalanceZNHB)
	}

	feeCollector, err := sp.getAccount(sp.marketFeeCollectorAddr.Bytes())
	if err != nil {
		t.Fatalf("load fee collector: %v", err)
	}
	if feeCollector.BalanceNHB.Cmp(defaultFeeWei) != 0 {
		t.Fatalf("expected fee collector to hold exactly the default flat fee %s, got %s", defaultFeeWei, feeCollector.BalanceNHB)
	}

	manager := nhbstate.NewManager(sp.Trie)
	listingAfterFill1, ok, err := manager.GetMarketListing(listingID)
	if err != nil || !ok {
		t.Fatalf("load listing after fill1: ok=%v err=%v", ok, err)
	}
	if listingAfterFill1.RemainingAmount.Cmp(big.NewInt(250)) != 0 {
		t.Fatalf("expected remaining 250 after fill1, got %s", listingAfterFill1.RemainingAmount)
	}
	if listingAfterFill1.Status.String() != "open" {
		t.Fatalf("expected listing still open after partial fill, got %s", listingAfterFill1.Status)
	}

	fillsForBuyer, err := manager.ListMarketFillsByBuyer([20]byte(buyerAddr))
	if err != nil {
		t.Fatalf("list fills by buyer: %v", err)
	}
	if len(fillsForBuyer) != 1 {
		t.Fatalf("expected exactly one fill for buyer, got %d", len(fillsForBuyer))
	}
	var expectHash [32]byte
	copy(expectHash[:], fill1Hash)
	if fillsForBuyer[0].TxHash != expectHash {
		t.Fatalf("fill TxHash mismatch: got %x want %x", fillsForBuyer[0].TxHash, expectHash)
	}

	// --- Second fill: remaining 250 of 400 ZNHB, must fully drain it ---
	fill2 := marketFillListingTx(t, 1, listingID, big.NewInt(250))
	if err := fill2.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign fill2: %v", err)
	}
	if err := sp.ApplyTransaction(fill2); err != nil {
		t.Fatalf("apply fill2: %v", err)
	}

	listingAfterFill2, ok, err := manager.GetMarketListing(listingID)
	if err != nil || !ok {
		t.Fatalf("load listing after fill2: ok=%v err=%v", ok, err)
	}
	if listingAfterFill2.RemainingAmount.Sign() != 0 {
		t.Fatalf("expected remaining 0 after fill2, got %s", listingAfterFill2.RemainingAmount)
	}
	if listingAfterFill2.Status.String() != "filled" {
		t.Fatalf("expected listing status filled, got %s", listingAfterFill2.Status)
	}
	openListings, err := manager.ListOpenMarketListings()
	if err != nil {
		t.Fatalf("list open listings: %v", err)
	}
	if len(openListings) != 0 {
		t.Fatalf("expected fully-filled listing removed from open index, got %d open", len(openListings))
	}

	finalSeller, err := sp.getAccount(sellerAddr)
	if err != nil {
		t.Fatalf("load seller final: %v", err)
	}
	if finalSeller.BalanceNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("expected seller NHB 400 total after both fills, got %s", finalSeller.BalanceNHB)
	}
}

func TestApplyMarketFillListing_RejectsPartialWhenNotAllowed(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sellerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate seller key: %v", err)
	}
	sellerAddr := sellerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(sellerAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(1_000), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed seller: %v", err)
	}
	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	buyerAddr := buyerKey.PubKey().Address().Bytes()
	buyerFunds := new(big.Int).Mul(big.NewInt(1000), big.NewInt(1_000000000000000000))
	if err := sp.setAccount(buyerAddr, &types.Account{BalanceNHB: buyerFunds, BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	createTx := marketCreateListingTx(t, 0, big.NewInt(400), big.NewInt(1), big.NewInt(1), false)
	if err := createTx.Sign(sellerKey.PrivateKey); err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := sp.ApplyTransaction(createTx); err != nil {
		t.Fatalf("apply create: %v", err)
	}
	listingID := soleOpenListingID(t, sp)

	partialFill := marketFillListingTx(t, 0, listingID, big.NewInt(100))
	if err := partialFill.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign partial fill: %v", err)
	}
	if err := sp.ApplyTransaction(partialFill); err == nil {
		t.Fatalf("expected partial fill to be rejected for a full-only listing")
	}

	buyer, err := sp.getAccount(buyerAddr)
	if err != nil {
		t.Fatalf("load buyer: %v", err)
	}
	if buyer.BalanceNHB.Cmp(buyerFunds) != 0 {
		t.Fatalf("expected buyer balance untouched after rejected fill, got %s", buyer.BalanceNHB)
	}
}

func TestApplyMarketFillListing_RespectsGovernedFeeOverride(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)
	overrideFee := big.NewInt(5_000_000_000_000_000) // 0.005 NHB, deliberately far from the 0.1 NHB default
	if err := manager.ParamStoreSet(governance.ParamKeyMarketFlatFeeWei, []byte(overrideFee.String())); err != nil {
		t.Fatalf("set governed fee: %v", err)
	}

	sellerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate seller key: %v", err)
	}
	sellerAddr := sellerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(sellerAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(1_000), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed seller: %v", err)
	}
	buyerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate buyer key: %v", err)
	}
	buyerAddr := buyerKey.PubKey().Address().Bytes()
	buyerFunds := new(big.Int).Mul(big.NewInt(1000), big.NewInt(1_000000000000000000))
	if err := sp.setAccount(buyerAddr, &types.Account{BalanceNHB: buyerFunds, BalanceZNHB: big.NewInt(0), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed buyer: %v", err)
	}

	createTx := marketCreateListingTx(t, 0, big.NewInt(400), big.NewInt(1), big.NewInt(1), true)
	if err := createTx.Sign(sellerKey.PrivateKey); err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := sp.ApplyTransaction(createTx); err != nil {
		t.Fatalf("apply create: %v", err)
	}
	listingID := soleOpenListingID(t, sp)

	fillTx := marketFillListingTx(t, 0, listingID, big.NewInt(100))
	if err := fillTx.Sign(buyerKey.PrivateKey); err != nil {
		t.Fatalf("sign fill: %v", err)
	}
	if err := sp.ApplyTransaction(fillTx); err != nil {
		t.Fatalf("apply fill: %v", err)
	}

	buyer, err := sp.getAccount(buyerAddr)
	if err != nil {
		t.Fatalf("load buyer: %v", err)
	}
	wantBuyerNHB := new(big.Int).Sub(buyerFunds, new(big.Int).Add(big.NewInt(100), overrideFee))
	if buyer.BalanceNHB.Cmp(wantBuyerNHB) != 0 {
		t.Fatalf("expected buyer NHB %s using governed fee override, got %s", wantBuyerNHB, buyer.BalanceNHB)
	}

	feeCollector, err := sp.getAccount(sp.marketFeeCollectorAddr.Bytes())
	if err != nil {
		t.Fatalf("load fee collector: %v", err)
	}
	if feeCollector.BalanceNHB.Cmp(overrideFee) != 0 {
		t.Fatalf("expected fee collector to hold the governed override fee %s, got %s", overrideFee, feeCollector.BalanceNHB)
	}
}

func TestApplyMarketCancelListing_ReturnsEscrowToSeller(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sellerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate seller key: %v", err)
	}
	sellerAddr := sellerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(sellerAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(1_000), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed seller: %v", err)
	}

	createTx := marketCreateListingTx(t, 0, big.NewInt(400), big.NewInt(1), big.NewInt(1), true)
	if err := createTx.Sign(sellerKey.PrivateKey); err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := sp.ApplyTransaction(createTx); err != nil {
		t.Fatalf("apply create: %v", err)
	}
	listingID := soleOpenListingID(t, sp)

	cancelTx := marketCancelListingTx(t, 1, listingID)
	if err := cancelTx.Sign(sellerKey.PrivateKey); err != nil {
		t.Fatalf("sign cancel: %v", err)
	}
	if err := sp.ApplyTransaction(cancelTx); err != nil {
		t.Fatalf("apply cancel: %v", err)
	}

	seller, err := sp.getAccount(sellerAddr)
	if err != nil {
		t.Fatalf("load seller: %v", err)
	}
	if seller.BalanceZNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected seller ZNHB fully restored to 1000, got %s", seller.BalanceZNHB)
	}

	manager := nhbstate.NewManager(sp.Trie)
	listing, ok, err := manager.GetMarketListing(listingID)
	if err != nil || !ok {
		t.Fatalf("load cancelled listing: ok=%v err=%v", ok, err)
	}
	if listing.Status.String() != "cancelled" {
		t.Fatalf("expected status cancelled, got %s", listing.Status)
	}
	openListings, err := manager.ListOpenMarketListings()
	if err != nil {
		t.Fatalf("list open listings: %v", err)
	}
	if len(openListings) != 0 {
		t.Fatalf("expected cancelled listing removed from open index, got %d open", len(openListings))
	}
}

// TestApplyMarketCreateListing_DuplicateTransactionsAreIdempotent drives the
// stranded-funds fix (native/market/engine.go's CreateListing) through the
// full chain-side pipeline: two DIFFERENT signed transactions (distinct
// nonces -- so replay protection can't be what's saving this test) that
// happen to request the exact same listing at the exact same nowFunc
// second. The second must be a true no-op, not a second debit.
func TestApplyMarketCreateListing_DuplicateTransactionsAreIdempotent(t *testing.T) {
	sp := newStakingStateProcessor(t)
	fixed := time.Unix(1_800_000_000, 0)
	sp.nowFunc = func() time.Time { return fixed }

	sellerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate seller key: %v", err)
	}
	sellerAddr := sellerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(sellerAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(1_000), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed seller: %v", err)
	}

	first := marketCreateListingTx(t, 0, big.NewInt(400), big.NewInt(1), big.NewInt(1), true)
	if err := first.Sign(sellerKey.PrivateKey); err != nil {
		t.Fatalf("sign first: %v", err)
	}
	if err := sp.ApplyTransaction(first); err != nil {
		t.Fatalf("apply first: %v", err)
	}

	second := marketCreateListingTx(t, 1, big.NewInt(400), big.NewInt(1), big.NewInt(1), true)
	if err := second.Sign(sellerKey.PrivateKey); err != nil {
		t.Fatalf("sign second: %v", err)
	}
	if err := sp.ApplyTransaction(second); err != nil {
		t.Fatalf("apply second (must be a safe no-op, not an error): %v", err)
	}

	seller, err := sp.getAccount(sellerAddr)
	if err != nil {
		t.Fatalf("load seller: %v", err)
	}
	if seller.BalanceZNHB.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("seller debited more than once across duplicate listings: balance=%s, want 600", seller.BalanceZNHB)
	}
	escrow, err := sp.getAccount(sp.marketEscrowAddr.Bytes())
	if err != nil {
		t.Fatalf("load escrow: %v", err)
	}
	if escrow.BalanceZNHB.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("escrow credited more than once (funds stranded): balance=%s, want 400", escrow.BalanceZNHB)
	}
	manager := nhbstate.NewManager(sp.Trie)
	openListings, err := manager.ListOpenMarketListings()
	if err != nil {
		t.Fatalf("list open listings: %v", err)
	}
	if len(openListings) != 1 {
		t.Fatalf("expected exactly one listing record despite two create calls, got %d", len(openListings))
	}
}

func TestApplyMarket_PauseGuardBlocksAllThreeOperations(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sellerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate seller key: %v", err)
	}
	sellerAddr := sellerKey.PubKey().Address().Bytes()
	if err := sp.setAccount(sellerAddr, &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(1_000), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed seller: %v", err)
	}

	sp.SetPauseView(pauseViewStub{modules: map[string]bool{moduleMarket: true}})

	tx := marketCreateListingTx(t, 0, big.NewInt(400), big.NewInt(1), big.NewInt(1), true)
	if err := tx.Sign(sellerKey.PrivateKey); err != nil {
		t.Fatalf("sign create: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err == nil {
		t.Fatalf("expected create listing to be rejected while moduleMarket is paused")
	}

	seller, err := sp.getAccount(sellerAddr)
	if err != nil {
		t.Fatalf("load seller: %v", err)
	}
	if seller.BalanceZNHB.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("expected seller balance untouched while paused, got %s", seller.BalanceZNHB)
	}
	if seller.Nonce != 0 {
		t.Fatalf("expected nonce unchanged on a rejected paused transaction, got %d", seller.Nonce)
	}
}
