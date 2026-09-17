package market

import (
	"math/big"
	"testing"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

// mockMarketState is a minimal in-memory implementation of engineState for
// unit tests, mirroring native/lending/engine_accrual_test.go's
// mockEngineState pattern.
type mockMarketState struct {
	accounts      map[string]*types.Account
	listings      map[[32]byte]*Listing
	openIdx       map[[32]byte]struct{}
	fills         map[[32]byte]*Fill
	fillsByBuyer  map[string][][32]byte
	fillsBySeller map[string][][32]byte
}

func newMockMarketState() *mockMarketState {
	return &mockMarketState{
		accounts:      make(map[string]*types.Account),
		listings:      make(map[[32]byte]*Listing),
		openIdx:       make(map[[32]byte]struct{}),
		fills:         make(map[[32]byte]*Fill),
		fillsByBuyer:  make(map[string][][32]byte),
		fillsBySeller: make(map[string][][32]byte),
	}
}

func (m *mockMarketState) key(addr crypto.Address) string {
	return string(addr.Bytes())
}

func (m *mockMarketState) GetAccount(addr crypto.Address) (*types.Account, error) {
	if acc, ok := m.accounts[m.key(addr)]; ok {
		return acc, nil
	}
	return nil, nil
}

func (m *mockMarketState) PutAccount(addr crypto.Address, account *types.Account) error {
	m.accounts[m.key(addr)] = account
	return nil
}

func (m *mockMarketState) GetListing(id [32]byte) (*Listing, error) {
	if listing, ok := m.listings[id]; ok {
		return listing.Clone(), nil
	}
	return nil, nil
}

func (m *mockMarketState) PutListing(listing *Listing) error {
	if listing == nil {
		return nil
	}
	m.listings[listing.ID] = listing.Clone()
	return nil
}

func (m *mockMarketState) AppendOpenListing(id [32]byte) error {
	m.openIdx[id] = struct{}{}
	return nil
}

func (m *mockMarketState) RemoveOpenListing(id [32]byte) error {
	delete(m.openIdx, id)
	return nil
}

func (m *mockMarketState) AppendFill(fill *Fill) error {
	if fill == nil {
		return nil
	}
	clone := fill.Clone()
	m.fills[fill.ID] = clone
	buyerKey := m.key(fill.Buyer)
	sellerKey := m.key(fill.Seller)
	m.fillsByBuyer[buyerKey] = append(m.fillsByBuyer[buyerKey], fill.ID)
	m.fillsBySeller[sellerKey] = append(m.fillsBySeller[sellerKey], fill.ID)
	return nil
}

func (m *mockMarketState) ListFillsByBuyer(addr crypto.Address) ([]*Fill, error) {
	ids := m.fillsByBuyer[m.key(addr)]
	fills := make([]*Fill, 0, len(ids))
	for _, id := range ids {
		fills = append(fills, m.fills[id].Clone())
	}
	return fills, nil
}

func (m *mockMarketState) ListFillsBySeller(addr crypto.Address) ([]*Fill, error) {
	ids := m.fillsBySeller[m.key(addr)]
	fills := make([]*Fill, 0, len(ids))
	for _, id := range ids {
		fills = append(fills, m.fills[id].Clone())
	}
	return fills, nil
}

func makeTestAddress(prefix crypto.AddressPrefix, suffix byte) crypto.Address {
	raw := make([]byte, 20)
	raw[len(raw)-1] = suffix
	return crypto.MustNewAddress(prefix, raw)
}

// testHarness bundles an engine, its backing mock state, and the module
// addresses used to construct it.
type testHarness struct {
	engine     *Engine
	state      *mockMarketState
	escrowAddr crypto.Address
	feeAddr    crypto.Address
}

func newTestHarness() *testHarness {
	escrowAddr := makeTestAddress(crypto.ZNHBPrefix, 0xE0)
	feeAddr := makeTestAddress(crypto.NHBPrefix, 0xFE)
	engine := NewEngine(escrowAddr, feeAddr)
	state := newMockMarketState()
	engine.SetState(state)
	state.accounts[state.key(escrowAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
	state.accounts[state.key(feeAddr)] = &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(0)}
	return &testHarness{engine: engine, state: state, escrowAddr: escrowAddr, feeAddr: feeAddr}
}

func (h *testHarness) seedAccount(addr crypto.Address, nhb, znhb int64) {
	h.state.accounts[h.state.key(addr)] = &types.Account{
		BalanceNHB:  big.NewInt(nhb),
		BalanceZNHB: big.NewInt(znhb),
	}
}

func (h *testHarness) balanceZNHB(addr crypto.Address) *big.Int {
	return h.state.accounts[h.state.key(addr)].BalanceZNHB
}

func (h *testHarness) balanceNHB(addr crypto.Address) *big.Int {
	return h.state.accounts[h.state.key(addr)].BalanceNHB
}

// --- CreateListing ---------------------------------------------------------

func TestCreateListingSuccess(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x01)
	h.seedAccount(seller, 0, 1000)

	listing, err := h.engine.CreateListing(seller, big.NewInt(400), big.NewInt(3), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	if listing.Status != ListingOpen {
		t.Fatalf("unexpected status: %v", listing.Status)
	}
	if listing.TotalAmount.Cmp(big.NewInt(400)) != 0 || listing.RemainingAmount.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("unexpected amounts: total=%s remaining=%s", listing.TotalAmount, listing.RemainingAmount)
	}
	if h.balanceZNHB(seller).Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("unexpected seller balance: %s", h.balanceZNHB(seller))
	}
	if h.balanceZNHB(h.escrowAddr).Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("unexpected escrow balance: %s", h.balanceZNHB(h.escrowAddr))
	}
	stored, err := h.state.GetListing(listing.ID)
	if err != nil || stored == nil {
		t.Fatalf("expected listing persisted, err=%v", err)
	}
	if _, open := h.state.openIdx[listing.ID]; !open {
		t.Fatalf("expected listing indexed as open")
	}
}

