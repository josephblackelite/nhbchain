package evidence

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

// NHB-AUDIT-C10 (second half): ValidateEvidence used to verify only the
// REPORTER's signature over the accusation -- proof that reporter X
// claims offender Y misbehaved, never anything tied to Y's own key.
// Anyone could self-sign a fabricated accusation against any address and
// have it pass validation; now that the weight ledger's zero-base bug is
// also fixed (see EnsureBaseline), a fabricated equivocation report would
// actually cause a real slash, not a no-op. This file adds the missing
// half for EQUIVOCATION specifically -- the one evidence type with a
// concrete, self-contained cryptographic proof shape: two signed votes,
// each independently verified to have been produced by the OFFENDER's
// own key, for the same height/round, but for different block hashes.
// Both signatures being valid for the SAME key is only possible if that
// key's holder genuinely signed two conflicting things.
//
// DOWNTIME and INVALID_BLOCK_PROPOSAL evidence have no equivalent
// self-contained proof shape (downtime is an absence of activity, which
// cannot itself be signed) and are deliberately left as a separate,
// later concern -- not attempted here.

// EquivocationVoteType mirrors consensus/bft.VoteType's wire values
// WITHOUT importing that package (see core/types/vote.go's
// PrecommitVote for why this codebase already uses independent,
// parity-tested mirrors instead of a cross-package dependency here --
// consensus/potso/evidence_equivocation_parity_test.go proves this
// mirror produces byte-identical signed payloads to the real thing).
type EquivocationVoteType byte

const (
	EquivocationVotePrevote   EquivocationVoteType = 0x01
	EquivocationVotePrecommit EquivocationVoteType = 0x02
)

// equivocationVotePayload mirrors consensus/bft.Vote's exact field
// names, JSON tags, and types -- this is the payload whose signature a
// real BFT consensus vote actually covers (sha256 of this struct's JSON
// encoding). A genuine SignedVote produced during a live consensus round
// can be submitted here unmodified as proof.
type equivocationVotePayload struct {
	BlockHash []byte                `json:"blockHash"`
	Round     int                   `json:"round"`
	Type      EquivocationVoteType  `json:"type"`
	Height    uint64                `json:"height"`
}

func (v *equivocationVotePayload) digest() [32]byte {
	b, _ := json.Marshal(v)
	return sha256.Sum256(b)
}

// EquivocationSignedVote is one half of an equivocation proof: a single
// signed vote, claimed to have been produced by the offender.
type EquivocationSignedVote struct {
	BlockHash string `json:"blockHash"` // hex, empty for a nil/prevote-nil vote
	Signature string `json:"signature"` // hex, 65-byte secp256k1
}

// EquivocationProof is the required Evidence.Details payload for
// TypeEquivocation. Height/Round/VoteType describe the single consensus
// decision point both votes claim to be for; VoteA and VoteB must
// recover to the offender's address and disagree on BlockHash.
type EquivocationProof struct {
	Height   uint64                  `json:"height"`
	Round    int                     `json:"round"`
	VoteType EquivocationVoteType    `json:"voteType"`
	VoteA    EquivocationSignedVote  `json:"voteA"`
	VoteB    EquivocationSignedVote  `json:"voteB"`
}

func decodeHexBlockHash(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	return hex.DecodeString(cleaned)
}

func decodeHexSignature(value string) ([]byte, error) {
	cleaned := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(value), "0x"), "0X")
	decoded, err := hex.DecodeString(cleaned)
	if err != nil {
		return nil, err
	}
	if len(decoded) != 65 {
		return nil, fmt.Errorf("signature must be 65 bytes, got %d", len(decoded))
	}
	return decoded, nil
}

// verifyEquivocationSignature recovers the signer of vote and confirms it
// equals offender, for the given (height, round, voteType) claim.
func verifyEquivocationSignature(vote EquivocationSignedVote, offender [20]byte, height uint64, round int, voteType EquivocationVoteType) ([]byte, error) {
	blockHash, err := decodeHexBlockHash(vote.BlockHash)
	if err != nil {
		return nil, fmt.Errorf("invalid block hash: %w", err)
	}
	sig, err := decodeHexSignature(vote.Signature)
	if err != nil {
		return nil, fmt.Errorf("invalid signature: %w", err)
	}
	payload := &equivocationVotePayload{BlockHash: blockHash, Round: round, Type: voteType, Height: height}
	digest := payload.digest()
	pubKey, err := ethcrypto.SigToPub(digest[:], sig)
	if err != nil {
		return nil, fmt.Errorf("recover signer: %w", err)
	}
	recovered := ethcrypto.PubkeyToAddress(*pubKey)
	if !bytes.Equal(recovered.Bytes(), offender[:]) {
		return nil, fmt.Errorf("signature does not match offender %s (recovered %s)", hex.EncodeToString(offender[:]), recovered.Hex())
	}
	return blockHash, nil
}

// VerifyEquivocationProof decodes and cryptographically verifies an
// EQUIVOCATION evidence submission's Details payload. Returns a
// descriptive error (never a panic) for any malformed or unproven claim.
func VerifyEquivocationProof(offender [20]byte, details []byte) error {
	if len(details) == 0 {
		return fmt.Errorf("equivocation proof required")
	}
	var proof EquivocationProof
	if err := json.Unmarshal(details, &proof); err != nil {
		return fmt.Errorf("invalid equivocation proof: %w", err)
	}
	if proof.VoteType != EquivocationVotePrevote && proof.VoteType != EquivocationVotePrecommit {
		return fmt.Errorf("invalid equivocation vote type %d", proof.VoteType)
	}
	hashA, err := verifyEquivocationSignature(proof.VoteA, offender, proof.Height, proof.Round, proof.VoteType)
	if err != nil {
		return fmt.Errorf("vote A: %w", err)
	}
	hashB, err := verifyEquivocationSignature(proof.VoteB, offender, proof.Height, proof.Round, proof.VoteType)
	if err != nil {
		return fmt.Errorf("vote B: %w", err)
	}
	if bytes.Equal(hashA, hashB) {
		return fmt.Errorf("votes A and B are identical -- not a genuine conflict")
	}
	return nil
}
