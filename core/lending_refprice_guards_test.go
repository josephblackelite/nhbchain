package core

import (
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/lendingoracle"
	"nhbchain/core/types"
	"nhbchain/crypto"
	"nhbchain/native/lending"
)

// PL-EXT-13 and PL-R5T-53: a lending reference-price bundle needed only a valid
// signer quorum and a timestamp newer than the last accepted one. Nothing compared
// the timestamp with the block that carried it, so one bundle dated far ahead
// refused every honest bundle after it, a timestamp above the largest signed 64-bit
// number wrapped and could never be passed again, a bundle that never landed could
// be applied much later and was stamped fresh, and a rate so small that its
// 1e18-scaled median floors to zero was stored as "no price", which values
// collateral 1:1.

const (
	refGuardActivation = 100
	refGuardHeight     = 200
	refGuardBlockTime  = int64(1_800_000_000)
	refGuardMaxAhead   = 120
	refGuardMaxAge     = 3_600
)

func refGuardProcessor(t *testing.T, gate, maxAhead, maxAge uint64, height uint64) (*StateProcessor, *crypto.PrivateKey) {
	t.Helper()
	sp, key := newLendingRefPriceTestState(t)
	sp.SetLendingRefPriceGuardsActivationHeight(gate)
	sp.SetLendingRefPriceMaxAheadSeconds(maxAhead)
	sp.SetLendingRefPriceMaxAgeSeconds(maxAge)
	manager := nhbstate.NewManager(sp.Trie)
	if err := manager.LendingPutMarket("default", &lending.Market{
		PoolID: "default", TotalNHBSupplied: big.NewInt(0), TotalSupplyShares: big.NewInt(0), TotalNHBBorrowed: big.NewInt(0),
		SupplyIndex: big.NewInt(0), BorrowIndex: big.NewInt(0), OracleMedianWei: big.NewInt(0), OraclePrevMedianWei: big.NewInt(0),
	}); err != nil {
		t.Fatal(err)
	}
	sp.BeginBlock(height, time.Unix(refGuardBlockTime, 0).UTC())
	t.Cleanup(sp.EndBlock)
	return sp, key
}

// submitRawRefPrice applies a bundle whose timestamp is given as the raw unsigned
// number the payload carries, signed over exactly what the chain reconstructs from it.
func submitRawRefPrice(t *testing.T, sp *StateProcessor, key *crypto.PrivateKey, num, den *big.Int, rawTimestamp uint64) error {
	t.Helper()
	rp := &lendingoracle.ReferencePrice{Rate: new(big.Rat).SetFrac(num, den), Timestamp: time.Unix(int64(rawTimestamp), 0).UTC()}
	digest, err := rp.Hash()
	if err != nil {
		t.Fatalf("hash reference price: %v", err)
	}
	sig, err := ethcrypto.Sign(digest[:], key.PrivateKey)
	if err != nil {
		t.Fatalf("sign reference price: %v", err)
	}
	data, err := rlp.EncodeToBytes(lendingRefPricePayload{RateNum: num, RateDenom: den, Timestamp: rawTimestamp, Signatures: [][]byte{sig}})
	if err != nil {
		t.Fatal(err)
	}
	return sp.applyLendingRefPriceTransaction(&types.Transaction{Type: types.TxTypeLendingRefPrice, Data: data})
}

func lastRefPriceTimestamp(t *testing.T, sp *StateProcessor) (uint64, bool) {
	t.Helper()
	rec, ok, err := nhbstate.NewManager(sp.Trie).LendingRefPriceLast()
	if err != nil {
		t.Fatal(err)
	}
	if !ok || rec == nil {
		return 0, false
	}
	return rec.Timestamp, true
}

func onePerWhole() (*big.Int, *big.Int) { return big.NewInt(5), big.NewInt(100) }

