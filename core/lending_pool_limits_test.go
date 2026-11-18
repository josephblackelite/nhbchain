package core

import (
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/tokenomics/lendingoracle"
	"nhbchain/core/types"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/lending"
)

// ---------------------------------------------------------------------------
// PL-R5T-13, stage 1 (no gate): the reference-price transaction writes every
// market it lists. Each write used to load and scan the one list that holds all
// pool ids, so the update cost O(pools squared); 4,000 pools took more than two
// seconds against a proposer budget of a quarter of a second. The state it leaves
// behind is the same.
// ---------------------------------------------------------------------------

func seedPools(t *testing.T, sp *StateProcessor, n int) {
	t.Helper()
	manager := nhbstate.NewManager(sp.Trie)
	for i := 0; i < n; i++ {
		id := "pool-" + strings.Repeat("x", i%7) + string(rune('a'+i%26)) + "-" + itoa(i)
		market := &lending.Market{
			PoolID:              id,
			TotalNHBSupplied:    big.NewInt(0),
			TotalSupplyShares:   big.NewInt(0),
			TotalNHBBorrowed:    big.NewInt(0),
			SupplyIndex:         big.NewInt(0),
			BorrowIndex:         big.NewInt(0),
			OracleMedianWei:     big.NewInt(int64(i)),
			OraclePrevMedianWei: big.NewInt(0),
		}
		if err := manager.LendingPutMarket(id, market); err != nil {
			t.Fatalf("seed pool %d: %v", i, err)
		}
	}
}

func itoa(i int) string { return big.NewInt(int64(i)).String() }

// applyRefPriceWithTheOldLoop is applyLendingRefPriceTransaction as it was before
// stage 1: the same steps, writing each market with LendingPutMarket (which scans
// the pool index every time). It is the reference the new code must leave the
// state identical to.
func applyRefPriceWithTheOldLoop(t *testing.T, sp *StateProcessor, rateNum, rateDenom *big.Int, ts uint64) {
	t.Helper()
	manager := nhbstate.NewManager(sp.Trie)
	markets, err := manager.LendingListMarkets()
	if err != nil {
		t.Fatal(err)
	}
	height := sp.blockHeight()
	medianWei := new(big.Int).Mul(rateNum, oneWholeZNHBWei)
	medianWei.Quo(medianWei, rateDenom)
	for _, market := range markets {
		prev := market.OracleMedianWei
		if prev == nil {
			prev = big.NewInt(0)
		}
		market.OraclePrevMedianWei = prev
		market.OracleMedianWei = new(big.Int).Set(medianWei)
		market.OracleUpdatedBlock = height
		if err := manager.LendingPutMarket(market.PoolID, market); err != nil {
			t.Fatal(err)
		}
	}
	rec := nhbstate.LendingRefPriceRecord{
		RateNum: new(big.Int).Set(rateNum), RateDenom: new(big.Int).Set(rateDenom), Timestamp: ts,
		AppliedBlock: height, MarketCount: uint64(len(markets)),
	}
	if err := manager.LendingSetRefPriceLast(rec); err != nil {
		t.Fatal(err)
	}
}

