package core

import (
	"math/big"
	"testing"
)

// The read-only views the RPC server answers from (bridge_getStatus, amm_getPool, amm_quote, amm_getTrades) on a real
// node: they report the settings, never write, and say "off" when the module is off.
func TestNodeBridgeAndPoolReads(t *testing.T) {
	node := newTestNode(t)

	off, err := node.BridgeStatus()
	if err != nil {
		t.Fatalf("bridge status: %v", err)
	}
	if off.Active || off.LockedWei.Sign() != 0 || off.NextLockID != 1 || off.Guardian != "" || len(off.Signers) != 0 || off.Paused || !off.EscrowCovered {
		t.Fatalf("an unconfigured bridge reports %+v", off)
	}
	pool, err := node.AmmPool()
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if pool.Active || pool.SwapsOpen || pool.ReserveZnhbWei.Sign() != 0 || pool.PriceNhbPerZnhb != "" {
		t.Fatalf("an unconfigured pool reports %+v", pool)
	}
	if _, err := node.AmmQuote(AmmDirZnhbToNhb, big.NewInt(1)); err == nil {
		t.Fatalf("a quote from an empty pool must fail")
	}
	if _, err := node.AmmQuote(9, big.NewInt(1)); err == nil {
		t.Fatalf("a quote in an unknown direction must fail")
	}
	trades, err := node.AmmTrades(10)
	if err != nil || len(trades) != 0 {
		t.Fatalf("trades from an unused pool: %v %v", trades, err)
	}

	// Configured and activated at height 1 (the next block of a fresh chain): the views say so.
	node.SetBridgeAndAmm(
		BridgeSettings{ActivationHeight: 1, Params: []BridgeParams{testBridgeParams(1)}, MaxLockWei: big.NewInt(77)},
		AmmSettings{ActivationHeight: 1, SwapStartHeight: 1, FeeBps: 30, MaxSwapBps: 500, MinReserveZNHB: big.NewInt(1), MinReserveNHB: big.NewInt(1)},
	)
	on, err := node.BridgeStatus()
	if err != nil {
		t.Fatal(err)
	}
	if !on.Active || on.Guardian == "" || len(on.Signers) != 3 || on.Threshold != 2 || on.MaxReleaseWei == nil || on.MaxReleaseWei.Cmp(big.NewInt(1_000)) != 0 ||
		on.MaxLockWei == nil || on.MaxLockWei.Cmp(big.NewInt(77)) != 0 || on.EscrowAddress == "" {
		t.Fatalf("an activated bridge reports %+v", on)
	}
	p2, err := node.AmmPool()
	if err != nil {
		t.Fatal(err)
	}
	if !p2.Active || !p2.SwapsOpen || p2.FeeBps != 30 || p2.PoolAddress == "" {
		t.Fatalf("an activated pool reports %+v", p2)
	}
}