func TestCreateListingInsufficientBalance(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x02)
	h.seedAccount(seller, 0, 100)

	_, err := h.engine.CreateListing(seller, big.NewInt(200), big.NewInt(1), big.NewInt(1), true)
	if err != errInsufficientBalance {
		t.Fatalf("expected errInsufficientBalance, got %v", err)
	}
	if h.balanceZNHB(seller).Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("seller balance mutated on failure: %s", h.balanceZNHB(seller))
	}
}

func TestCreateListingInvalidAmount(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x03)
	h.seedAccount(seller, 0, 1000)

	if _, err := h.engine.CreateListing(seller, big.NewInt(0), big.NewInt(1), big.NewInt(1), true); err != errInvalidAmount {
		t.Fatalf("expected errInvalidAmount for zero amount, got %v", err)
	}
	if _, err := h.engine.CreateListing(seller, big.NewInt(-5), big.NewInt(1), big.NewInt(1), true); err != errInvalidAmount {
		t.Fatalf("expected errInvalidAmount for negative amount, got %v", err)
	}
}

func TestCreateListingInvalidRate(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x04)
	h.seedAccount(seller, 0, 1000)

	if _, err := h.engine.CreateListing(seller, big.NewInt(100), big.NewInt(0), big.NewInt(1), true); err != errInvalidRate {
		t.Fatalf("expected errInvalidRate for zero numerator, got %v", err)
	}
	if _, err := h.engine.CreateListing(seller, big.NewInt(100), big.NewInt(1), big.NewInt(0), true); err != errInvalidRate {
		t.Fatalf("expected errInvalidRate for zero denominator, got %v", err)
	}
}

