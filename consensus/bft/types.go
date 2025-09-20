package bft

import (
	"nhbchain/core/types"
)

// VoteType defines the type of a BFT vote.
type VoteType byte

const (
	Prevote   VoteType = 0x01
	Precommit VoteType = 0x02
)

// Vote represents a vote message sent by a validator.
type Vote struct {
	BlockHash []byte
	Round     int
	Type      VoteType
	Validator []byte // Address of the validator
	Signature []byte // Validator's signature on the vote
}

// Proposal represents a block proposal message sent by the round's proposer.
type Proposal struct {
	Block    *types.Block
	Round    int
	Proposer []byte // Address of the proposer
}
