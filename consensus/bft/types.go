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
type Proposal struct {
	Block *types.Block `json:"block"`
	Round int          `json:"round"`
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
