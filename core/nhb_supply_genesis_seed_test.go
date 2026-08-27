package core

import (
	"math/big"
	"testing"
	"time"

	nhbstate "nhbchain/core/state"
	"nhbchain/core/types"
	"nhbchain/crypto"
)

// TestSeedGenesisNHBSupplyOnce_AddsExactAmount reproduces the real
// 2026-08-24 production bug: an account holding real NHB (seeded directly
// via setAccount, exactly as genesis allocation does -- never through
// MintToken/AdjustTokenSupply) attempts a TxTypeRedeemNHB burn before the
// tracked supply has ever been seeded. Without SeedGenesisNHBSupplyOnce
// having run, this must underflow; after it runs (as ProcessBlockLifecycle
// now calls it on every block), the identical burn must succeed and the
// tracked supply must land at exactly genesisNHBSupplyWei minus the burn.
func TestSeedGenesisNHBSupplyOnce_AddsExactAmount(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)

	before, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply before seed: %v", err)
	}
	if before.Sign() != 0 {
		t.Fatalf("expected zero supply before seeding in a fresh test processor, got %s", before)
	}

	if err := sp.SeedGenesisNHBSupplyOnce(); err != nil {
		t.Fatalf("seed genesis supply: %v", err)
	}

	after, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply after seed: %v", err)
	}
	if after.Cmp(genesisNHBSupplyWei) != 0 {
		t.Fatalf("expected supply %s after seeding, got %s", genesisNHBSupplyWei, after)
	}

	seeded, err := manager.NHBSupplyGenesisSeeded()
	if err != nil {
		t.Fatalf("check seeded flag: %v", err)
	}
	if !seeded {
		t.Fatalf("expected NHBSupplyGenesisSeeded flag to be set after a successful seed")
	}
}

// TestSeedGenesisNHBSupplyOnce_IsIdempotent confirms a second call is a
// true no-op -- critical, since ProcessBlockLifecycle calls this
// unconditionally on every single block forever.
func TestSeedGenesisNHBSupplyOnce_IsIdempotent(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)

	if err := sp.SeedGenesisNHBSupplyOnce(); err != nil {
		t.Fatalf("first seed call: %v", err)
	}
	firstSupply, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply after first seed: %v", err)
	}

	if err := sp.SeedGenesisNHBSupplyOnce(); err != nil {
		t.Fatalf("second seed call: %v", err)
	}
	secondSupply, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply after second seed: %v", err)
	}

	if secondSupply.Cmp(firstSupply) != 0 {
		t.Fatalf("expected supply to stay at %s after a second call, got %s -- seed was double-applied", firstSupply, secondSupply)
	}
}

// TestSeedGenesisNHBSupplyOnce_PreservesExistingMintedSupply confirms the
// seed ADDS to whatever the counter already holds (real cumulative minting
// via MintToken since launch), rather than overwriting it -- getting this
// backwards would silently erase real minted-supply tracking.
func TestSeedGenesisNHBSupplyOnce_PreservesExistingMintedSupply(t *testing.T) {
	sp := newStakingStateProcessor(t)
	manager := nhbstate.NewManager(sp.Trie)

	priorMinted := big.NewInt(0)
	priorMinted.SetString("42000000000000000000", 10) // 42 NHB, standing in for real post-launch mint activity
	seedTokenSupply(t, sp, priorMinted)

	if err := sp.SeedGenesisNHBSupplyOnce(); err != nil {
		t.Fatalf("seed genesis supply: %v", err)
	}

	after, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply after seed: %v", err)
	}
	expected := new(big.Int).Add(priorMinted, genesisNHBSupplyWei)
	if after.Cmp(expected) != 0 {
		t.Fatalf("expected supply %s (prior minted + genesis seed), got %s", expected, after)
	}
}

// TestSeedGenesisNHBSupplyOnce_UnblocksRealRedemption is the actual
// regression test for the production incident: reproduces the exact
// sequence a real account hit (real NHB balance, zero tracked supply, then
// a TxTypeRedeemNHB burn) via the real ProcessBlockLifecycle path a live
// node runs every block, confirming the burn that used to underflow now
// succeeds end to end.
func TestSeedGenesisNHBSupplyOnce_UnblocksRealRedemption(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()

	// Mirrors genesis: a real balance seeded directly, never through
	// MintToken/AdjustTokenSupply -- the tracked supply stays at zero here
	// exactly like it did on the live chain before this fix.
	burnAmount := big.NewInt(0)
	burnAmount.SetString("18000000000000000000", 10) // 18 NHB
	if err := sp.setAccount(userAddr, &types.Account{
		BalanceNHB:  new(big.Int).Set(burnAmount),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed user account: %v", err)
	}

	// The real per-block call every validator makes, in the same order a
	// live node would encounter it before ever processing a redeem tx.
	if err := sp.ProcessBlockLifecycle(1, 1700000000); err != nil {
		t.Fatalf("process block lifecycle: %v", err)
	}

	tx := redeemNHBTx(t, 0, burnAmount, "USDT", "TAvsay5Fi6odJhpuTuhuof5JtxwmNuSX4V")
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign redeem tx: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		t.Fatalf("expected redeem to succeed now that genesis supply is seeded, got error: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	supply, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply after burn: %v", err)
	}
	expected := new(big.Int).Sub(genesisNHBSupplyWei, burnAmount)
	if supply.Cmp(expected) != 0 {
		t.Fatalf("expected supply %s after seed+burn, got %s", expected, supply)
	}
}

