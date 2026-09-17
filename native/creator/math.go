package creator

import "math/big"

var (
	oneRay       = new(big.Int).Exp(big.NewInt(10), big.NewInt(27), nil)
	minLiquidity = big.NewInt(1)
	minDeposit   = big.NewInt(1_000)
)

func calculateMintShares(deposit, totalShares, totalAssets *big.Int) (userShares, bootstrapShares *big.Int, err error) {
	if deposit == nil || deposit.Sign() <= 0 {
		return nil, nil, errInvalidAmount
	}
	if totalShares == nil || totalShares.Sign() == 0 || totalAssets == nil || totalAssets.Sign() == 0 {
		if deposit.Cmp(minDeposit) < 0 {
			return nil, nil, errDepositTooSmall
		}
		if deposit.Cmp(minLiquidity) <= 0 {
			return nil, nil, errDepositTooSmall
		}
		minted := new(big.Int).Sub(deposit, minLiquidity)
		return minted, new(big.Int).Set(minLiquidity), nil
	}
	minted := new(big.Int).Mul(deposit, totalShares)
	minted = minted.Div(minted, totalAssets)
	if minted.Sign() == 0 {
		if deposit.Cmp(minDeposit) < 0 {
			return nil, nil, errDepositTooSmall
		}
		return nil, nil, errZeroShareMint
	}
	return minted, nil, nil
}

func calculateRedeemAssets(shares, totalShares, totalAssets *big.Int) (*big.Int, error) {
	if shares == nil || shares.Sign() <= 0 {
		return nil, errInvalidAmount
	}
	if totalShares == nil || totalShares.Sign() == 0 {
		return nil, errSharesDepleted
	}
	if totalAssets == nil || totalAssets.Sign() == 0 {
		return big.NewInt(0), nil
	}
	if shares.Cmp(totalShares) > 0 {
		return nil, errInsufficientShares
	}
	assets := new(big.Int).Mul(shares, totalAssets)
	assets = assets.Div(assets, totalShares)
	if assets.Sign() == 0 {
		return nil, errRedeemTooSmall
	}
	return assets, nil
}

func computeIndex(totalAssets, totalShares *big.Int) *big.Int {
	if totalShares == nil || totalShares.Sign() == 0 {
		return new(big.Int).Set(oneRay)
	}
	if totalAssets == nil || totalAssets.Sign() == 0 {
		return big.NewInt(0)
	}
	index := new(big.Int).Mul(totalAssets, oneRay)
	index = index.Div(index, totalShares)
	return index
}
