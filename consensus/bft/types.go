package bft

import (
	"encoding/json"

	"nhbchain/core/types"
)

// VoteType defines the type of a BFT vote.
type VoteType byte

const (
	Prevote   VoteType = 0x01
	Precommit VoteType = 0x02
)

// Vote represents a vote message sent by a validator.
// SignatureScheme enumerates supported signature algorithms for consensus
// messages.
type SignatureScheme string

const (
	SignatureSchemeSecp256k1 SignatureScheme = "secp256k1"
	SignatureSchemeEd25519   SignatureScheme = "ed25519"
)

// Signature encapsulates a validator's signature together with the key type
// metadata required for verification.
type Signature struct {
	Scheme    SignatureScheme `json:"scheme"`
	Signature []byte          `json:"signature"`
	PublicKey []byte          `json:"publicKey,omitempty"`
}

// Vote represents a vote message sent by a validator.
type Vote struct {
	BlockHash []byte   `json:"blockHash"`
	Round     int      `json:"round"`
	Type      VoteType `json:"type"`
	Height    uint64   `json:"height"`
}

// SignedVote bundles a vote with the validator identity and signature
// information.
type SignedVote struct {
	Vote      *Vote      `json:"vote"`
	Validator []byte     `json:"validator"`
	Signature *Signature `json:"signature"`
}

// Proposal represents a block proposal message sent by the round's proposer.
//
// ValidRound and ValidRoundProof together are the Proof-of-Lock-Change
// fields (NHB-AUDIT-C1): ValidRound of -1 means "a fresh value, built from
// mempool, with no prior-round polka behind it". A value >=0 means "this
// is a re-proposal of a value the proposer itself saw >=2/3 prevote power
// for (a Polka) at that earlier round", and ValidRoundProof carries the
// actual signed prevotes that constitute that Polka.
//
// The proof is verified PURELY cryptographically by every receiver
// (Engine.verifyPolkaProofLocked: recompute the exact vote payload for
// (height, ValidRound, this block's hash), check each signature, sum
// weighted power against the real validator set, compare to the 2/3
// threshold) -- never by asking "did I personally witness this round's
// gossip in real time". That distinction matters: an earlier version of
// this fix instead required each receiver to have independently recorded
// the polka from its own live vote tally, which silently failed whenever
// one validator's round counter raced ahead of another's before the
// relevant vote arrived (this codebase's HandleVote/HandleProposal both
// drop any message whose round is older than the receiver's current
// round) -- two validators could then lock on different rounds and
// deadlock forever, each unable to ever prove its claim to the other.
// Carrying the actual signatures makes verification stateless: it works
// regardless of when it happens or who's asking, closing that gap.
type Proposal struct {
	Block           *types.Block `json:"block"`
	Round           int          `json:"round"`
	ValidRound      int          `json:"validRound"`
	ValidRoundProof []*SignedVote `json:"validRoundProof,omitempty"`
}

// UnmarshalJSON defaults ValidRound to -1 when the field is absent from
// the payload (e.g. a message from a pre-PoLC peer during a rolling
// upgrade), rather than silently defaulting to Go's zero value 0 -- which
// would be misread as "claims a polka at round 0" instead of "no claim at
// all". -1 is always the conservative interpretation: it only ever makes
// a receiver MORE willing to accept a proposal (the plain
// not-locked-or-matches check), never less, so a missing field can't be
// exploited to bypass the lock.
func (p *Proposal) UnmarshalJSON(data []byte) error {
	type alias Proposal
	aux := &struct{ *alias }{alias: (*alias)(p)}
	p.ValidRound = -1
	if err := json.Unmarshal(data, aux); err != nil {
		return err
	}
	return nil
}

// SignedProposal wraps a proposal with proposer identity and signature
// metadata.
type SignedProposal struct {
	Proposal  *Proposal  `json:"proposal"`
	Proposer  []byte     `json:"proposer"`
	Signature *Signature `json:"signature"`
}

func (v *Vote) bytes() []byte     { b, _ := json.Marshal(v); return b }
func (p *Proposal) bytes() []byte { b, _ := json.Marshal(p); return b }