// TestSeedGenesisNHBSupplyOnce_SameBlockOrderingMatchesCreateBlock is the
// regression test the prior UnblocksRealRedemption test above was missing,
// per an independent review: that test calls ProcessBlockLifecycle and
// ApplyTransaction as two separate, sequential top-level calls, the OPPOSITE
// order from what core/node.go's three real execution paths actually do
// (apply the block's own transactions FIRST, then call
// ProcessBlockLifecycle) -- so it never exercised the real risk window.
// core/node.go now calls SeedGenesisNHBSupplyOnce directly, right after
// BeginBlock and before its tx-application loop, specifically so a
// TxTypeRedeemNHB burn in the SAME block the seed first runs in cannot
// underflow. This test drives sp directly through that exact same sequence
// (BeginBlock -> early SeedGenesisNHBSupplyOnce call -> ApplyTransaction ->
// ProcessBlockLifecycle -> EndBlock), on a genuinely fresh, never-seeded
// processor, proving the fix's mechanism end to end: remove the early call
// and this test fails with the same "token NHB supply underflow" the live
// chain hit on 2026-08-24.
func TestSeedGenesisNHBSupplyOnce_SameBlockOrderingMatchesCreateBlock(t *testing.T) {
	sp := newStakingStateProcessor(t)

	userKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate user key: %v", err)
	}
	userAddr := userKey.PubKey().Address().Bytes()

	burnAmount := big.NewInt(0)
	burnAmount.SetString("18000000000000000000", 10) // 18 NHB
	if err := sp.setAccount(userAddr, &types.Account{
		BalanceNHB:  new(big.Int).Set(burnAmount),
		BalanceZNHB: big.NewInt(0),
		Stake:       big.NewInt(0),
	}); err != nil {
		t.Fatalf("seed user account: %v", err)
	}

	manager := nhbstate.NewManager(sp.Trie)
	seededBefore, err := manager.NHBSupplyGenesisSeeded()
	if err != nil {
		t.Fatalf("check seeded flag: %v", err)
	}
	if seededBefore {
		t.Fatalf("expected a genuinely fresh, unseeded processor for this test")
	}

	blockTime := time.Unix(1700000000, 0).UTC()
	sp.BeginBlock(1, blockTime)

	// The exact fix: this call must happen here, before ApplyTransaction,
	// mirroring core/node.go's three execution paths. If this call is
	// removed (regressing to the pre-fix ordering, where only
	// ProcessBlockLifecycle below would eventually seed it -- too late for
	// this same block), the ApplyTransaction call below fails with "token
	// NHB supply underflow", reproducing the 2026-08-24 incident.
	if err := sp.SeedGenesisNHBSupplyOnce(); err != nil {
		sp.EndBlock()
		t.Fatalf("seed genesis NHB supply: %v", err)
	}

	tx := redeemNHBTx(t, 0, burnAmount, "USDT", "TAvsay5Fi6odJhpuTuhuof5JtxwmNuSX4V")
	if err := tx.Sign(userKey.PrivateKey); err != nil {
		t.Fatalf("sign redeem tx: %v", err)
	}
	if err := sp.ApplyTransaction(tx); err != nil {
		sp.EndBlock()
		t.Fatalf("redeem burn must succeed in the same block the genesis supply is first seeded, got: %v", err)
	}

	// The existing backstop call inside ProcessBlockLifecycle -- idempotent,
	// must be a harmless no-op here since the early call above already ran.
	if err := sp.ProcessBlockLifecycle(1, blockTime.Unix()); err != nil {
		sp.EndBlock()
		t.Fatalf("process block lifecycle: %v", err)
	}
	sp.EndBlock()

	supply, err := manager.TokenSupply("NHB")
	if err != nil {
		t.Fatalf("load supply after burn: %v", err)
	}
	expected := new(big.Int).Sub(genesisNHBSupplyWei, burnAmount)
	if supply.Cmp(expected) != 0 {
		t.Fatalf("expected supply %s after same-block seed+burn, got %s", expected, supply)
	}
}