func TestLendingRefPriceUpdateLeavesTheSameStateAsTheOldLoop(t *testing.T) {
	const pools = 40
	ts := time.Unix(1_800_000_000, 0).UTC()
	num, den := big.NewInt(5), big.NewInt(100)

	spNew, key := newLendingRefPriceTestState(t)
	seedPools(t, spNew, pools)
	if err := submitLendingRefPriceTx(t, spNew, num, den, ts, key); err != nil {
		t.Fatalf("apply the reference price: %v", err)
	}
	// The record carries the signers, which the reference loop above does not
	// know; compare everything else by writing the same record into the old state.
	spOld, _ := newLendingRefPriceTestState(t)
	seedPools(t, spOld, pools)
	applyRefPriceWithTheOldLoop(t, spOld, num, den, uint64(ts.Unix()))

	newManager, oldManager := nhbstate.NewManager(spNew.Trie), nhbstate.NewManager(spOld.Trie)
	newIDs, err := newManager.LendingListPoolIDs()
	if err != nil {
		t.Fatal(err)
	}
	oldIDs, err := oldManager.LendingListPoolIDs()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(newIDs, ",") != strings.Join(oldIDs, ",") {
		t.Fatalf("the pool index differs after the update")
	}
	for _, id := range newIDs {
		a, _, err := newManager.LendingGetMarket(id)
		if err != nil {
			t.Fatal(err)
		}
		b, _, err := oldManager.LendingGetMarket(id)
		if err != nil {
			t.Fatal(err)
		}
		if a.OracleMedianWei.Cmp(b.OracleMedianWei) != 0 || a.OraclePrevMedianWei.Cmp(b.OraclePrevMedianWei) != 0 || a.OracleUpdatedBlock != b.OracleUpdatedBlock {
			t.Fatalf("market %q differs after the update: median %s/%s prev %s/%s block %d/%d", id,
				a.OracleMedianWei, b.OracleMedianWei, a.OraclePrevMedianWei, b.OraclePrevMedianWei, a.OracleUpdatedBlock, b.OracleUpdatedBlock)
		}
	}
}

// Writing the markets must not cost a scan of the pool index per market. The two
// are timed on the same pools in the same process, so the comparison holds on a
// slow machine: the old loop is quadratic and the new update linear.
func TestLendingRefPriceUpdateOfManyPoolsDoesNotRescanTheIndexPerMarket(t *testing.T) {
	const pools = 1_400
	sp, key := newLendingRefPriceTestState(t)
	seedPools(t, sp, pools)

	startNew := time.Now()
	if err := submitLendingRefPriceTx(t, sp, big.NewInt(5), big.NewInt(100), time.Unix(1_800_000_000, 0).UTC(), key); err != nil {
		t.Fatalf("apply the reference price: %v", err)
	}
	elapsedNew := time.Since(startNew)

	startOld := time.Now()
	applyRefPriceWithTheOldLoop(t, sp, big.NewInt(6), big.NewInt(100), 1_800_000_001)
	elapsedOld := time.Since(startOld)

	t.Logf("%d pools: update %s, the old loop %s", pools, elapsedNew, elapsedOld)
	if elapsedNew*4 > elapsedOld {
		t.Fatalf("updating %d pools took %s against %s for the old per-market scan: the update still scans the pool index for every market", pools, elapsedNew, elapsedOld)
	}
}

// ---------------------------------------------------------------------------
// PL-R5T-13, stage 2 (gated): bounded pool creation.
// ---------------------------------------------------------------------------

const poolLimitsTestActivation = 100

func poolLimitsProcessor(t *testing.T, gate, maxPools, maxIDLen uint64) *StateProcessor {
	t.Helper()
	sp := newRewardTestState(t)
	sp.SetLendingRiskParameters(lending.RiskParameters{MaxLTV: 7_500, LiquidationThreshold: 8_000})
	sp.SetLendingAccrualConfig(0, 0, lending.DefaultInterestModel)
	sp.SetLendingPoolLimitsActivationHeight(gate)
	sp.SetLendingMaxPools(maxPools)
	sp.SetLendingPoolIDMaxLength(maxIDLen)
	return sp
}

type stubPauses map[string]bool

func (s stubPauses) IsPaused(module string) bool { return s[module] }

func createPoolInBlock(t *testing.T, sp *StateProcessor, owner *crypto.PrivateKey, nonce uint64, poolID string, height uint64) error {
	t.Helper()
	sp.BeginBlock(height, time.Unix(int64(height), 0).UTC())
	defer sp.EndBlock()
	return sp.applyLendingCreatePoolTransaction(lendingCreatePoolTx(t, nonce, poolID), owner.PubKey().Address().Bytes())
}

