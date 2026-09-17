package core

import (
	"math/big"
	"strings"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/tokenomics/buyback"
	"nhbchain/core/tokenomics/lendingoracle"
	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

// lendingRefPricePayload (mirroring applyLendingRefPriceTransaction's
// unexported payload shape) is defined once in node.go, since
// SubmitLendingRefPrice needs the same encode-side struct in a non-test
// file -- reused here rather than redeclared to avoid two
// structurally-identical types drifting apart.

// newLendingRefPriceTestState builds a bare StateProcessor with a 1-of-1
// signer quorum configured under sp.buybackConfig -- the same
// genesis-declared quorum applyLendingRefPriceTransaction deliberately
// reuses for lending's own ref price (see that function's doc comment).
func newLendingRefPriceTestState(t *testing.T) (sp *StateProcessor, signerKey *crypto.PrivateKey) {
	t.Helper()
	sp = newRewardTestState(t)

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate signer key: %v", err)
	}
	var signerAddr [20]byte
	copy(signerAddr[:], key.PubKey().Address().Bytes())
	cfg := buyback.Config{
		FeeShareBps:     2000,
		DiscountBps:     0,
		SafetyMarginBps: 0,
		SignerThreshold: 1,
		Signers:         [][20]byte{signerAddr},
	}
	if err := sp.SetBuybackConfig(cfg); err != nil {
		t.Fatalf("set buyback config: %v", err)
	}
	return sp, key
}

func signLendingRefPrice(t *testing.T, rp *lendingoracle.ReferencePrice, key *crypto.PrivateKey) []byte {
	t.Helper()
	digest, err := rp.Hash()
	if err != nil {
		t.Fatalf("hash reference price: %v", err)
	}
	sig, err := ethcrypto.Sign(digest[:], key.PrivateKey)
	if err != nil {
		t.Fatalf("sign reference price: %v", err)
	}
	return sig
}

func submitLendingRefPriceTx(t *testing.T, sp *StateProcessor, rateNum, rateDenom *big.Int, ts time.Time, key *crypto.PrivateKey) error {
	t.Helper()
	rp := &lendingoracle.ReferencePrice{Rate: new(big.Rat).SetFrac(rateNum, rateDenom), Timestamp: ts}
	sig := signLendingRefPrice(t, rp, key)
	data, err := rlp.EncodeToBytes(lendingRefPricePayload{
		RateNum:    rateNum,
		RateDenom:  rateDenom,
		Timestamp:  uint64(ts.Unix()),
		Signatures: [][]byte{sig},
	})
	if err != nil {
		t.Fatalf("encode ref price payload: %v", err)
	}
	tx := &types.Transaction{Type: types.TxTypeLendingRefPrice, Data: data}
	return sp.applyLendingRefPriceTransaction(tx)
}

func TestApplyLendingRefPriceTransaction_UpdatesAllConfiguredMarkets(t *testing.T) {
	sp, key := newLendingRefPriceTestState(t)
	manager := nhbstate.NewManager(sp.Trie)

	seed := func(poolID string, oldMedian *big.Int) {
		market := &lending.Market{
			PoolID:              poolID,
			TotalNHBSupplied:    big.NewInt(0),
			TotalSupplyShares:   big.NewInt(0),
			TotalNHBBorrowed:    big.NewInt(0),
			SupplyIndex:         big.NewInt(0),
			BorrowIndex:         big.NewInt(0),
			OracleMedianWei:     oldMedian,
			OraclePrevMedianWei: big.NewInt(0),
		}
		if err := manager.LendingPutMarket(poolID, market); err != nil {
			t.Fatalf("seed market %q: %v", poolID, err)
		}
	}
	seed("default", big.NewInt(0))
	seed("second", mustLendingBig("500000000000000000"))

	ts := time.Unix(1_800_000_000, 0).UTC()
	rateNum := big.NewInt(5)
	rateDenom := big.NewInt(100)
	if err := submitLendingRefPriceTx(t, sp, rateNum, rateDenom, ts, key); err != nil {
		t.Fatalf("apply lending ref price: %v", err)
	}

	// 5/100 == 0.05 NHB per whole ZNHB -> 0.05e18 NHB-wei per whole ZNHB.
	wantMedian := mustLendingBig("50000000000000000")

	for _, poolID := range []string{"default", "second"} {
		market, ok, err := manager.LendingGetMarket(poolID)
		if err != nil {
			t.Fatalf("load market %q: %v", poolID, err)
		}
		if !ok {
			t.Fatalf("expected market %q to exist", poolID)
		}
		if market.OracleMedianWei.Cmp(wantMedian) != 0 {
			t.Fatalf("market %q OracleMedianWei = %s, want %s", poolID, market.OracleMedianWei, wantMedian)
		}
		if market.OracleUpdatedBlock != sp.blockHeight() {
			t.Fatalf("market %q OracleUpdatedBlock = %d, want %d", poolID, market.OracleUpdatedBlock, sp.blockHeight())
		}
	}

	// "default" started at median 0 -> prev shifts to 0. "second" started
	// at 0.5e18 -> prev shifts to that prior value, not to the new one.
	defaultMarket, _, _ := manager.LendingGetMarket("default")
	if defaultMarket.OraclePrevMedianWei.Sign() != 0 {
		t.Fatalf("default market OraclePrevMedianWei = %s, want 0", defaultMarket.OraclePrevMedianWei)
	}
	secondMarket, _, _ := manager.LendingGetMarket("second")
	wantPrev := mustLendingBig("500000000000000000")
	if secondMarket.OraclePrevMedianWei.Cmp(wantPrev) != 0 {
		t.Fatalf("second market OraclePrevMedianWei = %s, want %s", secondMarket.OraclePrevMedianWei, wantPrev)
	}

	last, ok, err := manager.LendingRefPriceLast()
	if err != nil {
		t.Fatalf("load last record: %v", err)
	}
	if !ok {
		t.Fatalf("expected a last-record to be persisted")
	}
	if last.MarketCount != 2 {
		t.Fatalf("last.MarketCount = %d, want 2", last.MarketCount)
	}
	if len(last.Signers) != 1 {
		t.Fatalf("len(last.Signers) = %d, want 1", len(last.Signers))
	}
}

