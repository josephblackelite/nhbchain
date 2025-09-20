package p2p

import (
	"encoding/json"
	"nhbchain/core/types"
)

// Constants for our P2P message types.
const (
	MsgTypeTx        byte = 0x01
	MsgTypeBlock     byte = 0x02
	MsgTypeGetStatus byte = 0x03
	MsgTypeStatus    byte = 0x04
	MsgTypeGetBlocks byte = 0x05
	MsgTypeBlocks    byte = 0x06
	MsgTypeProposal  byte = 0x07
	MsgTypeVote      byte = 0x08
)

// StatusPayload is the data sent in a status message.
type StatusPayload struct {
	Height uint64
}

// GetBlocksPayload is the data for requesting blocks.
type GetBlocksPayload struct {
	From uint64
}

// BlocksPayload contains the blocks being sent in response.
type BlocksPayload struct {
	Blocks []*types.Block
}

// --- Message Creation Helpers ---

func NewTxMessage(tx *types.Transaction) (*Message, error) {
	payload, err := json.Marshal(tx)
	if err != nil {
		return nil, err
	}
	return &Message{Type: MsgTypeTx, Payload: payload}, nil
}

func NewBlockMessage(b *types.Block) (*Message, error) {
	payload, err := json.Marshal(b)
	if err != nil {
		return nil, err
	}
	return &Message{Type: MsgTypeBlock, Payload: payload}, nil
}
