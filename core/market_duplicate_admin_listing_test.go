package core

import (
	"errors"
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// Ledger PL-EXT-49 (bug-bounty report of 2026-11-18): two identical listings from the admin/treasury wallet in one block second derive
// the same listing id. The second is an idempotent no-op in the engine (nothing is escrowed), but the transaction handler still
// booked the escrow against the Reward Pool, so the pool was debited twice for one debit of the wallet and CheckZNHBSupplyInvariant
// failed the block's lifecycle: while the pair sat in the mempool every block was empty. The second listing is now refused (skippable:
// a later second lists it normally). A block holding such a pair was never valid before, so no valid block changes.

func adminListingAt(t *testing.T, sp *StateProcessor, key *crypto.PrivateKey, nonce uint64, amount int64, second int64, height uint64) error {
	t.Helper()
	tx := marketCreateListingTx(t, nonce, big.NewInt(amount), big.NewInt(1), big.NewInt(1), true)
	if err := tx.Sign(key.PrivateKey); err != nil {
		t.Fatalf("sign create listing: %v", err)
	}
	sp.BeginBlock(height, time.Unix(second, 0).UTC())
	defer sp.EndBlock()
	return sp.ApplyTransaction(tx)
}

func openListingCount(t *testing.T, sp *StateProcessor) int {
	t.Helper()
	listings, err := nhbstate.NewManager(sp.Trie).ListOpenMarketListings()
	if err != nil {
		t.Fatalf("read open listings: %v", err)
	}
	return len(listings)
}

func TestASecondIdenticalAdminListingInTheSameBlockSecondIsRefusedAndTheInvariantHolds(t *testing.T) {
	sp, adminKey, adminAddr := setUpMarketAdminInvariantTest(t)

	if err := adminListingAt(t, sp, adminKey, 0, 50_000, 1_700_000_000, 10); err != nil {
		t.Fatalf("first listing: %v", err)
	}
	err := adminListingAt(t, sp, adminKey, 1, 50_000, 1_700_000_000, 10)
	if !errors.Is(err, ErrMarketDuplicateAdminListing) {
		t.Fatalf("the second identical listing in the same second: got %v, want ErrMarketDuplicateAdminListing", err)
	}
	if got := classifyProposalError(err); got != proposalDispositionSkip {
		t.Fatalf("the refusal must be skippable (a later second lists it), got disposition %v", got)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant after the pair: %v", err)
	}
	acct, err := sp.getAccount(adminAddr[:])
	if err != nil {
		t.Fatalf("admin account: %v", err)
	}
	if want := big.NewInt(996_987_257 - 50_000); acct.BalanceZNHB.Cmp(want) != 0 {
		t.Fatalf("the admin wallet is debited once: got %s want %s", acct.BalanceZNHB, want)
	}
	if got := openListingCount(t, sp); got != 1 {
		t.Fatalf("one listing stands, got %d", got)
	}
}

func TestTheSameAdminListingInALaterSecondIsAFreshListing(t *testing.T) {
	sp, adminKey, _ := setUpMarketAdminInvariantTest(t)
	if err := adminListingAt(t, sp, adminKey, 0, 50_000, 1_700_000_000, 10); err != nil {
		t.Fatalf("first listing: %v", err)
	}
	if err := adminListingAt(t, sp, adminKey, 1, 50_000, 1_700_000_002, 11); err != nil {
		t.Fatalf("the same listing two seconds later: %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant: %v", err)
	}
	if got := openListingCount(t, sp); got != 2 {
		t.Fatalf("two listings stand, got %d", got)
	}
}

func TestTwoDifferentAdminListingsInOneSecondBothStand(t *testing.T) {
	sp, adminKey, _ := setUpMarketAdminInvariantTest(t)
	if err := adminListingAt(t, sp, adminKey, 0, 50_000, 1_700_000_000, 10); err != nil {
		t.Fatalf("first listing: %v", err)
	}
	if err := adminListingAt(t, sp, adminKey, 1, 40_000, 1_700_000_000, 10); err != nil {
		t.Fatalf("a different listing in the same second: %v", err)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant: %v", err)
	}
	if got := openListingCount(t, sp); got != 2 {
		t.Fatalf("two listings stand, got %d", got)
	}
}

func TestAnOrdinarySellersIdenticalListingInTheSameSecondStaysAHarmlessNoOp(t *testing.T) {
	// The idempotent behaviour is kept for everyone but the admin wallet: it strands nothing and books nothing.
	sp, _, _ := setUpMarketAdminInvariantTest(t)
	sellerKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate seller key: %v", err)
	}
	if err := sp.setAccount(sellerKey.PubKey().Address().Bytes(), &types.Account{BalanceNHB: big.NewInt(0), BalanceZNHB: big.NewInt(1_000_000), Stake: big.NewInt(0)}); err != nil {
		t.Fatalf("seed seller: %v", err)
	}
	if err := adminListingAt(t, sp, sellerKey, 0, 50_000, 1_700_000_000, 10); err != nil {
		t.Fatalf("first listing: %v", err)
	}
	if err := adminListingAt(t, sp, sellerKey, 1, 50_000, 1_700_000_000, 10); err != nil {
		t.Fatalf("the identical second listing of an ordinary seller must stay a no-op, got %v", err)
	}
	acct, err := sp.getAccount(sellerKey.PubKey().Address().Bytes())
	if err != nil {
		t.Fatalf("seller account: %v", err)
	}
	if want := big.NewInt(1_000_000 - 50_000); acct.BalanceZNHB.Cmp(want) != 0 {
		t.Fatalf("the seller is debited once: got %s want %s", acct.BalanceZNHB, want)
	}
	if err := sp.CheckZNHBSupplyInvariant(); err != nil {
		t.Fatalf("supply invariant: %v", err)
	}
}