func TestRefPriceBundlesAreCheckedAgainstTheBlockOnlyFromTheGuardsHeight(t *testing.T) {
	tiny := new(big.Int).Exp(big.NewInt(10), big.NewInt(19), nil) // 1/1e19 floors to a median of zero
	oneWei := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	T := uint64(refGuardBlockTime)
	cases := []struct {
		name     string
		ts       uint64
		num, den *big.Int
		onErr    error // nil: accepted from the gate
	}{
		{"fresh", T, big.NewInt(5), big.NewInt(100), nil},
		{"ahead by exactly the bound", T + refGuardMaxAhead, big.NewInt(5), big.NewInt(100), nil},
		{"one second past the bound", T + refGuardMaxAhead + 1, big.NewInt(5), big.NewInt(100), ErrLendingRefPriceTooFarAhead},
		{"a year ahead", T + 365*86_400, big.NewInt(5), big.NewInt(100), ErrLendingRefPriceTooFarAhead},
		{"as old as the bound", T - refGuardMaxAge, big.NewInt(5), big.NewInt(100), nil},
		{"one second older than the bound", T - refGuardMaxAge - 1, big.NewInt(5), big.NewInt(100), ErrLendingRefPriceTooOld},
		{"a day old", T - 86_400, big.NewInt(5), big.NewInt(100), ErrLendingRefPriceTooOld},
		{"a median of exactly one wei", T, big.NewInt(1), oneWei, nil},
		{"a median that floors to zero", T, big.NewInt(1), tiny, ErrLendingRefPriceRateTooSmall},
		{"above the largest signed 64-bit number", uint64(math.MaxInt64) + 10, big.NewInt(5), big.NewInt(100), ErrLendingRefPriceTimestampRange},
		{"the largest signed 64-bit number", uint64(math.MaxInt64), big.NewInt(5), big.NewInt(100), ErrLendingRefPriceTooFarAhead},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/before", func(t *testing.T) {
			sp, key := refGuardProcessor(t, refGuardActivation, refGuardMaxAhead, refGuardMaxAge, refGuardActivation-1)
			if err := submitRawRefPrice(t, sp, key, tc.num, tc.den, tc.ts); err != nil {
				t.Fatalf("before the height every well-signed bundle is accepted (the old behaviour), got %v", err)
			}
		})
		t.Run(tc.name+"/from", func(t *testing.T) {
			sp, key := refGuardProcessor(t, refGuardActivation, refGuardMaxAhead, refGuardMaxAge, refGuardActivation)
			err := submitRawRefPrice(t, sp, key, tc.num, tc.den, tc.ts)
			if tc.onErr == nil {
				if err != nil {
					t.Fatalf("from the height this bundle is accepted, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.onErr) {
				t.Fatalf("from the height got %v, want %v", err, tc.onErr)
			}
			if _, ok := lastRefPriceTimestamp(t, sp); ok {
				t.Fatalf("a refused bundle left a record")
			}
			market, _, err := nhbstate.NewManager(sp.Trie).LendingGetMarket("default")
			if err != nil {
				t.Fatal(err)
			}
			if market.OracleMedianWei.Sign() != 0 || market.OracleUpdatedBlock != 0 {
				t.Fatalf("a refused bundle changed the market: median %s block %d", market.OracleMedianWei, market.OracleUpdatedBlock)
			}
		})
	}
}

func TestRefPriceGuardsAreOffByDefaultAtEveryHeight(t *testing.T) {
	sp, key := refGuardProcessor(t, 0, refGuardMaxAhead, refGuardMaxAge, 10_000_000)
	num, den := onePerWhole()
	if err := submitRawRefPrice(t, sp, key, num, den, uint64(refGuardBlockTime)+86_400*365); err != nil {
		t.Fatalf("with the height at 0 a bundle a year ahead is accepted, got %v", err)
	}
}

// With no size set only the two checks that need none apply.
func TestRefPriceGuardsWithNoSizesCheckOnlyTheRangeAndTheMedian(t *testing.T) {
	T := uint64(refGuardBlockTime)
	num, den := onePerWhole()
	sp, key := refGuardProcessor(t, refGuardActivation, 0, 0, refGuardActivation)
	if err := submitRawRefPrice(t, sp, key, num, den, T+10*365*86_400); err != nil {
		t.Fatalf("with no future bound a far-ahead bundle is accepted, got %v", err)
	}
	sp, key = refGuardProcessor(t, refGuardActivation, 0, 0, refGuardActivation)
	if err := submitRawRefPrice(t, sp, key, num, den, T-10*365*86_400); err != nil {
		t.Fatalf("with no age bound an old bundle is accepted, got %v", err)
	}
	sp, key = refGuardProcessor(t, refGuardActivation, 0, 0, refGuardActivation)
	if err := submitRawRefPrice(t, sp, key, num, den, uint64(math.MaxInt64)+1); !errors.Is(err, ErrLendingRefPriceTimestampRange) {
		t.Fatalf("a timestamp beyond 64 signed bits is refused with no sizes set, got %v", err)
	}
	sp, key = refGuardProcessor(t, refGuardActivation, 0, 0, refGuardActivation)
	if err := submitRawRefPrice(t, sp, key, big.NewInt(1), new(big.Int).Exp(big.NewInt(10), big.NewInt(19), nil), T); !errors.Is(err, ErrLendingRefPriceRateTooSmall) {
		t.Fatalf("a median of zero is refused with no sizes set, got %v", err)
	}
}

