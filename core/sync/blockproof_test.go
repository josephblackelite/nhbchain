package sync

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"math"
	"strings"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

type proofValidator struct {
	key  *ecdsa.PrivateKey
	addr []byte
}

func newProofValidators(t *testing.T, n int) []proofValidator {
	t.Helper()
	out := make([]proofValidator, n)
	for i := range out {
		key, err := ethcrypto.GenerateKey()
		if err != nil {
			t.Fatalf("generate key: %v", err)
		}
		out[i] = proofValidator{key: key, addr: ethcrypto.PubkeyToAddress(key.PublicKey).Bytes()}
	}
	return out
}

func signDigest(t *testing.T, digest []byte, signers ...proofValidator) []BlockSignature {
	t.Helper()
	sigs := make([]BlockSignature, 0, len(signers))
	for _, s := range signers {
		sig, err := ethcrypto.Sign(digest, s.key)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		sigs = append(sigs, BlockSignature{Address: s.addr, Signature: sig})
	}
	return sigs
}

// TestVerifyQuorumRejectsExactlyTwoThirds is the DC-01 regression for the
// fast-sync quorum check: it used to accept signed*3 >= total*2, i.e. exactly
// 2/3 of the power whenever the total is a multiple of 3.
func TestVerifyQuorumRejectsExactlyTwoThirds(t *testing.T) {
	digestArr := sha256.Sum256([]byte("manifest-digest"))
	digest := digestArr[:]

	tests := []struct {
		name    string
		powers  []uint64
		signers []int
		wantOK  bool
	}{
		{"two of three equal validators", []uint64{1, 1, 1}, []int{0, 1}, false},
		{"three of three equal validators", []uint64{1, 1, 1}, []int{0, 1, 2}, true},
		{"four of six equal validators", []uint64{1, 1, 1, 1, 1, 1}, []int{0, 1, 2, 3}, false},
		{"five of six equal validators", []uint64{1, 1, 1, 1, 1, 1}, []int{0, 1, 2, 3, 4}, true},
		{"weighted set exactly two thirds", []uint64{4, 1, 1}, []int{0}, false},
		{"weighted set above two thirds", []uint64{4, 1, 1}, []int{0, 1}, true},
		{"three of four keeps the old bar", []uint64{1, 1, 1, 1}, []int{0, 1, 2}, true},
		{"two of four is below it", []uint64{1, 1, 1, 1}, []int{0, 1}, false},
		{"single validator", []uint64{10}, []int{0}, true},
		{"one of two", []uint64{1, 1}, []int{0}, false},
		{"both of two", []uint64{1, 1}, []int{0, 1}, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			vals := newProofValidators(t, len(tt.powers))
			list := make([]Validator, len(vals))
			for i, v := range vals {
				list[i] = Validator{Address: v.addr, Power: tt.powers[i]}
			}
			set := NewValidatorSet(list)
			signers := make([]proofValidator, 0, len(tt.signers))
			for _, idx := range tt.signers {
				signers = append(signers, vals[idx])
			}
			err := set.VerifyQuorum(digest, signDigest(t, digest, signers...))
			if tt.wantOK && err != nil {
				t.Fatalf("expected quorum to verify, got %v", err)
			}
			if !tt.wantOK {
				if err == nil {
					t.Fatalf("SECURITY: exactly 2/3 of the voting power must not be accepted as a quorum")
				}
				if !strings.Contains(err.Error(), "insufficient voting power") {
					t.Fatalf("expected an insufficient-power error, got %v", err)
				}
			}
		})
	}
}

// TestVerifyQuorumHandlesStakeScalePowers covers powers that saturate uint64
// (core/node.go's validatorPower clamps a stake-sized power to MaxUint64): the
// running uint64 total wraps around, which made a fully signed two-validator
// set fail the old check. The comparison is now done on big integers.
func TestVerifyQuorumHandlesStakeScalePowers(t *testing.T) {
	digestArr := sha256.Sum256([]byte("stake-scale-manifest-digest"))
	digest := digestArr[:]
	vals := newProofValidators(t, 2)
	set := NewValidatorSet([]Validator{
		{Address: vals[0].addr, Power: math.MaxUint64},
		{Address: vals[1].addr, Power: math.MaxUint64},
	})

	if err := set.VerifyQuorum(digest, signDigest(t, digest, vals[0], vals[1])); err != nil {
		t.Fatalf("expected both saturated-power validators to form a quorum, got %v", err)
	}
	if err := set.VerifyQuorum(digest, signDigest(t, digest, vals[0])); err == nil {
		t.Fatalf("SECURITY: one of two equal validators must not form a quorum")
	}
}

func TestVerifyQuorumRejectsBadInputs(t *testing.T) {
	digestArr := sha256.Sum256([]byte("bad-input-digest"))
	digest := digestArr[:]
	vals := newProofValidators(t, 3)
	set := NewValidatorSet([]Validator{
		{Address: vals[0].addr, Power: 1},
		{Address: vals[1].addr, Power: 1},
		{Address: vals[2].addr, Power: 1},
	})

	var nilSet *ValidatorSet
	if err := nilSet.VerifyQuorum(digest, signDigest(t, digest, vals[0])); err == nil {
		t.Fatalf("expected a nil validator set to be rejected")
	}
	if err := set.VerifyQuorum(digest, nil); err == nil {
		t.Fatalf("expected an empty signature list to be rejected")
	}
	// The same validator listed three times is one validator's power.
	dup := signDigest(t, digest, vals[0], vals[0], vals[0])
	if err := set.VerifyQuorum(digest, dup); err == nil {
		t.Fatalf("SECURITY: duplicated signatures from one validator must not add up to a quorum")
	}
	if err := NewValidatorSet(nil).VerifyQuorum(digest, signDigest(t, digest, vals[0])); err == nil {
		t.Fatalf("expected an empty validator set to be rejected")
	}
}