func TestCreateListingNilEngineAndState(t *testing.T) {
	var nilEngine *Engine
	if _, err := nilEngine.CreateListing(crypto.Address{}, big.NewInt(1), big.NewInt(1), big.NewInt(1), true); err != errNilState {
		t.Fatalf("expected errNilState from nil engine, got %v", err)
	}

	engine := NewEngine(makeTestAddress(crypto.ZNHBPrefix, 0xE0), makeTestAddress(crypto.NHBPrefix, 0xFE))
	seller := makeTestAddress(crypto.NHBPrefix, 0x05)
	if _, err := engine.CreateListing(seller, big.NewInt(1), big.NewInt(1), big.NewInt(1), true); err != errNilState {
		t.Fatalf("expected errNilState from unset state, got %v", err)
	}
}

// TestCreateListingDuplicateIsIdempotentNoOp locks in the fix for a real
// stranded-funds bug: two CreateListing calls carrying identical
// seller/amount/rate/allowPartial parameters at the same nowFn second derive
// the same listing ID (see newListingID's doc comment in types.go). Before
// this fix, the second call still re-debited the seller and re-credited the
// escrow account even though PutListing's second write left only one
// listing record behind -- permanently stranding the first call's escrowed
// ZNHB. The fix makes the second call a true no-op: it must return the
// existing listing unchanged and must NOT touch any balance a second time.
func TestCreateListingDuplicateIsIdempotentNoOp(t *testing.T) {
	h := newTestHarness()
	h.engine.SetNowFunc(func() int64 { return 1000 })
	seller := makeTestAddress(crypto.NHBPrefix, 0x06)
	h.seedAccount(seller, 0, 1000)

	first, err := h.engine.CreateListing(seller, big.NewInt(400), big.NewInt(3), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("first create listing: %v", err)
	}
	second, err := h.engine.CreateListing(seller, big.NewInt(400), big.NewInt(3), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("second create listing: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("expected identical listing IDs, got %x vs %x", first.ID, second.ID)
	}
	if h.balanceZNHB(seller).Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("seller debited more than once: balance=%s, want 600", h.balanceZNHB(seller))
	}
	if h.balanceZNHB(h.escrowAddr).Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("escrow credited more than once: balance=%s, want 400", h.balanceZNHB(h.escrowAddr))
	}
	if second.RemainingAmount.Cmp(big.NewInt(400)) != 0 {
		t.Fatalf("unexpected remaining amount on no-op return: %s", second.RemainingAmount)
	}
}

// --- FillListing -------------------------------------------------------

func TestFillListingFullFillPartialAllowed(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x10)
	buyer := makeTestAddress(crypto.NHBPrefix, 0x11)
	h.seedAccount(seller, 0, 1000)
	h.seedAccount(buyer, 1000, 0)

	listing, err := h.engine.CreateListing(seller, big.NewInt(1000), big.NewInt(1), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}

	fill, err := h.engine.FillListing(buyer, listing.ID, big.NewInt(1000), big.NewInt(0), [32]byte{})
	if err != nil {
		t.Fatalf("fill listing: %v", err)
	}
	if fill.NHBAmount.Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("unexpected nhb cost: %s", fill.NHBAmount)
	}

	stored, err := h.state.GetListing(listing.ID)
	if err != nil {
		t.Fatalf("get listing: %v", err)
	}
	if stored.Status != ListingFilled {
		t.Fatalf("expected listing filled, got %v", stored.Status)
	}
	if stored.RemainingAmount.Sign() != 0 {
		t.Fatalf("expected zero remaining, got %s", stored.RemainingAmount)
	}
	if _, open := h.state.openIdx[listing.ID]; open {
		t.Fatalf("expected listing removed from open index")
	}
}

func TestFillListingPartialFillPartialAllowed(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x12)
	buyer := makeTestAddress(crypto.NHBPrefix, 0x13)
	h.seedAccount(seller, 0, 1000)
	h.seedAccount(buyer, 1000, 0)

	listing, err := h.engine.CreateListing(seller, big.NewInt(1000), big.NewInt(1), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}

	if _, err := h.engine.FillListing(buyer, listing.ID, big.NewInt(400), big.NewInt(0), [32]byte{}); err != nil {
		t.Fatalf("fill listing: %v", err)
	}

	stored, err := h.state.GetListing(listing.ID)
	if err != nil {
		t.Fatalf("get listing: %v", err)
	}
	if stored.Status != ListingOpen {
		t.Fatalf("expected listing still open, got %v", stored.Status)
	}
	if stored.RemainingAmount.Cmp(big.NewInt(600)) != 0 {
		t.Fatalf("unexpected remaining amount: %s", stored.RemainingAmount)
	}
	if _, open := h.state.openIdx[listing.ID]; !open {
		t.Fatalf("expected listing to remain in open index")
	}
}

