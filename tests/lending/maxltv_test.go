package lending_test

import (
	"math/big"
	"strings"
	"testing"

	"nhbchain/crypto"
	"nhbchain/native/lending"
)

// TestBorrowRespectsMaxLTV proves the actual gap found in review: with
// MaxLTV=7500bps and LiquidationThreshold=8000bps (setupCapsEngine's
// defaults) against 10 tokens of collateral, a borrow that lands between
// the two -- above the 75% MaxLTV cap but still below the 80% liquidation
// threshold -- used to succeed, because only positionHealthy (the
// liquidation-threshold check) was ever enforced at borrow time. A real
// borrower could walk right up to the liquidation edge with zero safety
// buffer. It must now be rejected with errMaxLTVExceeded before ever
// reaching the liquidation-threshold check.
func TestBorrowRespectsMaxLTV(t *testing.T) {
	moduleAddr := makeAddress(crypto.NHBPrefix, 0x30)
	collateralAddr := makeAddress(crypto.ZNHBPrefix, 0x31)
	borrower := makeAddress(crypto.NHBPrefix, 0x32)
	one := mustBig("1000000000000000000")

	t.Run("borrow within MaxLTV succeeds", func(t *testing.T) {
		engine, state := setupCapsEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
			p.BorrowCaps.PerBlock = nil
		})
		engine.SetState(state)
		engine.SetBlockHeight(10)

		// 10 collateral * 75% MaxLTV = 7.5 max. Borrowing 7 stays under it.
		if _, err := engine.Borrow(borrower, new(big.Int).Mul(one, big.NewInt(7)), crypto.Address{}, 0); err != nil {
			t.Fatalf("expected borrow within MaxLTV to succeed, got %v", err)
		}
	})

	t.Run("borrow between MaxLTV and liquidation threshold is rejected", func(t *testing.T) {
		engine, state := setupCapsEngine(moduleAddr, collateralAddr, borrower, func(p *lending.RiskParameters) {
			p.BorrowCaps.PerBlock = nil
		})
		engine.SetState(state)
		engine.SetBlockHeight(10)

		// 10 collateral: MaxLTV caps debt at 7.5, LiquidationThreshold at 8.
		// 7.8 is comfortably below the old (only) check but must now fail
		// against the new one.
		amount := mustBig("7800000000000000000")
		_, err := engine.Borrow(borrower, amount, crypto.Address{}, 0)
		if err == nil {
			t.Fatalf("expected MaxLTV rejection for a borrow within the old liquidation threshold but over the new cap")
		}
		if !strings.Contains(err.Error(), "maximum loan-to-value") {
			t.Fatalf("expected MaxLTV error, got %v", err)
		}
	})
}
