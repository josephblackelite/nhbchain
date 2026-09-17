package curve

import (
	"math/big"
	"testing"
	"time"
)

func wei(whole int64) *big.Int {
	return new(big.Int).Mul(big.NewInt(whole), weiPerWholeToken)
}

func TestDefaultParamsShape(t *testing.T) {
	p := DefaultParams()
	if p.TrancheSize.Cmp(big.NewInt(50000)) != 0 {
		t.Fatalf("tranche size = %v, want 50000", p.TrancheSize)
	}
	if p.TrancheCount != 16000 {
		t.Fatalf("tranche count = %d, want 16000", p.TrancheCount)
	}
	wantCap := wei(800_000_000)
	if p.SalePoolCapWei().Cmp(wantCap) != 0 {
		t.Fatalf("sale pool cap = %v, want %v", p.SalePoolCapWei(), wantCap)
	}
}

func TestTranchePrice(t *testing.T) {
	p := DefaultParams()
	price0, err := p.TranchePrice(0)
	if err != nil {
		t.Fatal(err)
	}
	if price0.Cmp(big.NewRat(5, 100)) != 0 {
		t.Fatalf("tranche 0 price = %v, want 0.05", price0)
	}

	// The LAST valid tranche's spot price (index TrancheCount-1) should sit
	// just under the $1.00 terminal price -- the curve reaches exactly
	// $1.00 only as the boundary of tranche TrancheCount, one step beyond
	// the last valid index.
	last, err := p.TranchePrice(p.TrancheCount - 1)
	if err != nil {
		t.Fatal(err)
	}
	lastF, _ := last.Float64()
	if lastF < 0.99 || lastF > 1.0 {
		t.Fatalf("last tranche spot price = %v, want in [0.99, 1.00)", lastF)
	}

	if _, err := p.TranchePrice(p.TrancheCount); err != ErrTrancheOutOfRange {
		t.Fatalf("expected ErrTrancheOutOfRange for index == TrancheCount, got %v", err)
	}
}

func TestCostOrderSplittingEquivalence(t *testing.T) {
	p := DefaultParams()
	c0 := wei(1_000_000)
	c1 := wei(1_100_000) // spans multiple 50,000-ZNHB tranches
	mid := wei(1_050_000)

	whole, err := p.Cost(c0, c1)
	if err != nil {
		t.Fatal(err)
	}
	part1, err := p.Cost(c0, mid)
	if err != nil {
		t.Fatal(err)
	}
	part2, err := p.Cost(mid, c1)
	if err != nil {
		t.Fatal(err)
	}
	sum := new(big.Rat).Add(part1, part2)
	if whole.Cmp(sum) != 0 {
		t.Fatalf("Cost(c0,c1)=%v != Cost(c0,mid)+Cost(mid,c1)=%v -- order-splitting is exploitable", whole, sum)
	}
}

func TestCostManyWaySplitEquivalence(t *testing.T) {
	p := DefaultParams()
	c0 := wei(0)
	c1 := wei(500_000)
	whole, err := p.Cost(c0, c1)
	if err != nil {
		t.Fatal(err)
	}

	total := new(big.Rat)
	step := new(big.Int).Div(new(big.Int).Sub(c1, c0), big.NewInt(10))
	cursor := new(big.Int).Set(c0)
	for i := 0; i < 10; i++ {
		next := new(big.Int).Add(cursor, step)
		if i == 9 {
			next = c1
		}
		cost, err := p.Cost(cursor, next)
		if err != nil {
			t.Fatal(err)
		}
		total.Add(total, cost)
		cursor = next
	}
	if whole.Cmp(total) != 0 {
		t.Fatalf("10-way split total=%v != single-call cost=%v -- splitting into many small purchases changed the total", total, whole)
	}
}

func TestCostRejectsOutOfRange(t *testing.T) {
	p := DefaultParams()
	if _, err := p.Cost(wei(10), wei(5)); err != ErrInvalidRange {
		t.Fatalf("expected ErrInvalidRange for c1<c0, got %v", err)
	}
	if _, err := p.Cost(big.NewInt(-1), wei(5)); err != ErrInvalidRange {
		t.Fatalf("expected ErrInvalidRange for negative c0, got %v", err)
	}
	cap := p.SalePoolCapWei()
	over := new(big.Int).Add(cap, big.NewInt(1))
	if _, err := p.Cost(wei(0), over); err != ErrExceedsSalePool {
		t.Fatalf("expected ErrExceedsSalePool, got %v", err)
	}
}

func TestCostAtExactBoundary(t *testing.T) {
	p := DefaultParams()
	cap := p.SalePoolCapWei()
	cost, err := p.Cost(big.NewInt(0), cap)
	if err != nil {
		t.Fatalf("full Sale Pool sellout should not error: %v", err)
	}
	if cost.Sign() <= 0 {
		t.Fatalf("full sellout cost should be positive, got %v", cost)
	}
}

func TestInvertByBudgetRoundTrip(t *testing.T) {
	p := DefaultParams()
	c0 := wei(2_000_000)
	budget := big.NewRat(500000, 1) // 500,000 NHB budget

	granted, spent, err := p.InvertByBudget(c0, budget)
	if err != nil {
		t.Fatal(err)
	}
	if granted.Sign() <= 0 {
		t.Fatalf("expected positive ZNHB granted, got %v", granted)
	}
	if spent.Cmp(budget) > 0 {
		t.Fatalf("spent %v exceeds budget %v", spent, budget)
	}

	recomputed, err := p.Cost(c0, new(big.Int).Add(c0, granted))
	if err != nil {
		t.Fatal(err)
	}
	if recomputed.Cmp(spent) != 0 {
		t.Fatalf("Cost(c0,c0+granted)=%v != InvertByBudget's reported spent=%v", recomputed, spent)
	}

	oneMore := new(big.Int).Add(granted, big.NewInt(1))
	if oneMore.Cmp(p.SalePoolCapWei()) <= 0 {
		costOneMore, err := p.Cost(c0, new(big.Int).Add(c0, oneMore))
		if err == nil && costOneMore.Cmp(budget) <= 0 {
			t.Fatalf("granting one more attoZNHB (%v) should have exceeded budget, cost=%v budget=%v", oneMore, costOneMore, budget)
		}
	}
}