func TestFillListingPartialRejectedWhenNotAllowed(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x14)
	buyer := makeTestAddress(crypto.NHBPrefix, 0x15)
	h.seedAccount(seller, 0, 1000)
	h.seedAccount(buyer, 1000, 0)

	listing, err := h.engine.CreateListing(seller, big.NewInt(1000), big.NewInt(1), big.NewInt(1), false)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}

	if _, err := h.engine.FillListing(buyer, listing.ID, big.NewInt(400), big.NewInt(0), [32]byte{}); err != errPartialFillNotAllowed {
		t.Fatalf("expected errPartialFillNotAllowed, got %v", err)
	}
}

func TestFillListingExactFullAmountAcceptedWhenPartialNotAllowed(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x16)
	buyer := makeTestAddress(crypto.NHBPrefix, 0x17)
	h.seedAccount(seller, 0, 1000)
	h.seedAccount(buyer, 1000, 0)

	listing, err := h.engine.CreateListing(seller, big.NewInt(1000), big.NewInt(1), big.NewInt(1), false)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}

	if _, err := h.engine.FillListing(buyer, listing.ID, big.NewInt(1000), big.NewInt(0), [32]byte{}); err != nil {
		t.Fatalf("expected exact full fill to succeed, got %v", err)
	}
}

func TestFillListingExceedsRemainingRejected(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x18)
	buyer := makeTestAddress(crypto.NHBPrefix, 0x19)
	h.seedAccount(seller, 0, 500)
	h.seedAccount(buyer, 10000, 0)

	listing, err := h.engine.CreateListing(seller, big.NewInt(500), big.NewInt(1), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}

	if _, err := h.engine.FillListing(buyer, listing.ID, big.NewInt(600), big.NewInt(0), [32]byte{}); err != errInsufficientRemaining {
		t.Fatalf("expected errInsufficientRemaining, got %v", err)
	}
}

func TestFillListingAlreadyFilledRejected(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x1A)
	buyer := makeTestAddress(crypto.NHBPrefix, 0x1B)
	h.seedAccount(seller, 0, 500)
	h.seedAccount(buyer, 10000, 0)

	listing, err := h.engine.CreateListing(seller, big.NewInt(500), big.NewInt(1), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	if _, err := h.engine.FillListing(buyer, listing.ID, big.NewInt(500), big.NewInt(0), [32]byte{}); err != nil {
		t.Fatalf("first fill: %v", err)
	}
	if _, err := h.engine.FillListing(buyer, listing.ID, big.NewInt(1), big.NewInt(0), [32]byte{}); err != errListingNotOpen {
		t.Fatalf("expected errListingNotOpen, got %v", err)
	}
}

func TestFillListingCancelledRejected(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x1C)
	buyer := makeTestAddress(crypto.NHBPrefix, 0x1D)
	h.seedAccount(seller, 0, 500)
	h.seedAccount(buyer, 10000, 0)

	listing, err := h.engine.CreateListing(seller, big.NewInt(500), big.NewInt(1), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	if err := h.engine.CancelListing(seller, listing.ID); err != nil {
		t.Fatalf("cancel listing: %v", err)
	}
	if _, err := h.engine.FillListing(buyer, listing.ID, big.NewInt(1), big.NewInt(0), [32]byte{}); err != errListingNotOpen {
		t.Fatalf("expected errListingNotOpen, got %v", err)
	}
}

