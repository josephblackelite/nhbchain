package p2p

import (
	"encoding/json"
	"time"

	"nhbchain/core/types"
)

// Constants for our P2P message types.
const (
	MsgTypeTx           byte = 0x01
	MsgTypeBlock        byte = 0x02
	MsgTypeGetStatus    byte = 0x03
	MsgTypeStatus       byte = 0x04
	MsgTypeGetBlocks    byte = 0x05
	MsgTypeBlocks       byte = 0x06
	MsgTypeProposal     byte = 0x07
	MsgTypeVote         byte = 0x08
	MsgTypePing         byte = 0x09
	MsgTypePong         byte = 0x0A
	MsgTypeHandshake    byte = 0x0B
	MsgTypeHandshakeAck byte = 0x0C
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

// PingPayload is exchanged as a lightweight keepalive message.
type PingPayload struct {
	Nonce     uint64 `json:"nonce"`
	Timestamp int64  `json:"timestamp"`
}

// PongPayload acknowledges receipt of a ping message.
type PongPayload struct {
	Nonce     uint64 `json:"nonce"`
	Timestamp int64  `json:"timestamp"`
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

// NewPingMessage builds a ping keepalive message using the provided nonce and timestamp.
func NewPingMessage(nonce uint64, ts time.Time) (*Message, error) {
	payload, err := json.Marshal(PingPayload{Nonce: nonce, Timestamp: ts.UnixNano()})
	if err != nil {
		return nil, err
	}
	return &Message{Type: MsgTypePing, Payload: payload}, nil
}

// NewPongMessage builds a pong response echoing the supplied nonce.
func NewPongMessage(nonce uint64, ts time.Time) (*Message, error) {
	payload, err := json.Marshal(PongPayload{Nonce: nonce, Timestamp: ts.UnixNano()})
	if err != nil {
		return nil, err
	}
	return &Message{Type: MsgTypePong, Payload: payload}, nil
}