func poolExists(t *testing.T, sp *StateProcessor, poolID string) bool {
	t.Helper()
	_, ok, err := nhbstate.NewManager(sp.Trie).LendingGetMarket(poolID)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

func newKey(t *testing.T) *crypto.PrivateKey {
	t.Helper()
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestCreatePoolRejectsUnusableIDsOnlyFromThePoolLimitsHeight(t *testing.T) {
	long := strings.Repeat("a", 65)
	cases := []struct {
		name    string
		poolID  string
		allowed bool // from the height, with a 64 character limit
	}{
		{"plain", "Acme-Pool_1.X", true},
		{"exactly the limit", strings.Repeat("a", 64), true},
		{"one past the limit", long, false},
		{"a space inside", "acme pool", false},
		{"a colon, the key separator", "acme:pool", false},
		{"a slash", "acme/pool", false},
		{"a non-ASCII letter", "poolé", false},
		{"a control character", "acme\x01pool", false},
	}
	for _, tc := range cases {
		for _, gated := range []bool{false, true} {
			name := tc.name + "/before"
			gate, height := uint64(poolLimitsTestActivation), uint64(poolLimitsTestActivation-1)
			if gated {
				name = tc.name + "/from"
				height = poolLimitsTestActivation
			}
			t.Run(name, func(t *testing.T) {
				sp := poolLimitsProcessor(t, gate, 0, 64)
				err := createPoolInBlock(t, sp, newKey(t), 0, tc.poolID, height)
				if !gated || tc.allowed {
					if err != nil {
						t.Fatalf("pool %q must be created (gated=%v): %v", tc.poolID, gated, err)
					}
					return
				}
				if !errors.Is(err, ErrLendingPoolIDInvalid) {
					t.Fatalf("pool %q from the height: got %v, want ErrLendingPoolIDInvalid", tc.poolID, err)
				}
				if poolExists(t, sp, tc.poolID) {
					t.Fatalf("the refused pool %q was created", tc.poolID)
				}
			})
		}
	}
}

func TestCreatePoolWithoutALengthLimitOnlyChecksTheCharacters(t *testing.T) {
	sp := poolLimitsProcessor(t, poolLimitsTestActivation, 0, 0)
	if err := createPoolInBlock(t, sp, newKey(t), 0, strings.Repeat("a", 500), poolLimitsTestActivation); err != nil {
		t.Fatalf("with no length limit a long id of allowed characters is created: %v", err)
	}
	if err := createPoolInBlock(t, sp, newKey(t), 0, "bad id", poolLimitsTestActivation); !errors.Is(err, ErrLendingPoolIDInvalid) {
		t.Fatalf("the character rule still applies without a length limit, got %v", err)
	}
}

func TestCreatePoolStopsAtThePoolCountLimit(t *testing.T) {
	// The default pool counts once it exists.
	sp := poolLimitsProcessor(t, poolLimitsTestActivation, 3, 0)
	owner := newKey(t)
	height := uint64(poolLimitsTestActivation)
	for i, id := range []string{"p1", "p2", "p3"} {
		if err := createPoolInBlock(t, sp, owner, uint64(i), id, height); err != nil {
			t.Fatalf("pool %s within the limit: %v", id, err)
		}
	}
	if err := createPoolInBlock(t, sp, owner, 3, "p4", height); !errors.Is(err, ErrLendingPoolLimitReached) {
		t.Fatalf("the fourth pool of a limit of three: got %v, want ErrLendingPoolLimitReached", err)
	}
	if poolExists(t, sp, "p4") {
		t.Fatalf("the pool over the limit was created")
	}

	// Before the height there is no limit.
	sp = poolLimitsProcessor(t, poolLimitsTestActivation, 3, 0)
	for i := 0; i < 6; i++ {
		if err := createPoolInBlock(t, sp, owner, uint64(i), "q"+itoa(i), poolLimitsTestActivation-1); err != nil {
			t.Fatalf("before the height pool %d: %v", i, err)
		}
	}
}

func TestCreatePoolIsRefusedWhileLendingIsPausedFromThePoolLimitsHeight(t *testing.T) {
	for _, tc := range []struct {
		name    string
		height  uint64
		refused bool
	}{
		{"before the height the pause is ignored", poolLimitsTestActivation - 1, false},
		{"from the height the pause applies", poolLimitsTestActivation, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := poolLimitsProcessor(t, poolLimitsTestActivation, 0, 0)
			sp.SetPauseView(stubPauses{"lending": true})
			err := createPoolInBlock(t, sp, newKey(t), 0, "paused-pool", tc.height)
			if tc.refused {
				if !errors.Is(err, nativecommon.ErrModulePaused) {
					t.Fatalf("got %v, want ErrModulePaused", err)
				}
				if poolExists(t, sp, "paused-pool") {
					t.Fatalf("a pool was created while lending is paused")
				}
				return
			}
			if err != nil {
				t.Fatalf("before the height a paused module still creates pools (old behaviour): %v", err)
			}
		})
	}
}

// Naming a pool the chain has not seen used to create it (and, for each new name,
// sweep every account). From the height only the default pool is created that way.
func TestAnUnseenPoolIsNotCreatedByANonCreationTransactionFromThePoolLimitsHeight(t *testing.T) {
	for _, tc := range []struct {
		name    string
		height  uint64
		created bool
	}{
		{"before the height it is created", poolLimitsTestActivation - 1, true},
		{"from the height it is not", poolLimitsTestActivation, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := poolLimitsProcessor(t, poolLimitsTestActivation, 0, 0)
			sp.BeginBlock(tc.height, time.Unix(int64(tc.height), 0).UTC())
			defer sp.EndBlock()
			_, _, err := sp.lendingEngine("ghost-pool")
			if tc.created {
				if err != nil || !poolExists(t, sp, "ghost-pool") {
					t.Fatalf("old behaviour: the pool is created implicitly, got err=%v exists=%v", err, poolExists(t, sp, "ghost-pool"))
				}
				return
			}
			if !errors.Is(err, ErrLendingPoolNotFound) {
				t.Fatalf("got %v, want ErrLendingPoolNotFound", err)
			}
			if poolExists(t, sp, "ghost-pool") {
				t.Fatalf("the pool was created implicitly")
			}
			if done, err := nhbstate.NewManager(sp.Trie).LendingLegacyReconciled("ghost-pool"); err != nil || done {
				t.Fatalf("a sweep of every account was done for a pool that does not exist: done=%v err=%v", done, err)
			}
			// The default pool is still created on first use, and a created pool is used.
			if _, _, err := sp.lendingEngine("default"); err != nil {
				t.Fatalf("the default pool is created on first use: %v", err)
			}
			if err := sp.applyLendingCreatePoolTransaction(lendingCreatePoolTx(t, 0, "real-pool"), newKey(t).PubKey().Address().Bytes()); err != nil {
				t.Fatalf("create pool: %v", err)
			}
			if _, _, err := sp.lendingEngine("real-pool"); err != nil {
				t.Fatalf("a created pool is usable: %v", err)
			}
		})
	}
}

