package types

import (
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

func genValidatorKey(t *testing.T) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := ethcrypto.PubkeyToAddress(priv.PublicKey)
	return priv, addr.Bytes()
}

// buildSignedQuorumCert signs the given header hash on behalf of every
// (key, addr) pair in signers, mirroring exactly what
// consensus/bft.Engine.buildQuorumCertLocked does with real precommit
// votes: one PrecommitVote.Sign call per validator over the identical
// (height, round, blockHash, Precommit) payload.
func buildSignedQuorumCert(t *testing.T, headerHash []byte, height uint64, round int, signers []*ecdsa.PrivateKey) *QuorumCert {
	t.Helper()
	vote := &PrecommitVote{BlockHash: headerHash, Round: round, Type: VoteTypePrecommit, Height: height}
	qc := &QuorumCert{Height: height, Round: round, BlockHash: append([]byte(nil), headerHash...)}
	for _, priv := range signers {
		sig, err := vote.Sign(priv)
		if err != nil {
			t.Fatalf("sign vote: %v", err)
		}
		qc.Signatures = append(qc.Signatures, *sig)
	}
	return qc
}

func TestQuorumCertVerifyAcceptsGenuineSuperMajorityQuorum(t *testing.T) {
	keyA, addrA := genValidatorKey(t)
	keyB, addrB := genValidatorKey(t)
	keyC, addrC := genValidatorKey(t)
	_, addrD := genValidatorKey(t)

	headerHash := []byte("block-123-header-hash")
	power := map[string]*big.Int{
		string(addrA): big.NewInt(1),
		string(addrB): big.NewInt(1),
		string(addrC): big.NewInt(1),
		string(addrD): big.NewInt(1),
	}

	// 3 of 4 equal-power validators = 75%, strictly more than 2/3.
	qc := buildSignedQuorumCert(t, headerHash, 10, 0, []*ecdsa.PrivateKey{keyA, keyB, keyC})
	if err := qc.Verify(headerHash, power); err != nil {
		t.Fatalf("expected genuine >2/3 quorum to verify, got %v", err)
	}
}

