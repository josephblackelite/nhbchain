package core

import (
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
)

// PL-R1-SUPPLY90K. The live chain's genesis puts 100,000 NHB straight into
// account state without touching the tracked supply counter (token/supply/NHB).
// The binary that produced block 1 then seeded the counter with 10,000 NHB, the
// figure of an older network's genesis, so the counter started 90,000 NHB short
// and has stayed exactly that far short ever since (measured on the live state at
// height 2,321,017: the 98 account balances sum to 90,709.812525 NHB, the counter
// reads 709.812525 NHB, the difference is 90,000 NHB to the wei). The counter
// refuses a burn larger than itself, so redemptions above about 709 NHB cumulative
// net would fail with a supply underflow.
//
// ReconcileNHBGenesisShortfallOnce raises the counter once, from a configured
// height, by the allocation it never counted. The numbers below are written out
// here on purpose, not taken from the constants under test.

// liveChainIDForSupplyTests is the live network's chain id.
const liveChainIDForSupplyTests uint64 = 18346390202490284624

func supplyNHB(t *testing.T, sp *StateProcessor) *big.Int {
	t.Helper()
	got, err := nhbstate.NewManager(sp.Trie).TokenSupply("NHB")
	if err != nil {
		t.Fatalf("read NHB supply: %v", err)
	}
	return got
}

// liveShapedProcessor is a processor in the live chain's state shape: the old
// binary's seed ran (flag set) and the counter holds the live reading.
func liveShapedProcessor(t *testing.T, gate uint64, chainID uint64) *StateProcessor {
	t.Helper()
	sp := newStakingStateProcessor(t)
	sp.SetSwapVoucherChainID(chainID)
	sp.SetNHBSupplyGenesisCorrectionActivationHeight(gate)
	manager := nhbstate.NewManager(sp.Trie)
	counter, _ := new(big.Int).SetString("709812525000000000000", 10) // 709.812525 NHB
	if err := manager.SetTokenSupply("NHB", counter); err != nil {
		t.Fatalf("seed counter: %v", err)
	}
	if err := manager.MarkNHBSupplyGenesisSeeded(); err != nil {
		t.Fatalf("mark seeded: %v", err)
	}
	return sp
}

