package lending

import "math/big"

var (
	basisPoints  = big.NewInt(10_000)
	ray          = mustBigInt("1000000000000000000000000000") // 1e27 precision
	halfRay      = new(big.Int).Rsh(ray, 1)
	minLiquidity = mustBigInt("1000000000000000000") // 1 NHB in wei required at bootstrap
)

func mustBigInt(value string) *big.Int {
	v, ok := new(big.Int).SetString(value, 10)
	if !ok {
		panic("invalid big integer constant")
	}
	return v
}

func rayMul(a, b *big.Int) *big.Int {
	if a == nil || b == nil {
		return big.NewInt(0)
	}
	product := new(big.Int).Mul(a, b)
	product.Add(product, halfRay)
	product.Quo(product, ray)
	return product
}

func rayDiv(a, b *big.Int) *big.Int {
	if a == nil || b == nil || b.Sign() == 0 {
		return big.NewInt(0)
	}
	numerator := new(big.Int).Mul(a, ray)
	numerator.Add(numerator, halfUp(b))
	numerator.Quo(numerator, b)
	return numerator
}

func ratToRay(r *big.Rat) *big.Int {
	if r == nil {
		return new(big.Int).Set(ray)
	}
	scaled := new(big.Rat).Mul(r, new(big.Rat).SetInt(ray))
	num := scaled.Num()
	den := scaled.Denom()
	if den.Sign() == 0 {
		return new(big.Int).Set(ray)
	}
	result := new(big.Int).Quo(new(big.Int).Add(num, halfUp(den)), den)
	if result.Sign() == 0 {
		return new(big.Int).Set(ray)
	}
	return result
}

func rateFactor(rate *big.Rat, delta uint64) *big.Int {
	if rate == nil || rate.Sign() == 0 || delta == 0 {
		return new(big.Int).Set(ray)
	}
	perBlock := new(big.Rat).Set(rate)
	perBlock.Quo(perBlock, new(big.Rat).SetUint64(blocksPerYear))
	perBlock.Mul(perBlock, new(big.Rat).SetUint64(delta))
	factor := new(big.Rat).Add(big.NewRat(1, 1), perBlock)
	return ratToRay(factor)
}

func computeInterest(totalBorrowed *big.Int, rate *big.Rat, delta uint64) *big.Int {
	if totalBorrowed == nil || totalBorrowed.Sign() == 0 || rate == nil || rate.Sign() == 0 || delta == 0 {
		return big.NewInt(0)
	}
	perBlock := new(big.Rat).Set(rate)
	perBlock.Quo(perBlock, new(big.Rat).SetUint64(blocksPerYear))
	perBlock.Mul(perBlock, new(big.Rat).SetUint64(delta))
	interest := new(big.Rat).Mul(perBlock, new(big.Rat).SetInt(totalBorrowed))
	if interest.Sign() <= 0 {
		return big.NewInt(0)
	}
	num := interest.Num()
	den := interest.Denom()
	if den.Sign() == 0 {
		return big.NewInt(0)
	}
	result := new(big.Int).Quo(new(big.Int).Add(num, halfUp(den)), den)
	return result
}

func sharesFromLiquidity(amount, index *big.Int) *big.Int {
	if amount == nil || amount.Sign() <= 0 || index == nil || index.Sign() == 0 {
		return big.NewInt(0)
	}
	scaled := new(big.Int).Mul(amount, ray)
	scaled.Add(scaled, halfUp(index))
	scaled.Quo(scaled, index)
	return scaled
}

func liquidityFromShares(shares, index *big.Int) *big.Int {
	if shares == nil || shares.Sign() <= 0 || index == nil || index.Sign() == 0 {
		return big.NewInt(0)
	}
	scaled := new(big.Int).Mul(shares, index)
	scaled.Add(scaled, halfRay)
	scaled.Quo(scaled, ray)
	return scaled
}

func scaledDebtFromAmount(amount, index *big.Int) *big.Int {
	if amount == nil || amount.Sign() <= 0 || index == nil || index.Sign() == 0 {
		return big.NewInt(0)
	}
	scaled := new(big.Int).Mul(amount, ray)
	scaled.Add(scaled, halfUp(index))
	scaled.Quo(scaled, index)
	if scaled.Sign() == 0 && amount.Sign() > 0 {
		return big.NewInt(1)
	}
	return scaled
}

func debtFromScaled(scaled, index *big.Int) *big.Int {
	if scaled == nil || scaled.Sign() == 0 || index == nil || index.Sign() == 0 {
		return big.NewInt(0)
	}
	actual := new(big.Int).Mul(scaled, index)
	actual.Add(actual, halfRay)
	actual.Quo(actual, ray)
	return actual
}

func halfUp(x *big.Int) *big.Int {
	if x == nil {
		return big.NewInt(0)
	}
	if x.Sign() <= 0 {
		return big.NewInt(0)
	}
	half := new(big.Int).Add(x, big.NewInt(1))
	half.Rsh(half, 1)
	return half
}
