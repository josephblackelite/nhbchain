package evidence

import (
	"encoding/json"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

func TestVerifyEquivocationProofAcceptsGenuineConflictingVotes(t *testing.T) {
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := ethcrypto.PubkeyToAddress(priv.PublicKey)
	var offender [20]byte
	copy(offender[:], addr.Bytes())

	sign := func(blockHash []byte) []byte {
		vote := equivocationVotePayload{BlockHash: blockHash, Round: 3, Type: EquivocationVotePrevote, Height: 100}
		digest := vote.digest()
		sig, err := ethcrypto.Sign(digest[:], priv)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return sig
	}

	hashA := []byte{0xAA, 0xBB}
	hashB := []byte{0xCC, 0xDD}
	sigA := sign(hashA)
	sigB := sign(hashB)

	proof := EquivocationProof{
		Height:   100,
		Round:    3,
		VoteType: EquivocationVotePrevote,
		VoteA:    EquivocationSignedVote{BlockHash: "0x" + hexEncode(hashA), Signature: "0x" + hexEncode(sigA)},
		VoteB:    EquivocationSignedVote{BlockHash: "0x" + hexEncode(hashB), Signature: "0x" + hexEncode(sigB)},
	}
	details, err := json.Marshal(&proof)
	if err != nil {
		t.Fatalf("marshal proof: %v", err)
	}

	if err := VerifyEquivocationProof(offender, details); err != nil {
		t.Fatalf("expected genuine conflicting votes to verify, got: %v", err)
	}
}

func TestVerifyEquivocationProofRejectsFabricatedAccusation(t *testing.T) {
	// The reporter signs both "votes" with their OWN key, not the
	// offender's -- exactly what a fabricated accusation looks like.
	reporterPriv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate reporter key: %v", err)
	}
	offenderPriv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate offender key: %v", err)
	}
	offenderAddr := ethcrypto.PubkeyToAddress(offenderPriv.PublicKey)
	var offender [20]byte
	copy(offender[:], offenderAddr.Bytes())

	sign := func(blockHash []byte) []byte {
		vote := equivocationVotePayload{BlockHash: blockHash, Round: 1, Type: EquivocationVotePrevote, Height: 50}
		digest := vote.digest()
		sig, err := ethcrypto.Sign(digest[:], reporterPriv)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return sig
	}

	hashA := []byte{0x01}
	hashB := []byte{0x02}
	proof := EquivocationProof{
		Height:   50,
		Round:    1,
		VoteType: EquivocationVotePrevote,
		VoteA:    EquivocationSignedVote{BlockHash: "0x" + hexEncode(hashA), Signature: "0x" + hexEncode(sign(hashA))},
		VoteB:    EquivocationSignedVote{BlockHash: "0x" + hexEncode(hashB), Signature: "0x" + hexEncode(sign(hashB))},
	}
	details, err := json.Marshal(&proof)
	if err != nil {
		t.Fatalf("marshal proof: %v", err)
	}

	if err := VerifyEquivocationProof(offender, details); err == nil {
		t.Fatalf("expected fabricated (reporter-signed) accusation to be rejected")
	}
}

func TestVerifyEquivocationProofRejectsNonConflictingVotes(t *testing.T) {
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	addr := ethcrypto.PubkeyToAddress(priv.PublicKey)
	var offender [20]byte
	copy(offender[:], addr.Bytes())

	hash := []byte{0x11, 0x22}
	vote := equivocationVotePayload{BlockHash: hash, Round: 7, Type: EquivocationVotePrecommit, Height: 200}
	digest := vote.digest()
	sig, err := ethcrypto.Sign(digest[:], priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	proof := EquivocationProof{
		Height:   200,
		Round:    7,
		VoteType: EquivocationVotePrecommit,
		VoteA:    EquivocationSignedVote{BlockHash: "0x" + hexEncode(hash), Signature: "0x" + hexEncode(sig)},
		VoteB:    EquivocationSignedVote{BlockHash: "0x" + hexEncode(hash), Signature: "0x" + hexEncode(sig)},
	}
	details, err := json.Marshal(&proof)
	if err != nil {
		t.Fatalf("marshal proof: %v", err)
	}

	if err := VerifyEquivocationProof(offender, details); err == nil {
		t.Fatalf("expected identical (non-conflicting) votes to be rejected")
	}
}

func TestValidateEvidenceRequiresEquivocationProof(t *testing.T) {
	reporterPriv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate reporter key: %v", err)
	}
	reporterAddr := ethcrypto.PubkeyToAddress(reporterPriv.PublicKey)
	var reporter [20]byte
	copy(reporter[:], reporterAddr.Bytes())

	offenderPriv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate offender key: %v", err)
	}
	offenderAddr := ethcrypto.PubkeyToAddress(offenderPriv.PublicKey)
	var offender [20]byte
	copy(offender[:], offenderAddr.Bytes())

	e := &Evidence{
		Type:      TypeEquivocation,
		Offender:  offender,
		Heights:   []uint64{10},
		Details:   []byte(`{}`),
		Reporter:  reporter,
		Timestamp: 1,
	}
	hash, err := e.CanonicalHash()
	if err != nil {
		t.Fatalf("canonical hash: %v", err)
	}
	digest := e.SigningDigest(hash)
	sig, err := ethcrypto.Sign(digest, reporterPriv)
	if err != nil {
		t.Fatalf("sign evidence: %v", err)
	}
	e.ReporterSig = sig

	verr := ValidateEvidence(e, hash, 10, 0, nil)
	if verr == nil {
		t.Fatalf("expected evidence without a valid equivocation proof to be rejected")
	}
	if verr.Reason != RejectReasonInvalidEquivocationProof {
		t.Fatalf("expected reason %q, got %q (%s)", RejectReasonInvalidEquivocationProof, verr.Reason, verr.Message)
	}
}

func hexEncode(b []byte) string {
	const hextable = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hextable[c>>4]
		out[i*2+1] = hextable[c&0x0f]
	}
	return string(out)
}
