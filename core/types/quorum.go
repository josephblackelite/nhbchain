package types

import "math/big"

// QuorumThreshold returns the smallest voting power that counts as a BFT
// quorum out of totalPower: strictly more than two thirds of it, which is
// floor(2*totalPower/3) + 1. totalPower must be positive.
//
// The bar has to be strictly above 2/3. Two quorums that each hold exactly
// 2/3 of the power can overlap in only 1/3 of it, so one equivocating
// validator (or a third of the power) could get two different blocks
// committed at the same height; with strictly more than 2/3 any two quorums
// share more than 1/3 of the power, which always includes an honest
// validator while less than a third is faulty.
//
// For every total that is not a multiple of 3 this equals ceil(2*total/3),
// the value the engine used before this helper existed, so decisions on such
// totals are unchanged. For a multiple of 3 it is one unit stricter: three
// equal validators now all have to sign.
//
// This is the only place the threshold is computed. consensus/bft (live
// rounds and polka proofs), QuorumCert.Verify and core/sync's manifest and
// header proofs all go through it so they cannot drift apart.
func QuorumThreshold(totalPower *big.Int) *big.Int {
	threshold := new(big.Int).Mul(totalPower, big.NewInt(2))
	threshold.Div(threshold, big.NewInt(3))
	return threshold.Add(threshold, big.NewInt(1))
}

// HasQuorum reports whether signedPower is a BFT quorum out of totalPower
// (see QuorumThreshold). A nil or non-positive total never has a quorum, so
// an empty validator set cannot commit anything.
func HasQuorum(signedPower, totalPower *big.Int) bool {
	if signedPower == nil || totalPower == nil || totalPower.Sign() <= 0 {
		return false
	}
	return signedPower.Cmp(QuorumThreshold(totalPower)) >= 0
}