// The failure that matters: one bundle dated far ahead used to refuse every honest
// bundle after it. From the height it is refused itself and the next honest one lands.
func TestAFarAheadBundleNoLongerFreezesTheFeed(t *testing.T) {
	T := uint64(refGuardBlockTime)
	num, den := onePerWhole()
	for _, tc := range []struct {
		name   string
		height uint64
		frozen bool
	}{
		{"before the height it freezes the feed", refGuardActivation - 1, true},
		{"from the height it does not", refGuardActivation, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp, key := refGuardProcessor(t, refGuardActivation, refGuardMaxAhead, refGuardMaxAge, tc.height)
			_ = submitRawRefPrice(t, sp, key, num, den, T+5*365*86_400)
			err := submitRawRefPrice(t, sp, key, num, den, T)
			if tc.frozen {
				if !errors.Is(err, ErrLendingRefPriceStaleTimestamp) {
					t.Fatalf("the old behaviour: the honest bundle after it is refused as stale, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("the honest bundle after a refused far-ahead one must be accepted, got %v", err)
			}
			if last, ok := lastRefPriceTimestamp(t, sp); !ok || last != T {
				t.Fatalf("last accepted timestamp is %d (found=%v), want %d", last, ok, T)
			}
		})
	}
}

// A timestamp beyond 64 signed bits wrapped negative in the signed message while
// the stored value became huge, so no later bundle could ever be newer.
func TestATimestampBeyond64SignedBitsNoLongerBricksTheFeed(t *testing.T) {
	T := uint64(refGuardBlockTime)
	num, den := onePerWhole()
	huge := uint64(math.MaxInt64) + 77
	for _, tc := range []struct {
		name   string
		height uint64
		bricks bool
	}{
		{"before the height", refGuardActivation - 1, true},
		{"from the height", refGuardActivation, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp, key := refGuardProcessor(t, refGuardActivation, refGuardMaxAhead, refGuardMaxAge, tc.height)
			_ = submitRawRefPrice(t, sp, key, num, den, huge)
			err := submitRawRefPrice(t, sp, key, num, den, T)
			if tc.bricks {
				if !errors.Is(err, ErrLendingRefPriceStaleTimestamp) {
					t.Fatalf("the old behaviour: no later bundle can be newer, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("the feed must still accept an honest bundle, got %v", err)
			}
		})
	}
}

// A median that floors to zero is read by the engine as "no price" and collateral
// is then valued 1:1; from the height such a bundle is refused.
func TestAZeroMedianIsStoredAsNoPriceOnlyBeforeTheGuardsHeight(t *testing.T) {
	tiny := new(big.Int).Exp(big.NewInt(10), big.NewInt(19), nil)
	T := uint64(refGuardBlockTime)
	sp, key := refGuardProcessor(t, refGuardActivation, refGuardMaxAhead, refGuardMaxAge, refGuardActivation-1)
	if err := submitRawRefPrice(t, sp, key, big.NewInt(1), tiny, T); err != nil {
		t.Fatalf("before the height: %v", err)
	}
	market, _, err := nhbstate.NewManager(sp.Trie).LendingGetMarket("default")
	if err != nil {
		t.Fatal(err)
	}
	if market.OracleMedianWei.Sign() != 0 {
		t.Fatalf("the old behaviour stores a median of zero, got %s", market.OracleMedianWei)
	}
	if got := lending.OracleAdjustedCollateralValue(market, big.NewInt(1_000)); got.Cmp(big.NewInt(1_000)) != 0 {
		t.Fatalf("the engine values collateral 1:1 when the median is zero, got %s", got)
	}
}

func TestRefPriceGuardRefusalsAreClassifiedSoTheyNeverHaltABlock(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want proposalTxDisposition
	}{
		{ErrLendingRefPriceTimestampRange, proposalDispositionPrune},
		{ErrLendingRefPriceTooOld, proposalDispositionPrune},
		{ErrLendingRefPriceRateTooSmall, proposalDispositionPrune},
		{ErrLendingRefPriceTooFarAhead, proposalDispositionSkip},
	} {
		if got := classifyProposalError(tc.err); got != tc.want {
			t.Errorf("%v is classified %v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestRefPriceGuardsReachBlockState(t *testing.T) {
	node := newTestNode(t)
	node.SetLendingRefPriceGuardsActivationHeight(refGuardActivation)
	node.SetLendingRefPriceMaxAheadSeconds(refGuardMaxAhead)
	node.SetLendingRefPriceMaxAgeSeconds(refGuardMaxAge)
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	check := func(what string, sp *StateProcessor) {
		t.Helper()
		if sp.lendingRefPriceGuardsFromHeight != refGuardActivation || sp.lendingRefPriceMaxAhead != refGuardMaxAhead || sp.lendingRefPriceMaxAge != refGuardMaxAge {
			t.Fatalf("%s holds %d/%d/%d, want %d/%d/%d", what, sp.lendingRefPriceGuardsFromHeight, sp.lendingRefPriceMaxAhead, sp.lendingRefPriceMaxAge,
				refGuardActivation, refGuardMaxAhead, refGuardMaxAge)
		}
	}
	check("the node's state", node.state)
	copied, err := node.state.Copy()
	if err != nil {
		t.Fatal(err)
	}
	check("a copy of the state", copied)
	if err := node.rebuildStateProcessorLocked(node.state.Trie.Root()); err != nil {
		t.Fatal(err)
	}
	check("a rebuilt state processor", node.state)
}

// A refused bundle and an honest one in the same proposal: the block is built with
// the honest one, the refused one leaves the mempool, and a second node derives the
// same state root.
func TestCreateBlockWithARefusedRefPriceBundleStillProducesABlock(t *testing.T) {
	proposer, validator, key := newLendingRefPriceConsensusHarness(t)
	for _, n := range []*Node{proposer, validator} {
		n.SetLendingRefPriceGuardsActivationHeight(1)
		n.SetLendingRefPriceMaxAheadSeconds(120)
		n.SetLendingRefPriceMaxAgeSeconds(3_600)
	}
	block1, err := proposer.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block 1: %v", err)
	}
	commitBlockOnBoth(t, proposer, validator, block1)

	build := func(rawTs uint64) *types.Transaction {
		rp := &lendingoracle.ReferencePrice{Rate: new(big.Rat).SetFrac(big.NewInt(5), big.NewInt(100)), Timestamp: time.Unix(int64(rawTs), 0).UTC()}
		digest, err := rp.Hash()
		if err != nil {
			t.Fatal(err)
		}
		sig, err := ethcrypto.Sign(digest[:], key.PrivateKey)
		if err != nil {
			t.Fatal(err)
		}
		data, err := rlp.EncodeToBytes(lendingRefPricePayload{RateNum: big.NewInt(5), RateDenom: big.NewInt(100), Timestamp: rawTs, Signatures: [][]byte{sig}})
		if err != nil {
			t.Fatal(err)
		}
		return &types.Transaction{ChainID: types.NHBChainID(), Type: types.TxTypeLendingRefPrice, Data: data, GasPrice: big.NewInt(0)}
	}
	const now = uint64(1_800_000_000) // the harness's fixed clock
	tooFar, honest := build(now+86_400), build(now)

	block2, err := proposer.CreateBlock([]*types.Transaction{tooFar, honest})
	if err != nil {
		t.Fatalf("a refused bundle must not stop the block: %v", err)
	}
	if len(block2.Transactions) != 1 {
		t.Fatalf("expected only the honest bundle in the block, got %d txs", len(block2.Transactions))
	}
	commitBlockOnBoth(t, proposer, validator, block2)
	if proposer.state.CurrentRoot() != validator.state.CurrentRoot() {
		t.Fatalf("proposer and validator diverged")
	}
	status, err := proposer.LendingRefPriceStatus()
	if err != nil || !status.HasRefPrice || status.Timestamp != now {
		t.Fatalf("expected the honest bundle on file (ts %d), got %+v err=%v", now, status, err)
	}
}