func TestFillListingInsufficientBuyerBalance(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x1E)
	buyer := makeTestAddress(crypto.NHBPrefix, 0x1F)
	h.seedAccount(seller, 0, 500)
	h.seedAccount(buyer, 10, 0)

	listing, err := h.engine.CreateListing(seller, big.NewInt(500), big.NewInt(1), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	if _, err := h.engine.FillListing(buyer, listing.ID, big.NewInt(100), big.NewInt(0), [32]byte{}); err != errInsufficientBalance {
		t.Fatalf("expected errInsufficientBalance, got %v", err)
	}
}

// TestFillListingFeeMathRoundsUp verifies exact fee accounting -- buyer
// debited nhbCost+fee, seller credited exactly nhbCost with no fee
// deducted, fee collector credited exactly the flat fee -- using a rate
// that forces a non-terminating division to confirm round-up-not-truncate
// behaviour: rate is 3 ZNHB per 1 NHB, requesting 10 ZNHB gives
// 10*1/3 = 3.333.., which must round up to 4, not truncate to 3.
func TestFillListingFeeMathRoundsUp(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x20)
	buyer := makeTestAddress(crypto.NHBPrefix, 0x21)
	h.seedAccount(seller, 0, 100)
	h.seedAccount(buyer, 1000, 0)

	listing, err := h.engine.CreateListing(seller, big.NewInt(100), big.NewInt(3), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}

	flatFee := big.NewInt(7)
	fill, err := h.engine.FillListing(buyer, listing.ID, big.NewInt(10), flatFee, [32]byte{})
	if err != nil {
		t.Fatalf("fill listing: %v", err)
	}

	wantCost := big.NewInt(4) // ceil(10*1/3) = 4, not truncated 3
	if fill.NHBAmount.Cmp(wantCost) != 0 {
		t.Fatalf("unexpected nhb cost: got %s want %s", fill.NHBAmount, wantCost)
	}
	if fill.FeeAmount.Cmp(flatFee) != 0 {
		t.Fatalf("unexpected fee amount: got %s want %s", fill.FeeAmount, flatFee)
	}
	if fill.ZNHBAmount.Cmp(big.NewInt(10)) != 0 {
		t.Fatalf("unexpected znhb amount: %s", fill.ZNHBAmount)
	}

	wantBuyerNHB := big.NewInt(1000 - 4 - 7) // 989
	if h.balanceNHB(buyer).Cmp(wantBuyerNHB) != 0 {
		t.Fatalf("unexpected buyer NHB balance: got %s want %s", h.balanceNHB(buyer), wantBuyerNHB)
	}
	wantSellerNHB := big.NewInt(4)
	if h.balanceNHB(seller).Cmp(wantSellerNHB) != 0 {
		t.Fatalf("unexpected seller NHB balance (fee must not be deducted): got %s want %s", h.balanceNHB(seller), wantSellerNHB)
	}
	wantFeeCollectorNHB := big.NewInt(7)
	if h.balanceNHB(h.feeAddr).Cmp(wantFeeCollectorNHB) != 0 {
		t.Fatalf("unexpected fee collector balance: got %s want %s", h.balanceNHB(h.feeAddr), wantFeeCollectorNHB)
	}
	wantBuyerZNHB := big.NewInt(10)
	if h.balanceZNHB(buyer).Cmp(wantBuyerZNHB) != 0 {
		t.Fatalf("unexpected buyer ZNHB balance: got %s want %s", h.balanceZNHB(buyer), wantBuyerZNHB)
	}

	buyerFills, err := h.state.ListFillsByBuyer(buyer)
	if err != nil || len(buyerFills) != 1 {
		t.Fatalf("expected 1 fill indexed by buyer, got %d err=%v", len(buyerFills), err)
	}
	sellerFills, err := h.state.ListFillsBySeller(seller)
	if err != nil || len(sellerFills) != 1 {
		t.Fatalf("expected 1 fill indexed by seller, got %d err=%v", len(sellerFills), err)
	}
}

// --- CancelListing -------------------------------------------------------