func TestRoundCostUpNeverUndercharges(t *testing.T) {
	// (1/3) attoNHB, expressed in whole-NHB terms, has a nonzero fractional
	// attoNHB remainder and must round UP to 1, never truncate to 0.
	frac := new(big.Rat).SetFrac(big.NewInt(1), big.NewInt(3))
	frac.Quo(frac, new(big.Rat).SetInt(weiPerWholeToken))
	got := RoundCostUp(frac)
	if got.Cmp(big.NewInt(1)) != 0 {
		t.Fatalf("RoundCostUp((1/3) attoNHB) = %v, want 1 (must round up any nonzero remainder)", got)
	}

	// An already-exact attoNHB amount must round to itself, not up further.
	exact := new(big.Rat).SetFrac(big.NewInt(7), weiPerWholeToken)
	gotExact := RoundCostUp(exact)
	if gotExact.Cmp(big.NewInt(7)) != 0 {
		t.Fatalf("RoundCostUp(exact 7 attoNHB) = %v, want 7", gotExact)
	}
}

func TestRoundZNHBDownNeverOvergrants(t *testing.T) {
	amt := new(big.Rat).SetFrac(big.NewInt(29), big.NewInt(10)) // 2.9
	got := RoundZNHBDown(amt)
	if got.Cmp(big.NewInt(2)) != 0 {
		t.Fatalf("RoundZNHBDown(2.9) = %v, want 2 (must round down, never grant more than paid for)", got)
	}
}

// TestDefaultRatioConverges verifies the frozen Ratio constant, applied one
// more step past the table's last entry (price[TrancheCount-1] * Ratio),
// lands astronomically close to the intended $1.00 terminal price -- the
// drift is orders of magnitude below one attoNHB, undetectable at any
// on-chain precision. See curve.go's Params.Ratio doc comment for the
// derivation, and TestPriceTableStaysBounded below for the regression
// guard against the exact-exponentiation blowup this design replaced.
func TestDefaultRatioConverges(t *testing.T) {
	p := DefaultParams()
	terminal := new(big.Rat).Mul(p.prices[p.TrancheCount-1], p.Ratio)
	target := big.NewRat(1, 1) // $1.00
	diff := new(big.Rat).Sub(terminal, target)
	diffF, _ := diff.Float64()
	if diffF < 0 {
		diffF = -diffF
	}
	if diffF > 1e-30 {
		t.Fatalf("terminal price drift too large: %v (want << 1e-18, one attoNHB)", diffF)
	}
}

// TestPriceTableStaysBounded is a regression guard for the bug this design
// replaced: computing Ratio^i via exact exponentiation-by-squaring for
// large i causes the numerator/denominator bit-length to roughly double on
// every squaring step, producing numbers with hundreds of thousands of
// digits by i=16,000 and making the table take upwards of a minute (or
// effectively hang) to build. The rounded, iteratively-built table must
// keep every entry's numerator/denominator within a small, fixed bit
// budget, regardless of index.
func TestPriceTableStaysBounded(t *testing.T) {
	p := DefaultParams()
	const maxReasonableBits = 512 // priceRoundingScale (10^50) needs ~167 bits; generous headroom above that
	for _, idx := range []uint64{0, 1, 100, 8000, p.TrancheCount - 1} {
		price := p.prices[idx]
		if bits := price.Num().BitLen(); bits > maxReasonableBits {
			t.Fatalf("price[%d] numerator is %d bits (want <= %d) -- the table has regressed to unbounded growth", idx, bits, maxReasonableBits)
		}
		if bits := price.Denom().BitLen(); bits > maxReasonableBits {
			t.Fatalf("price[%d] denominator is %d bits (want <= %d) -- the table has regressed to unbounded growth", idx, bits, maxReasonableBits)
		}
	}
}

// TestBuildPriceTableIsFast guards against the same blowup from a
// different angle: building the full 16,000-entry table must complete in
// well under a second. Before the fix, this took upwards of a minute.
func TestBuildPriceTableIsFast(t *testing.T) {
	start := time.Now()
	p := DefaultParams()
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("building the default price table took %v, want well under 2s", elapsed)
	}
	if len(p.prices) != int(p.TrancheCount) {
		t.Fatalf("price table has %d entries, want %d", len(p.prices), p.TrancheCount)
	}
}

func TestTerminalPrice(t *testing.T) {
	p := DefaultParams()
	terminal := p.TerminalPrice()
	f, _ := terminal.Float64()
	if f < 0.999 || f > 1.001 {
		t.Fatalf("terminal price = %v, want ~$1.00", f)
	}
}

func TestAlwaysOpenGate(t *testing.T) {
	g := AlwaysOpenGate{}
	ok, err := g.IsPurchasable(12345, GateState{Height: 1, Timestamp: 1})
	if err != nil || !ok {
		t.Fatalf("AlwaysOpenGate should always report purchasable, got ok=%v err=%v", ok, err)
	}
}
