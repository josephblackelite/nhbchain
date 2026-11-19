package core

import (
	"math"
	"testing"
	"time"
)

// Early in a chain the block time is smaller than the age bound: nothing is older
// than the window yet, and the subtraction must not wrap.
func TestRefPriceAgeBoundDoesNotUnderflowEarlyInTheChain(t *testing.T) {
	sp, key := newLendingRefPriceTestState(t)
	sp.SetLendingRefPriceGuardsActivationHeight(refGuardActivation)
	sp.SetLendingRefPriceMaxAgeSeconds(3_600)
	sp.BeginBlock(refGuardActivation, time.Unix(100, 0).UTC())
	defer sp.EndBlock()
	num, den := onePerWhole()
	if err := submitRawRefPrice(t, sp, key, num, den, 5); err != nil {
		t.Fatalf("a bundle dated 95 s before a block at time 100 is within a 3,600 s window: %v", err)
	}
}

// A bound as large as a number can be must not wrap the sum with the block time.
func TestRefPriceFutureBoundDoesNotOverflow(t *testing.T) {
	sp, key := refGuardProcessor(t, refGuardActivation, math.MaxUint64, 0, refGuardActivation)
	num, den := onePerWhole()
	if err := submitRawRefPrice(t, sp, key, num, den, uint64(refGuardBlockTime)+10); err != nil {
		t.Fatalf("with an enormous future bound a bundle 10 s ahead is accepted: %v", err)
	}
}