func TestApplyLendingRefPriceTransaction_ZeroMarketsSucceedsAsNoOp(t *testing.T) {
	sp, key := newLendingRefPriceTestState(t)
	ts := time.Unix(1_800_000_000, 0).UTC()
	if err := submitLendingRefPriceTx(t, sp, big.NewInt(5), big.NewInt(100), ts, key); err != nil {
		t.Fatalf("apply lending ref price with zero configured markets: %v", err)
	}
	last, ok, err := nhbstate.NewManager(sp.Trie).LendingRefPriceLast()
	if err != nil {
		t.Fatalf("load last record: %v", err)
	}
	if !ok {
		t.Fatalf("expected a last-record even with zero markets")
	}
	if last.MarketCount != 0 {
		t.Fatalf("last.MarketCount = %d, want 0", last.MarketCount)
	}
}

func TestApplyLendingRefPriceTransaction_InsufficientSignaturesRejected(t *testing.T) {
	sp, _ := newLendingRefPriceTestState(t)
	otherKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate unrelated key: %v", err)
	}
	ts := time.Unix(1_800_000_000, 0).UTC()
	// Signed by a key that isn't in sp.buybackConfig.Signers.
	if err := submitLendingRefPriceTx(t, sp, big.NewInt(5), big.NewInt(100), ts, otherKey); err == nil {
		t.Fatalf("expected an error for a signature bundle with no authorized signer")
	}
}

func TestApplyLendingRefPriceTransaction_ReplayProtectionRejectsNonIncreasingTimestamp(t *testing.T) {
	sp, key := newLendingRefPriceTestState(t)
	ts := time.Unix(1_800_000_000, 0).UTC()
	if err := submitLendingRefPriceTx(t, sp, big.NewInt(5), big.NewInt(100), ts, key); err != nil {
		t.Fatalf("first submission: %v", err)
	}
	// Same timestamp again -- must be rejected, not silently re-accepted.
	if err := submitLendingRefPriceTx(t, sp, big.NewInt(6), big.NewInt(100), ts, key); err == nil {
		t.Fatalf("expected replay-protection rejection for a non-increasing timestamp")
	} else if !strings.Contains(err.Error(), "not newer") {
		t.Fatalf("expected a 'not newer' replay-protection error, got: %v", err)
	}
	// An earlier timestamp must also be rejected.
	earlier := ts.Add(-1 * time.Second)
	if err := submitLendingRefPriceTx(t, sp, big.NewInt(6), big.NewInt(100), earlier, key); err == nil {
		t.Fatalf("expected replay-protection rejection for an earlier timestamp")
	}
	// A strictly later timestamp must succeed.
	later := ts.Add(1 * time.Second)
	if err := submitLendingRefPriceTx(t, sp, big.NewInt(6), big.NewInt(100), later, key); err != nil {
		t.Fatalf("expected a strictly later timestamp to be accepted: %v", err)
	}
}

func mustLendingBig(value string) *big.Int {
	v, ok := new(big.Int).SetString(value, 10)
	if !ok {
		panic("invalid big integer constant: " + value)
	}
	return v
}
