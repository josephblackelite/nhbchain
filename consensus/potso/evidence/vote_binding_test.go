package evidence

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/json"
	"testing"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/types"
)

// PL-R5T-31: two honest votes by one key on two networks, at the same height, round and
// type, are a valid equivocation proof under the legacy digest and forfeit the validator's
// stake. From the vote binding height each vote must be signed over the genesis hash of the
// network that judges the proof.

var (
	netGenesisA = bytes.Repeat([]byte{0xA1}, 32)
	netGenesisB = bytes.Repeat([]byte{0xB2}, 32)
)

type proofFixture struct {
	priv    *ecdsa.PrivateKey
	offence [20]byte
}

func newProofFixture(t *testing.T) *proofFixture {
	t.Helper()
	priv, err := ethcrypto.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	f := &proofFixture{priv: priv}
	copy(f.offence[:], ethcrypto.PubkeyToAddress(priv.PublicKey).Bytes())
	return f
}

// vote signs a prevote at (height, round) for blockHash over the digest the binding names;
// a nil genesis signs the legacy digest whatever the height.
func (f *proofFixture) vote(t *testing.T, blockHash []byte, height uint64, round int, genesis []byte) EquivocationSignedVote {
	t.Helper()
	payload := equivocationVotePayload{BlockHash: blockHash, Round: round, Type: EquivocationVotePrevote, Height: height}
	encoded, err := json.Marshal(&payload)
	if err != nil {
		t.Fatal(err)
	}
	var digest [32]byte
	if genesis == nil {
		digest = payload.digest()
	} else {
		digest = types.BoundVoteDigest(genesis, encoded)
	}
	sig, err := ethcrypto.Sign(digest[:], f.priv)
	if err != nil {
		t.Fatal(err)
	}
	return EquivocationSignedVote{BlockHash: "0x" + hexEncode(blockHash), Signature: "0x" + hexEncode(sig)}
}

func (f *proofFixture) proof(t *testing.T, height uint64, a, b EquivocationSignedVote) []byte {
	t.Helper()
	details, err := json.Marshal(&EquivocationProof{Height: height, Round: 3, VoteType: EquivocationVotePrevote, VoteA: a, VoteB: b})
	if err != nil {
		t.Fatal(err)
	}
	return details
}

func TestVoteBinding_TwoNetworksVotesAreNotAnEquivocation(t *testing.T) {
	f := newProofFixture(t)
	const height = 150
	on := types.VoteBinding{Genesis: netGenesisA, FromHeight: 100}

	// The vulnerability: honest votes on two networks, each signed the legacy way.
	hashA, hashB := []byte{0x01}, []byte{0x02}
	crossLegacy := f.proof(t, height, f.vote(t, hashA, height, 3, nil), f.vote(t, hashB, height, 3, nil))
	if err := VerifyEquivocationProof(f.offence, crossLegacy); err != nil {
		t.Fatalf("the legacy rule is expected to take two legacy votes as a proof (this is what the binding closes): %v", err)
	}
	if err := VerifyEquivocationProofBound(f.offence, crossLegacy, on); err == nil {
		t.Fatalf("two legacy votes at the binding height were taken as a proof")
	}

	// Honest bound votes: one signed for network A, one for network B.
	cross := f.proof(t, height, f.vote(t, hashA, height, 3, netGenesisA), f.vote(t, hashB, height, 3, netGenesisB))
	if err := VerifyEquivocationProofBound(f.offence, cross, on); err == nil {
		t.Fatalf("a vote of network A and a vote of network B were taken as an equivocation on network A")
	}
	onB := types.VoteBinding{Genesis: netGenesisB, FromHeight: 100}
	if err := VerifyEquivocationProofBound(f.offence, cross, onB); err == nil {
		t.Fatalf("a vote of network A and a vote of network B were taken as an equivocation on network B")
	}
}

