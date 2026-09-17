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
// ValidRound is the Proof-of-Lock-Change field (NHB-AUDIT-C1): -1 means
// "a fresh value, built from mempool, with no prior-round polka behind it".
// A value >=0 means "this is a re-proposal of a value the proposer itself
// saw >=2/3 prevote power for (a Polka) at that earlier round" -- receivers
// never trust this claim blindly; Engine.lockCompliesLocked only honors it
// if the receiving validator independently observed the same polka itself
// (see Engine.polkaHistory). This is what lets an honest validator safely
// switch away from an earlier lock instead of being stuck forever, while
// still making it impossible for two different quorums to each commit a
// different block at the same height (see Engine.lockedBlock/lockedRound).
type Proposal struct {
	Block      *types.Block `json:"block"`
	Round      int          `json:"round"`
	ValidRound int          `json:"validRound"`
}

// UnmarshalJSON defaults ValidRound to -1 when the field is absent from the
// payload (e.g. a message from a pre-PoLC peer during a rolling upgrade),
// rather than silently defaulting to Go's zero value 0 -- which would be
// misread as "claims a polka at round 0" instead of "no claim at all". -1
// is always the conservative interpretation: it only ever makes a receiver
// MORE willing to accept a proposal (the plain not-locked-or-matches check),
// never less, so a missing field can't be exploited to bypass the lock.
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
