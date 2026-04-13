package lending

import "math/big"

// InterestModel encapsulates the parameters that shape how interest rates react
// to market utilisation.
type InterestModel struct {
	// BaseRate is the minimum borrow APR applied when utilisation is zero.
	BaseRate *big.Rat
	// Slope1 is the borrow APR increase per unit of utilisation up to the
	// kink point.
	Slope1 *big.Rat
	// Slope2 governs the additional APR increase applied when utilisation
	// exceeds the kink point.
	Slope2 *big.Rat
	// Kink represents the utilisation ratio where the borrow rate slope
	// changes to encourage liquidity.
	Kink *big.Rat
}

// Clone returns a deep copy of the interest model.
func (m *InterestModel) Clone() *InterestModel {
	if m == nil {
		return nil
	}
	clone := &InterestModel{
		BaseRate: new(big.Rat),
		Slope1:   new(big.Rat),
		Slope2:   new(big.Rat),
		Kink:     new(big.Rat),
	}
	if m.BaseRate != nil {
		clone.BaseRate.Set(m.BaseRate)
	}
	if m.Slope1 != nil {
		clone.Slope1.Set(m.Slope1)
	}
	if m.Slope2 != nil {
		clone.Slope2.Set(m.Slope2)
	}
	if m.Kink != nil {
		clone.Kink.Set(m.Kink)
	}
	return clone
}

// NewInterestModel constructs an interest model from floating point inputs.
//
// The parameters should be provided as decimals, e.g. a 2% base rate is
// expressed as 0.02 and an 80% kink utilisation is 0.8.
func NewInterestModel(baseRate, slope1, slope2, kink float64) *InterestModel {
	model := &InterestModel{
		BaseRate: new(big.Rat),
		Slope1:   new(big.Rat),
		Slope2:   new(big.Rat),
		Kink:     new(big.Rat),
	}
	model.BaseRate.SetFloat64(baseRate)
	model.Slope1.SetFloat64(slope1)
	model.Slope2.SetFloat64(slope2)
	model.Kink.SetFloat64(kink)
	return model
}

// Utilisation computes the pool utilisation ratio U = totalBorrowed /
// totalSupplied. When no liquidity exists the utilisation is defined as zero.
func (m *InterestModel) Utilisation(totalBorrowed, totalSupplied *big.Int) *big.Rat {
	if totalBorrowed == nil || totalBorrowed.Sign() == 0 {
		return new(big.Rat)
	}
	if totalSupplied == nil || totalSupplied.Sign() == 0 {
		return new(big.Rat)
	}
	return new(big.Rat).SetFrac(totalBorrowed, totalSupplied)
}

// BorrowAPR derives the dynamic borrow APR based on the current utilisation.
func (m *InterestModel) BorrowAPR(totalBorrowed, totalSupplied *big.Int) *big.Rat {
	if m == nil {
		return new(big.Rat)
	}
	base := cloneRat(m.BaseRate)
	utilisation := m.Utilisation(totalBorrowed, totalSupplied)
	if utilisation.Sign() == 0 {
		return base
	}

	rate := base
	kink := cloneRat(m.Kink)
	slope1 := cloneRat(m.Slope1)
	slope2 := cloneRat(m.Slope2)
	if kink.Sign() == 0 || utilisation.Cmp(kink) <= 0 {
		// Linear region before the kink.
		return rate.Add(rate, new(big.Rat).Mul(slope1, utilisation))
	}

	// Rate at the kink using slope1.
	rate.Add(rate, new(big.Rat).Mul(slope1, kink))

	// Additional rate beyond the kink using slope2.
	excess := new(big.Rat).Sub(utilisation, kink)
	if excess.Sign() < 0 {
		excess.SetInt64(0)
	}
	return rate.Add(rate, new(big.Rat).Mul(slope2, excess))
}

// SupplyAPY derives the supply APY based on the borrow APR, utilisation and
// the protocol reserve factor. The reserve factor is expected to be provided in
// basis points.
func (m *InterestModel) SupplyAPY(totalBorrowed, totalSupplied *big.Int, reserveFactorBps uint64) *big.Rat {
	if m == nil {
		return new(big.Rat)
	}

	borrowAPR := m.BorrowAPR(totalBorrowed, totalSupplied)
	if borrowAPR.Sign() == 0 {
		return new(big.Rat)
	}

	utilisation := m.Utilisation(totalBorrowed, totalSupplied)
	if utilisation.Sign() == 0 {
		return new(big.Rat)
	}

	reserveFactor := new(big.Rat).SetFrac(big.NewInt(int64(reserveFactorBps)), big.NewInt(10_000))
	oneMinusReserve := new(big.Rat).Sub(big.NewRat(1, 1), reserveFactor)
	if oneMinusReserve.Sign() < 0 {
		oneMinusReserve.SetInt64(0)
	}

	supplyAPY := new(big.Rat).Mul(borrowAPR, utilisation)
	supplyAPY.Mul(supplyAPY, oneMinusReserve)
	return supplyAPY
}

func cloneRat(r *big.Rat) *big.Rat {
	if r == nil {
		return new(big.Rat)
	}
	return new(big.Rat).Set(r)
}

// DefaultInterestModel provides a reasonable starting configuration featuring a
// kinked interest rate curve with a modest base rate.
var DefaultInterestModel = NewInterestModel(0.02, 0.15, 0.6, 0.8)
