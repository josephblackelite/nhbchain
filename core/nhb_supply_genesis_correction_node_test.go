package core

import (
	"math/big"
	"testing"

	nhbstate "nhbchain/core/state"
)

func nodeNHBSupply(t *testing.T, node *Node) *big.Int {
	t.Helper()
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	got, err := nhbstate.NewManager(node.state.Trie).TokenSupply("NHB")
	if err != nil {
		t.Fatalf("read NHB supply: %v", err)
	}
	return got
}

func setNodeChainID(node *Node, id uint64) {
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	node.state.SetSwapVoucherChainID(id)
}

// commitEmptyBlock builds an empty block, validates it the way a peer would and
// commits it, so the builder, validator and commit paths all run.
func commitEmptyBlock(t *testing.T, node *Node) uint64 {
	t.Helper()
	block, err := node.CreateBlock(nil)
	if err != nil {
		t.Fatalf("create block: %v", err)
	}
	if err := node.ValidateBlock(block); err != nil {
		t.Fatalf("validate block %d: %v", block.Header.Height, err)
	}
	if err := node.CommitBlock(block); err != nil {
		t.Fatalf("commit block %d: %v", block.Header.Height, err)
	}
	return block.Header.Height
}

// Through real blocks: the old binary's seed (and the mint drift repair) land in
// block 1 on a node whose chain id is not yet set; the node is then switched to
// the live chain id, as a real node is by its genesis, and the setting is turned
// on for height 3. The counter must stay put through block 2, rise by exactly
// 90,000 NHB in block 3 and stay put afterwards, on every path a block takes.
func TestNodeNHBSupplyGenesisCorrection_RisesOnceAtTheConfiguredHeight(t *testing.T) {
	node := newTestNode(t)
	setNodeChainID(node, 0)
	if h := commitEmptyBlock(t, node); h != 1 {
		t.Fatalf("expected the first committed block to be height 1, got %d", h)
	}
	afterSeed := nodeNHBSupply(t, node)
	if want := new(big.Int).Add(genesisNHBSupplyWei, nhbMintSupplyDriftWei); afterSeed.Cmp(want) != 0 {
		t.Fatalf("block 1 on a chain with no chain id set should seed the old figures: got %s want %s", afterSeed, want)
	}

	setNodeChainID(node, liveChainIDForSupplyTests)
	node.SetNHBSupplyGenesisCorrectionActivationHeight(3)

	if h := commitEmptyBlock(t, node); h != 2 {
		t.Fatalf("expected height 2, got %d", h)
	}
	if got := nodeNHBSupply(t, node); got.Cmp(afterSeed) != 0 {
		t.Fatalf("block 2 is below the height; the counter moved %s -> %s", afterSeed, got)
	}

	if h := commitEmptyBlock(t, node); h != 3 {
		t.Fatalf("expected height 3, got %d", h)
	}
	ninety, _ := new(big.Int).SetString("90000000000000000000000", 10)
	want := new(big.Int).Add(afterSeed, ninety)
	if got := nodeNHBSupply(t, node); got.Cmp(want) != 0 {
		t.Fatalf("block 3 should raise the counter by exactly 90,000 NHB: got %s want %s", got, want)
	}

	for i := 0; i < 2; i++ {
		commitEmptyBlock(t, node)
	}
	if got := nodeNHBSupply(t, node); got.Cmp(want) != 0 {
		t.Fatalf("blocks after the correction moved the counter: %s want %s", got, want)
	}
}

// The same run with the setting at its default never moves the counter.
func TestNodeNHBSupplyGenesisCorrection_DefaultNeverMovesTheCounter(t *testing.T) {
	node := newTestNode(t)
	setNodeChainID(node, 0)
	commitEmptyBlock(t, node)
	afterSeed := nodeNHBSupply(t, node)
	setNodeChainID(node, liveChainIDForSupplyTests)
	for i := 0; i < 6; i++ {
		commitEmptyBlock(t, node)
	}
	if got := nodeNHBSupply(t, node); got.Cmp(afterSeed) != 0 {
		t.Fatalf("with the setting at 0 the counter moved: %s -> %s", afterSeed, got)
	}
}

// The setter reaches the processor, and a processor the node rebuilds carries it.
func TestNodeNHBSupplyGenesisCorrection_SetterReachesTheProcessor(t *testing.T) {
	node := newTestNode(t)
	node.SetNHBSupplyGenesisCorrectionActivationHeight(7)
	node.stateMu.Lock()
	got := node.state.nhbSupplyGenesisCorrectionFromHeight
	held := node.nhbSupplyGenesisCorrectionHeight
	node.stateMu.Unlock()
	if got != 7 || held != 7 {
		t.Fatalf("setter left processor=%d node=%d, want 7 and 7", got, held)
	}

	node.stateMu.Lock()
	err := node.rebuildStateProcessorLocked(node.state.Trie.Root())
	rebuilt := node.state.nhbSupplyGenesisCorrectionFromHeight
	node.stateMu.Unlock()
	if err != nil {
		t.Fatalf("rebuild state processor: %v", err)
	}
	if rebuilt != 7 {
		t.Fatalf("a rebuilt state processor lost the setting: %d, want 7", rebuilt)
	}
}
