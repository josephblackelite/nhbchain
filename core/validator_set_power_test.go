package core

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"math/big"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	syncmgr "nhbchain/core/sync"
)

// PL-R5T-44: the validator set handed to fast sync carries each validator's full
// stake-scale power. The old conversion clamped anything above 63 bits to
// MaxUint64, so every validator with a real stake counted the same: with two
// large holders (49 units each) and five small ones (1 unit each), the five
// small holders, under 5% of the stake, formed a quorum of seven equal votes,
// and the two large holders together (95%) did not.
//
// The test goes through buildValidatorSet, with real addresses and real
// signatures, so it holds for any representation of the power.
func TestBuildValidatorSetQuorumIsStakeWeighted(t *testing.T) {
	unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil) // 10^24 per unit, far above 2^64
	units := []int64{49, 49, 1, 1, 1, 1, 1}

	source := map[string]*big.Int{}
	var addrs [][]byte
	privs := make([]*ecdsa.PrivateKey, 0, len(units))
	for _, n := range units {
		priv, err := ethcrypto.GenerateKey()
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		addr := ethcrypto.PubkeyToAddress(priv.PublicKey).Bytes()
		source[string(addr)] = new(big.Int).Mul(big.NewInt(n), unit)
		addrs = append(addrs, addr)
		privs = append(privs, priv)
	}
	set := buildValidatorSet(source)

	sum := sha256.Sum256([]byte("fast-sync stake weighted quorum"))
	digest := sum[:]
	sign := func(indexes ...int) []syncmgr.BlockSignature {
		out := make([]syncmgr.BlockSignature, 0, len(indexes))
		for _, i := range indexes {
			sig, err := ethcrypto.Sign(digest, privs[i])
			if err != nil {
				t.Fatalf("sign: %v", err)
			}
			out = append(out, syncmgr.BlockSignature{Address: addrs[i], Signature: sig})
		}
		return out
	}

	if err := set.VerifyQuorum(digest, sign(2, 3, 4, 5, 6)); err == nil {
		t.Fatalf("SECURITY: five small holders with 5 of 103 units of stake formed a quorum")
	}
	if err := set.VerifyQuorum(digest, sign(0, 1)); err != nil {
		t.Fatalf("the two large holders (98 of 103 units) must form a quorum, got %v", err)
	}
	if err := set.VerifyQuorum(digest, sign(0)); err == nil {
		t.Fatalf("SECURITY: one large holder (49 of 103 units) must not form a quorum")
	}
	if err := set.VerifyQuorum(digest, sign(0, 2, 3, 4)); err == nil {
		// 49 + 3 = 52 of 103 is not above two thirds
		t.Fatalf("SECURITY: 52 of 103 units must not form a quorum")
	}
}