func TestNHBGenesisShortfall_OffByDefault(t *testing.T) {
	sp := liveShapedProcessor(t, 0, liveChainIDForSupplyTests)
	sp.BeginBlock(5_000_000, time.Unix(1_800_000_000, 0))
	before := supplyNHB(t, sp)
	if err := sp.ReconcileNHBGenesisShortfallOnce(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if after := supplyNHB(t, sp); after.Cmp(before) != 0 {
		t.Fatalf("setting 0 must leave the counter alone: %s -> %s", before, after)
	}
	done, err := nhbstate.NewManager(sp.Trie).NHBGenesisShortfallCorrected()
	if err != nil || done {
		t.Fatalf("setting 0 must not mark the correction done (done=%v err=%v)", done, err)
	}
	if len(sp.Events()) != 0 {
		t.Fatalf("setting 0 must emit nothing, got %d events", len(sp.Events()))
	}
}

func TestNHBGenesisShortfall_NotBeforeTheHeight(t *testing.T) {
	sp := liveShapedProcessor(t, 100, liveChainIDForSupplyTests)
	sp.BeginBlock(99, time.Unix(1_800_000_000, 0))
	before := supplyNHB(t, sp)
	if err := sp.ReconcileNHBGenesisShortfallOnce(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if after := supplyNHB(t, sp); after.Cmp(before) != 0 {
		t.Fatalf("below the height the counter must not move: %s -> %s", before, after)
	}
}

func TestNHBGenesisShortfall_AddsExactlyNinetyThousandOnTheLiveChain(t *testing.T) {
	sp := liveShapedProcessor(t, 100, liveChainIDForSupplyTests)
	sp.BeginBlock(100, time.Unix(1_800_000_000, 0))
	if err := sp.ReconcileNHBGenesisShortfallOnce(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	want, _ := new(big.Int).SetString("90709812525000000000000", 10) // 90,709.812525 NHB
	if got := supplyNHB(t, sp); got.Cmp(want) != 0 {
		t.Fatalf("counter after the correction = %s, want %s", got, want)
	}
	done, err := nhbstate.NewManager(sp.Trie).NHBGenesisShortfallCorrected()
	if err != nil || !done {
		t.Fatalf("the correction must be marked done (done=%v err=%v)", done, err)
	}
	var found bool
	for _, ev := range sp.Events() {
		if ev.Type == "token.supply" && ev.Attributes["reason"] == "genesis_correction" {
			found = true
			if ev.Attributes["delta"] != "90000000000000000000000" || ev.Attributes["token"] != "NHB" || ev.Attributes["total"] != want.String() {
				t.Fatalf("supply event carries the wrong figures: %v", ev.Attributes)
			}
		}
	}
	if !found {
		t.Fatalf("no token.supply event with reason genesis_correction was emitted: %v", sp.Events())
	}
}

func TestNHBGenesisShortfall_RunsOnceOnly(t *testing.T) {
	sp := liveShapedProcessor(t, 100, liveChainIDForSupplyTests)
	sp.BeginBlock(100, time.Unix(1_800_000_000, 0))
	if err := sp.ReconcileNHBGenesisShortfallOnce(); err != nil {
		t.Fatalf("first: %v", err)
	}
	after := supplyNHB(t, sp)
	eventsAfter := len(sp.Events())
	for _, height := range []uint64{100, 101, 5_000} {
		sp.BeginBlock(height, time.Unix(1_800_000_000, 0))
		if err := sp.ReconcileNHBGenesisShortfallOnce(); err != nil {
			t.Fatalf("repeat at %d: %v", height, err)
		}
	}
	if again := supplyNHB(t, sp); again.Cmp(after) != 0 {
		t.Fatalf("a second run moved the counter: %s -> %s", after, again)
	}
	if len(sp.Events()) != eventsAfter {
		t.Fatalf("a second run emitted events")
	}
}

// A chain whose counter was never seeded (a node that replays from block 1 with
// a binary that no longer seeds) is short by the whole allocation.
func TestNHBGenesisShortfall_UnseededChainIsShortByTheWholeAllocation(t *testing.T) {
	sp := newStakingStateProcessor(t)
	sp.SetSwapVoucherChainID(liveChainIDForSupplyTests)
	sp.SetNHBSupplyGenesisCorrectionActivationHeight(1)
	sp.BeginBlock(1, time.Unix(1_800_000_000, 0))
	if err := sp.ReconcileNHBGenesisShortfallOnce(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	want, _ := new(big.Int).SetString("100000000000000000000000", 10)
	if got := supplyNHB(t, sp); got.Cmp(want) != 0 {
		t.Fatalf("unseeded counter after the correction = %s, want %s", got, want)
	}
}

// The amount is a fact about one network's genesis. Any other chain id, and an
// unset one, is left alone (it fails closed, unlike the older seed).
func TestNHBGenesisShortfall_OtherChainsAreLeftAlone(t *testing.T) {
	for name, chainID := range map[string]uint64{
		"unset chain id":      0,
		"the older network":   MintChainID,
		"live id plus one":    liveChainIDForSupplyTests + 1,
		"live id minus one":   liveChainIDForSupplyTests - 1,
		"a small test chain":  1337,
		"all bits set (wrap)": ^uint64(0),
	} {
		t.Run(name, func(t *testing.T) {
			sp := liveShapedProcessor(t, 1, chainID)
			sp.BeginBlock(10, time.Unix(1_800_000_000, 0))
			before := supplyNHB(t, sp)
			if err := sp.ReconcileNHBGenesisShortfallOnce(); err != nil {
				t.Fatalf("reconcile: %v", err)
			}
			if after := supplyNHB(t, sp); after.Cmp(before) != 0 {
				t.Fatalf("chain id %d: counter moved %s -> %s", chainID, before, after)
			}
			done, _ := nhbstate.NewManager(sp.Trie).NHBGenesisShortfallCorrected()
			if done {
				t.Fatalf("chain id %d: marked done though it was skipped", chainID)
			}
		})
	}
}

// The reason the counter matters: it refuses a burn larger than itself.
func TestNHBGenesisShortfall_UnblocksBurnsAboveTheOldHeadroom(t *testing.T) {
	sp := liveShapedProcessor(t, 100, liveChainIDForSupplyTests)
	manager := nhbstate.NewManager(sp.Trie)
	burn := new(big.Int).Neg(nhbAmount(800)) // more than the 709.81 NHB the counter holds
	if _, err := manager.AdjustTokenSupply("NHB", burn); err == nil {
		t.Fatalf("control: a burn of 800 NHB against a counter of 709.81 NHB must underflow")
	}
	sp.BeginBlock(100, time.Unix(1_800_000_000, 0))
	if err := sp.ReconcileNHBGenesisShortfallOnce(); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	after, err := manager.AdjustTokenSupply("NHB", burn)
	if err != nil {
		t.Fatalf("after the correction an 800 NHB burn must succeed: %v", err)
	}
	if want, _ := new(big.Int).SetString("89909812525000000000000", 10); after.Cmp(want) != 0 {
		t.Fatalf("counter after the burn = %s, want %s", after, want)
	}
}

// The copies a block is built and validated on come from Copy; a copy that lost
// the setting would never correct (or, for a setting whose off value were the
// largest number, would correct from height 0).
func TestNHBGenesisShortfall_TheSettingSurvivesCopy(t *testing.T) {
	sp := liveShapedProcessor(t, 100, liveChainIDForSupplyTests)
	copied, err := sp.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	copied.BeginBlock(100, time.Unix(1_800_000_000, 0))
	if err := copied.ReconcileNHBGenesisShortfallOnce(); err != nil {
		t.Fatalf("reconcile on the copy: %v", err)
	}
	want, _ := new(big.Int).SetString("90709812525000000000000", 10)
	if got := supplyNHB(t, copied); got.Cmp(want) != 0 {
		t.Fatalf("the copy did not correct: %s, want %s", got, want)
	}
	// And the copy of a processor with the setting off stays off.
	off := liveShapedProcessor(t, 0, liveChainIDForSupplyTests)
	offCopy, err := off.Copy()
	if err != nil {
		t.Fatalf("copy: %v", err)
	}
	offCopy.BeginBlock(5_000_000, time.Unix(1_800_000_000, 0))
	before := supplyNHB(t, offCopy)
	if err := offCopy.ReconcileNHBGenesisShortfallOnce(); err != nil {
		t.Fatalf("reconcile on the off copy: %v", err)
	}
	if after := supplyNHB(t, offCopy); after.Cmp(before) != 0 {
		t.Fatalf("a copy of an off processor corrected anyway")
	}
}

// Every place that runs the genesis seed (the block builder, the validator, the
// commit path, the containment re-build and the lifecycle) has to run the
// correction too, or the nodes disagree on the block that crosses the height.
func TestNHBGenesisShortfall_EverySeedCallSiteAlsoCorrects(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	seed := regexp.MustCompile(`\.SeedGenesisNHBSupplyOnce\(\)`)
	fix := regexp.MustCompile(`\.ReconcileNHBGenesisShortfallOnce\(\)`)
	var sites int
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		a, b := len(seed.FindAll(data, -1)), len(fix.FindAll(data, -1))
		sites += a
		if a != b {
			t.Errorf("%s calls the genesis seed %d times and the shortfall correction %d times; every seed call site must be followed by the correction", name, a, b)
		}
	}
	if sites < 4 {
		t.Fatalf("found only %d seed call sites; the scan no longer finds them", sites)
	}
}
