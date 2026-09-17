package modules

import (
	"math/big"
	"testing"

	"nhbchain/native/lending"
)

// TestApplyComputedAPYDeductsProtocolFee is the regression test for the
// bug where DepositApyBps only deducted reserveFactorBps, never
// protocolFeeBps, silently overstating real depositor yield by the
// protocol fee's share whenever one was configured. Mirrors
// native/lending/engine.go's own combine-then-cap-at-10000bps step exactly.
func TestApplyComputedAPYDeductsProtocolFee(t *testing.T) {
	node := newLendingTestNode(t)
	model := lending.NewInterestModel(0.02, 0.10, 0.75, 0.80)

	const reserveBps = uint64(1000)  // 10%
	const protocolBps = uint64(500)  // 5%
	node.SetLendingAccrualConfig(reserveBps, protocolBps, model)

	module := NewLendingModule(node)
	market := &lending.Market{
		TotalNHBBorrowed: big.NewInt(600),
		TotalNHBSupplied: big.NewInt(1000),
	}
	module.applyComputedAPY(market)

	wantSupplyAPY := model.SupplyAPY(market.TotalNHBBorrowed, market.TotalNHBSupplied, reserveBps+protocolBps)
	wantBps := lending.RateBps(wantSupplyAPY)
	if market.DepositApyBps != wantBps {
		t.Fatalf("DepositApyBps = %d, want %d (reserveBps+protocolBps combined)", market.DepositApyBps, wantBps)
	}

	// The actual bug: computing with reserveBps ALONE (the old, wrong
	// behavior) must give a strictly HIGHER (never-deducted-enough) figure
	// than the fixed combined computation, proving the fee genuinely
	// changes the reported number rather than being a no-op.
	reserveOnlyAPY := model.SupplyAPY(market.TotalNHBBorrowed, market.TotalNHBSupplied, reserveBps)
	reserveOnlyBps := lending.RateBps(reserveOnlyAPY)
	if market.DepositApyBps >= reserveOnlyBps {
		t.Fatalf("DepositApyBps (%d) should be strictly lower than the reserve-only figure (%d) once a protocol fee is configured", market.DepositApyBps, reserveOnlyBps)
	}
}

// TestApplyComputedAPYCapsCombinedFeeAt10000Bps mirrors engine.go's own
// cap so a misconfigured reserve+protocol fee summing over 100% can never
// drive the supply rate negative/nonsensical.
func TestApplyComputedAPYCapsCombinedFeeAt10000Bps(t *testing.T) {
	node := newLendingTestNode(t)
	model := lending.NewInterestModel(0.02, 0.10, 0.75, 0.80)

	const reserveBps = uint64(7000)
	const protocolBps = uint64(6000) // 7000+6000 = 13000, over the 10000 cap
	node.SetLendingAccrualConfig(reserveBps, protocolBps, model)

	module := NewLendingModule(node)
	market := &lending.Market{
		TotalNHBBorrowed: big.NewInt(600),
		TotalNHBSupplied: big.NewInt(1000),
	}
	module.applyComputedAPY(market)

	wantSupplyAPY := model.SupplyAPY(market.TotalNHBBorrowed, market.TotalNHBSupplied, 10_000)
	wantBps := lending.RateBps(wantSupplyAPY)
	if market.DepositApyBps != wantBps {
		t.Fatalf("DepositApyBps = %d, want %d (combined bps capped at 10000)", market.DepositApyBps, wantBps)
	}
}

// TestApplyComputedAPYZeroProtocolFeeMatchesReserveOnly confirms the fix is
// a pure addition, not a behavior change, when no protocol fee is
// configured (protocolBps == 0, the default/most common configuration).
func TestApplyComputedAPYZeroProtocolFeeMatchesReserveOnly(t *testing.T) {
	node := newLendingTestNode(t)
	model := lending.NewInterestModel(0.02, 0.10, 0.75, 0.80)

	const reserveBps = uint64(1000)
	node.SetLendingAccrualConfig(reserveBps, 0, model)

	module := NewLendingModule(node)
	market := &lending.Market{
		TotalNHBBorrowed: big.NewInt(600),
		TotalNHBSupplied: big.NewInt(1000),
	}
	module.applyComputedAPY(market)

	wantSupplyAPY := model.SupplyAPY(market.TotalNHBBorrowed, market.TotalNHBSupplied, reserveBps)
	wantBps := lending.RateBps(wantSupplyAPY)
	if market.DepositApyBps != wantBps {
		t.Fatalf("DepositApyBps = %d, want %d (protocolBps=0 must match reserve-only math exactly)", market.DepositApyBps, wantBps)
	}
}