// TestQuorumCertVerifyRejectsExactlyTwoThirds is the DC-01 regression: the
// threshold used to be ceil(2*total/3), so on any validator set whose total
// power is a multiple of 3 exactly 2/3 of the power was accepted. Two such
// quorums can overlap in a single (faulty) validator.
func TestQuorumCertVerifyRejectsExactlyTwoThirds(t *testing.T) {
	keys := make([]*ecdsa.PrivateKey, 6)
	addrs := make([][]byte, 6)
	for i := range keys {
		keys[i], addrs[i] = genValidatorKey(t)
	}

	tests := []struct {
		name    string
		powers  []int64
		signers []int
		wantOK  bool
	}{
		{name: "two of three equal validators", powers: []int64{1, 1, 1}, signers: []int{0, 1}, wantOK: false},
		{name: "three of three equal validators", powers: []int64{1, 1, 1}, signers: []int{0, 1, 2}, wantOK: true},
		{name: "four of six equal validators", powers: []int64{1, 1, 1, 1, 1, 1}, signers: []int{0, 1, 2, 3}, wantOK: false},
		{name: "five of six equal validators", powers: []int64{1, 1, 1, 1, 1, 1}, signers: []int{0, 1, 2, 3, 4}, wantOK: true},
		{name: "weighted set exactly two thirds", powers: []int64{4, 1, 1}, signers: []int{0}, wantOK: false},
		{name: "weighted set just above two thirds", powers: []int64{4, 1, 1}, signers: []int{0, 1}, wantOK: true},
		{name: "total not a multiple of three keeps the old bar", powers: []int64{1, 1, 1, 1}, signers: []int{0, 1, 2}, wantOK: true},
		{name: "total not a multiple of three, one short", powers: []int64{1, 1, 1, 1}, signers: []int{0, 1}, wantOK: false},
	}
	for i, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			power := make(map[string]*big.Int, len(tt.powers))
			for j, p := range tt.powers {
				power[string(addrs[j])] = big.NewInt(p)
			}
			signers := make([]*ecdsa.PrivateKey, 0, len(tt.signers))
			for _, idx := range tt.signers {
				signers = append(signers, keys[idx])
			}
			headerHash := []byte(fmt.Sprintf("block-two-thirds-%d", i))
			qc := buildSignedQuorumCert(t, headerHash, uint64(100+i), 0, signers)
			err := qc.Verify(headerHash, power)
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

func TestQuorumCertVerifyRejectsInsufficientPower(t *testing.T) {
	keyA, addrA := genValidatorKey(t)
	_, addrB := genValidatorKey(t)
	_, addrC := genValidatorKey(t)

	headerHash := []byte("block-124-header-hash")
	power := map[string]*big.Int{
		string(addrA): big.NewInt(1),
		string(addrB): big.NewInt(1),
		string(addrC): big.NewInt(1),
	}

	// Only 1 of 3 -- below 2/3.
	qc := buildSignedQuorumCert(t, headerHash, 11, 0, []*ecdsa.PrivateKey{keyA})
	if err := qc.Verify(headerHash, power); err == nil {
		t.Fatalf("SECURITY: expected insufficient-power rejection, got nil error")
	}
}

func TestQuorumCertVerifyRejectsSignatureFromNonValidator(t *testing.T) {
	keyA, addrA := genValidatorKey(t)
	attackerKey, _ := genValidatorKey(t) // not in the validator set at all

	headerHash := []byte("block-125-header-hash")
	power := map[string]*big.Int{
		string(addrA): big.NewInt(3),
	}

	qc := buildSignedQuorumCert(t, headerHash, 12, 0, []*ecdsa.PrivateKey{keyA, attackerKey})
	if err := qc.Verify(headerHash, power); err == nil {
		t.Fatalf("SECURITY: expected rejection of a signature from a non-validator, got nil error")
	} else if !strings.Contains(err.Error(), "non-validator") {
		t.Fatalf("expected a non-validator error, got: %v", err)
	}
}

func TestQuorumCertVerifyRejectsMismatchedValidatorSignaturePairing(t *testing.T) {
	_, addrA := genValidatorKey(t)
	keyB, addrB := genValidatorKey(t)

	headerHash := []byte("block-126-header-hash")
	power := map[string]*big.Int{
		string(addrA): big.NewInt(1),
		string(addrB): big.NewInt(1),
	}

	// Sign genuinely with keyB, but claim it's addrA's signature -- the
	// exact shape of a forgery attempt: attacker relabels a real
	// signature's claimed signer.
	vote := &PrecommitVote{BlockHash: headerHash, Round: 0, Type: VoteTypePrecommit, Height: 13}
	realSig, err := vote.Sign(keyB)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	forged := QuorumSignature{Validator: append([]byte(nil), addrA...), Signature: realSig.Signature}
	qc := &QuorumCert{Height: 13, Round: 0, BlockHash: headerHash, Signatures: []QuorumSignature{forged}}

	if err := qc.Verify(headerHash, power); err == nil {
		t.Fatalf("SECURITY: expected rejection of a signature relabeled as a different validator, got nil error")
	} else if !strings.Contains(err.Error(), "does not match claimed validator") {
		t.Fatalf("expected a claimed-validator mismatch error, got: %v", err)
	}
}

func TestQuorumCertVerifyRejectsBlockHashMismatch(t *testing.T) {
	keyA, addrA := genValidatorKey(t)
	keyB, addrB := genValidatorKey(t)

	signedHash := []byte("the-real-block-header-hash")
	tamperedHash := []byte("a-different-block-header-hash!!")
	power := map[string]*big.Int{
		string(addrA): big.NewInt(1),
		string(addrB): big.NewInt(1),
	}

	// A genuinely valid QC for one block must not verify against a
	// DIFFERENT block's header hash -- e.g. an attacker who tampers
	// Header.Validator (or anything else) on an otherwise-real, previously
	// quorum-certified block and tries to replay its old QC.
	qc := buildSignedQuorumCert(t, signedHash, 14, 0, []*ecdsa.PrivateKey{keyA, keyB})
	if err := qc.Verify(tamperedHash, power); err == nil {
		t.Fatalf("SECURITY: expected rejection when the QC's block hash doesn't match the block actually being verified")
	}
}

func TestQuorumCertVerifyDoesNotDoubleCountDuplicateSigner(t *testing.T) {
	keyA, addrA := genValidatorKey(t)
	_, addrB := genValidatorKey(t)
	_, addrC := genValidatorKey(t)

	headerHash := []byte("block-127-header-hash")
	power := map[string]*big.Int{
		string(addrA): big.NewInt(1),
		string(addrB): big.NewInt(1),
		string(addrC): big.NewInt(1),
	}

	// The same validator's signature listed twice must not count as 2/3 of
	// the power on its own -- only 1 of 3 distinct validators actually
	// signed.
	qc := buildSignedQuorumCert(t, headerHash, 15, 0, []*ecdsa.PrivateKey{keyA, keyA})
	if err := qc.Verify(headerHash, power); err == nil {
		t.Fatalf("SECURITY: a single validator's signature duplicated in the list must not satisfy quorum")
	}
}

func TestQuorumCertVerifyRejectsNilOrEmpty(t *testing.T) {
	_, addrA := genValidatorKey(t)
	power := map[string]*big.Int{string(addrA): big.NewInt(1)}
	headerHash := []byte("block-128-header-hash")

	var nilQC *QuorumCert
	if err := nilQC.Verify(headerHash, power); err == nil {
		t.Fatalf("expected nil QuorumCert to be rejected")
	}

	empty := &QuorumCert{Height: 16, Round: 0, BlockHash: headerHash}
	if err := empty.Verify(headerHash, power); err == nil {
		t.Fatalf("expected a QuorumCert with zero signatures to be rejected")
	}
}
