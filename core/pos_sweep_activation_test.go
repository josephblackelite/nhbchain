package core

import (
	"testing"
	"time"
)

// The POS sweep activation height is a consensus setting: a block is built,
// validated and committed on a copy of the node's state processor, so the
// setting has to reach that processor, survive Copy, and be the same after the
// processor is rebuilt from the trie.
func TestNodePOSSweepSkipActivationHeightReachesBlockState(t *testing.T) {
	node := newTestNode(t)

	node.stateMu.RLock()
	unset := node.state.posSweepSkipActivationHeight
	node.stateMu.RUnlock()
	if unset != 0 {
		t.Fatalf("a new node has activation height %d, want 0 (off)", unset)
	}

	node.SetPOSSweepSkipActivationHeight(25)

	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	if got := node.state.posSweepSkipActivationHeight; got != 25 {
		t.Fatalf("node state has activation height %d, want 25", got)
	}
	copied, err := node.state.Copy()
	if err != nil {
		t.Fatalf("copy state: %v", err)
	}
	if got := copied.posSweepSkipActivationHeight; got != 25 {
		t.Fatalf("a copy of the state has activation height %d, want 25", got)
	}
	if err := node.rebuildStateProcessorLocked(node.state.Trie.Root()); err != nil {
		t.Fatalf("rebuild state processor: %v", err)
	}
	if got := node.state.posSweepSkipActivationHeight; got != 25 {
		t.Fatalf("a rebuilt state processor has activation height %d, want 25", got)
	}
}

// The setting is active from the block at the activation height, never before
// it, never when it is zero, and never outside a block.
func TestPOSSweepSkipActiveFromTheActivationHeight(t *testing.T) {
	node := newTestNode(t)
	node.stateMu.Lock()
	defer node.stateMu.Unlock()
	sp, err := node.state.Copy()
	if err != nil {
		t.Fatalf("copy state: %v", err)
	}
	at := time.Unix(1_700_000_000, 0).UTC()

	check := func(label string, activation, height uint64, want bool) {
		t.Helper()
		sp.SetPOSSweepSkipActivationHeight(activation)
		sp.BeginBlock(height, at)
		defer sp.EndBlock()
		if got := sp.posSweepSkipActive(); got != want {
			t.Fatalf("%s: active = %v with activation %d at height %d, want %v", label, got, activation, height, want)
		}
	}
	check("unset", 0, 1_000_000, false)
	check("block before", 25, 24, false)
	check("block at", 25, 25, true)
	check("block after", 25, 26, true)
	check("first height", 1, 1, true)

	sp.SetPOSSweepSkipActivationHeight(1)
	if sp.posSweepSkipActive() {
		t.Fatal("active outside a block")
	}
}