func TestPoolLimitRefusalsAreClassifiedSoTheyNeverHaltABlock(t *testing.T) {
	if got := classifyProposalError(ErrLendingPoolIDInvalid); got != proposalDispositionPrune {
		t.Errorf("ErrLendingPoolIDInvalid is classified %v, want prune", got)
	}
	if got := classifyProposalError(ErrLendingPoolLimitReached); got != proposalDispositionPrune {
		t.Errorf("ErrLendingPoolLimitReached is classified %v, want prune", got)
	}
	if got := classifyProposalError(ErrLendingPoolNotFound); got != proposalDispositionSkip {
		t.Errorf("ErrLendingPoolNotFound is classified %v, want skip", got)
	}
}

// One refused pool creation and one valid one in the same proposal: the block is
// built with the valid one, the refused one leaves the mempool, and an independent
// validator derives the same state root.
func TestCreateBlockWithARefusedPoolCreationStillProducesABlock(t *testing.T) {
	build := func() *Node {
		node := newTestNode(t)
		node.SetLendingPoolLimitsActivationHeight(1)
		node.SetLendingPoolIDMaxLength(16)
		return node
	}
	proposer, validator := build(), build()

	good, bad := newKey(t), newKey(t)
	goodTx := lendingCreatePoolTx(t, 0, "good-pool")
	if err := goodTx.Sign(good.PrivateKey); err != nil {
		t.Fatal(err)
	}
	badTx := lendingCreatePoolTx(t, 0, "this id is far too long and has spaces")
	if err := badTx.Sign(bad.PrivateKey); err != nil {
		t.Fatal(err)
	}

	block, err := proposer.CreateBlock([]*types.Transaction{badTx, goodTx})
	if err != nil {
		t.Fatalf("a refused pool creation must not stop the block: %v", err)
	}
	if len(block.Transactions) != 1 || block.Transactions[0].Type != types.TxTypeLendingCreatePool {
		t.Fatalf("expected exactly the valid pool creation in the block, got %d txs", len(block.Transactions))
	}
	if err := validator.ValidateBlock(block); err != nil {
		t.Fatalf("validator rejected the proposer's block: %v", err)
	}
	if err := proposer.CommitBlock(block); err != nil {
		t.Fatalf("proposer commit: %v", err)
	}
	if err := validator.CommitBlock(block); err != nil {
		t.Fatalf("validator commit: %v", err)
	}
	if proposer.state.CurrentRoot() != validator.state.CurrentRoot() {
		t.Fatalf("proposer and validator diverged")
	}
}

