package types

import (
	"crypto/ecdsa"
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

func TestQuorumCertVerifyAcceptsGenuineTwoThirdsQuorum(t *testing.T) {
	keyA, addrA := genValidatorKey(t)
	keyB, addrB := genValidatorKey(t)
	_, addrC := genValidatorKey(t)

	headerHash := []byte("block-123-header-hash")
	power := map[string]*big.Int{
		string(addrA): big.NewInt(1),
		string(addrB): big.NewInt(1),
		string(addrC): big.NewInt(1),
	}

	// Exactly 2 of 3 equal-power validators = 2/3, at the threshold.
	qc := buildSignedQuorumCert(t, headerHash, 10, 0, []*ecdsa.PrivateKey{keyA, keyB})
	if err := qc.Verify(headerHash, power); err != nil {
		t.Fatalf("expected genuine 2/3 quorum to verify, got %v", err)
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