func TestCancelListingSuccess(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x30)
	h.seedAccount(seller, 0, 1000)

	listing, err := h.engine.CreateListing(seller, big.NewInt(400), big.NewInt(1), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	if err := h.engine.CancelListing(seller, listing.ID); err != nil {
		t.Fatalf("cancel listing: %v", err)
	}

	if h.balanceZNHB(seller).Cmp(big.NewInt(1000)) != 0 {
		t.Fatalf("unexpected seller balance after cancel: %s", h.balanceZNHB(seller))
	}
	if h.balanceZNHB(h.escrowAddr).Sign() != 0 {
		t.Fatalf("unexpected escrow balance after cancel: %s", h.balanceZNHB(h.escrowAddr))
	}
	stored, err := h.state.GetListing(listing.ID)
	if err != nil || stored.Status != ListingCancelled {
		t.Fatalf("expected listing cancelled, got %v err=%v", stored.Status, err)
	}
	if _, open := h.state.openIdx[listing.ID]; open {
		t.Fatalf("expected listing removed from open index")
	}
}

func TestCancelListingNonSellerRejected(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x31)
	other := makeTestAddress(crypto.NHBPrefix, 0x32)
	h.seedAccount(seller, 0, 1000)
	h.seedAccount(other, 0, 0)

	listing, err := h.engine.CreateListing(seller, big.NewInt(400), big.NewInt(1), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	if err := h.engine.CancelListing(other, listing.ID); err != errNotSeller {
		t.Fatalf("expected errNotSeller, got %v", err)
	}
}

func TestCancelListingAlreadyCancelledRejected(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x33)
	h.seedAccount(seller, 0, 1000)

	listing, err := h.engine.CreateListing(seller, big.NewInt(400), big.NewInt(1), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	if err := h.engine.CancelListing(seller, listing.ID); err != nil {
		t.Fatalf("first cancel: %v", err)
	}
	if err := h.engine.CancelListing(seller, listing.ID); err != errListingNotOpen {
		t.Fatalf("expected errListingNotOpen, got %v", err)
	}
}

func TestCancelListingAlreadyFilledRejected(t *testing.T) {
	h := newTestHarness()
	seller := makeTestAddress(crypto.NHBPrefix, 0x34)
	buyer := makeTestAddress(crypto.NHBPrefix, 0x35)
	h.seedAccount(seller, 0, 500)
	h.seedAccount(buyer, 10000, 0)

	listing, err := h.engine.CreateListing(seller, big.NewInt(500), big.NewInt(1), big.NewInt(1), true)
	if err != nil {
		t.Fatalf("create listing: %v", err)
	}
	if _, err := h.engine.FillListing(buyer, listing.ID, big.NewInt(500), big.NewInt(0), [32]byte{}); err != nil {
		t.Fatalf("fill listing: %v", err)
	}
	if err := h.engine.CancelListing(seller, listing.ID); err != errListingNotOpen {
		t.Fatalf("expected errListingNotOpen, got %v", err)
	}
}

// --- Clone -----------------------------------------------------------------

func TestListingCloneDoesNotShareState(t *testing.T) {
	original := &Listing{
		Seller:          makeTestAddress(crypto.NHBPrefix, 0x40),
		RateNumerator:   big.NewInt(1),
		RateDenominator: big.NewInt(1),
		TotalAmount:     big.NewInt(100),
		RemainingAmount: big.NewInt(100),
	}
	clone := original.Clone()
	clone.RemainingAmount.SetInt64(0)
	if original.RemainingAmount.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("mutating clone affected original: %s", original.RemainingAmount)
	}
}

func TestFillCloneDoesNotShareState(t *testing.T) {
	original := &Fill{
		Buyer:      makeTestAddress(crypto.NHBPrefix, 0x41),
		Seller:     makeTestAddress(crypto.NHBPrefix, 0x42),
		ZNHBAmount: big.NewInt(100),
		NHBAmount:  big.NewInt(100),
		FeeAmount:  big.NewInt(1),
	}
	clone := original.Clone()
	clone.ZNHBAmount.SetInt64(0)
	if original.ZNHBAmount.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("mutating clone affected original: %s", original.ZNHBAmount)
	}
}