func TestVoteBinding_AGenuineDoubleSignOnOneNetworkIsStillAProof(t *testing.T) {
	f := newProofFixture(t)
	const height = 150
	on := types.VoteBinding{Genesis: netGenesisA, FromHeight: 100}
	proof := f.proof(t, height, f.vote(t, []byte{0x01}, height, 3, netGenesisA), f.vote(t, []byte{0x02}, height, 3, netGenesisA))
	if err := VerifyEquivocationProofBound(f.offence, proof, on); err != nil {
		t.Fatalf("two conflicting votes signed over the network's genesis hash must be a proof: %v", err)
	}
	// Not for another network.
	if err := VerifyEquivocationProofBound(f.offence, proof, types.VoteBinding{Genesis: netGenesisB, FromHeight: 100}); err == nil {
		t.Fatalf("a proof of network A was accepted by network B")
	}
}

func TestVoteBinding_AProofAboutAnEarlierHeightKeepsTheLegacyRule(t *testing.T) {
	f := newProofFixture(t)
	const height = 99
	on := types.VoteBinding{Genesis: netGenesisA, FromHeight: 100}
	legacy := f.proof(t, height, f.vote(t, []byte{0x01}, height, 3, nil), f.vote(t, []byte{0x02}, height, 3, nil))
	if err := VerifyEquivocationProofBound(f.offence, legacy, on); err != nil {
		t.Fatalf("a legacy proof about a height below the binding height must still verify: %v", err)
	}
	bound := f.proof(t, height, f.vote(t, []byte{0x01}, height, 3, netGenesisA), f.vote(t, []byte{0x02}, height, 3, netGenesisA))
	if err := VerifyEquivocationProofBound(f.offence, bound, on); err == nil {
		t.Fatalf("a proof of bound votes about a height below the binding height was accepted")
	}
}

func TestVoteBinding_WithTheBindingOffTheLegacyEntryPointsAreUnchanged(t *testing.T) {
	f := newProofFixture(t)
	const height = 150
	legacy := f.proof(t, height, f.vote(t, []byte{0x01}, height, 3, nil), f.vote(t, []byte{0x02}, height, 3, nil))
	if err := VerifyEquivocationProof(f.offence, legacy); err != nil {
		t.Fatal(err)
	}
	// A binding with a genesis but no height is off.
	if err := VerifyEquivocationProofBound(f.offence, legacy, types.VoteBinding{Genesis: netGenesisA}); err != nil {
		t.Fatalf("a binding with no height must keep the legacy rule: %v", err)
	}
	bound := f.proof(t, height, f.vote(t, []byte{0x01}, height, 3, netGenesisA), f.vote(t, []byte{0x02}, height, 3, netGenesisA))
	if err := VerifyEquivocationProof(f.offence, bound); err == nil {
		t.Fatalf("with the binding off a proof of bound votes must not verify")
	}
}

func TestVoteBinding_ActiveWithoutAGenesisHashRefusesTheProof(t *testing.T) {
	f := newProofFixture(t)
	const height = 150
	proof := f.proof(t, height, f.vote(t, []byte{0x01}, height, 3, nil), f.vote(t, []byte{0x02}, height, 3, nil))
	if err := VerifyEquivocationProofBound(f.offence, proof, types.VoteBinding{FromHeight: 100}); err == nil {
		t.Fatalf("a proof was accepted with the binding active and no genesis hash")
	}
}

func TestVoteBinding_ValidateEvidenceHoldsAReportToTheBinding(t *testing.T) {
	// The report envelope around the proof (reporter signature and the rest) is exercised by the
	// core tests; here the same proof is run through the shared entry point.
	f := newProofFixture(t)
	const height = 150
	on := types.VoteBinding{Genesis: netGenesisA, FromHeight: 100}
	legacy := f.proof(t, height, f.vote(t, []byte{0x01}, height, 3, nil), f.vote(t, []byte{0x02}, height, 3, nil))
	var proof EquivocationProof
	if err := json.Unmarshal(legacy, &proof); err != nil {
		t.Fatal(err)
	}
	if err := verifyEquivocationVotes(&proof, f.offence, on); err == nil {
		t.Fatalf("verifyEquivocationVotes accepted a legacy pair at the binding height")
	}
	if err := verifyEquivocationVotes(&proof, f.offence, types.VoteBinding{}); err != nil {
		t.Fatalf("verifyEquivocationVotes must accept a legacy pair with the binding off: %v", err)
	}
}