var _ = lendingoracle.ReferencePrice{}

// The setting and the two sizes must survive Copy and a rebuild of the state
// processor from the node.
func TestLendingPoolLimitsReachBlockState(t *testing.T) {
	node := newTestNode(t)
	node.stateMu.RLock()
	if node.state.lendingPoolLimitsFromHeight != 0 || node.state.lendingMaxPools != 0 || node.state.lendingPoolIDMaxLength != 0 {
		node.stateMu.RUnlock()
		t.Fatalf("a new node has pool limits set, want all off")
	}
	node.stateMu.RUnlock()

	node.SetLendingPoolLimitsActivationHeight(poolLimitsTestActivation)
	node.SetLendingMaxPools(7)
	node.SetLendingPoolIDMaxLength(33)

	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	check := func(what string, sp *StateProcessor) {
		t.Helper()
		if sp.lendingPoolLimitsFromHeight != poolLimitsTestActivation || sp.lendingMaxPools != 7 || sp.lendingPoolIDMaxLength != 33 {
			t.Fatalf("%s holds height %d, max pools %d, max id length %d; want %d, 7, 33", what,
				sp.lendingPoolLimitsFromHeight, sp.lendingMaxPools, sp.lendingPoolIDMaxLength, poolLimitsTestActivation)
		}
	}
	check("the node's state", node.state)
	copied, err := node.state.Copy()
	if err != nil {
		t.Fatalf("copy state: %v", err)
	}
	check("a copy of the state", copied)
	if err := node.rebuildStateProcessorLocked(node.state.Trie.Root()); err != nil {
		t.Fatalf("rebuild state processor: %v", err)
	}
	check("a rebuilt state processor", node.state)
}

// With the height at its default of zero nothing in this commit applies at any
// height, whatever the two sizes say.
func TestPoolLimitsAreOffByDefaultAtEveryHeight(t *testing.T) {
	sp := poolLimitsProcessor(t, 0, 1, 4)
	sp.SetPauseView(stubPauses{"lending": true})
	owner := newKey(t)
	const late = uint64(10_000_000)
	for i, id := range []string{"a long id with spaces", "second", "third"} {
		if err := createPoolInBlock(t, sp, owner, uint64(i), id, late); err != nil {
			t.Fatalf("with the height at 0 pool %q must be created whatever the limits say: %v", id, err)
		}
	}
	sp.BeginBlock(late, time.Unix(int64(late), 0).UTC())
	defer sp.EndBlock()
	if _, _, err := sp.lendingEngine("never-created"); err != nil {
		t.Fatalf("with the height at 0 naming an unseen pool still creates it: %v", err)
	}
}

func TestValidLendingPoolIDRefusesAnEmptyID(t *testing.T) {
	if err := validLendingPoolID("", 0); !errors.Is(err, ErrLendingPoolIDInvalid) {
		t.Fatalf("an empty id: got %v, want ErrLendingPoolIDInvalid", err)
	}
	if err := validLendingPoolID("ok", 0); err != nil {
		t.Fatalf("a plain id: %v", err)
	}
}
