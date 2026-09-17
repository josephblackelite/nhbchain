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
	MsgTypePexRequest   byte = 0x0D
	MsgTypePexAddresses byte = 0x0E
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

// NewGetStatusMessage requests the latest observed chain height from peers.
func NewGetStatusMessage() (*Message, error) {
	payload, err := json.Marshal(struct{}{})
	if err != nil {
		return nil, err
	}
	return &Message{Type: MsgTypeGetStatus, Payload: payload}, nil
}

// NewStatusMessage advertises the latest committed chain height.
func NewStatusMessage(height uint64) (*Message, error) {
	payload, err := json.Marshal(StatusPayload{Height: height})
	if err != nil {
		return nil, err
	}
	return &Message{Type: MsgTypeStatus, Payload: payload}, nil
}

// NewGetBlocksMessage requests blocks starting at the provided height.
func NewGetBlocksMessage(from uint64) (*Message, error) {
	payload, err := json.Marshal(GetBlocksPayload{From: from})
	if err != nil {
		return nil, err
	}
	return &Message{Type: MsgTypeGetBlocks, Payload: payload}, nil
}

// NewBlocksMessage advertises a batch of canonical blocks.
func NewBlocksMessage(blocks []*types.Block) (*Message, error) {
	payload, err := json.Marshal(BlocksPayload{Blocks: blocks})
	if err != nil {
		return nil, err
	}
	return &Message{Type: MsgTypeBlocks, Payload: payload}, nil
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

// NewPexRequestMessage builds a peer exchange request using the provided limit and token.
func NewPexRequestMessage(limit int, token string) (*Message, error) {
	payload, err := json.Marshal(PexRequestPayload{Limit: limit, Token: token})
	if err != nil {
		return nil, err
	}
	return &Message{Type: MsgTypePexRequest, Payload: payload}, nil
}

// NewPexAddressesMessage builds a peer exchange response message.
func NewPexAddressesMessage(payload PexAddressesPayload) (*Message, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return &Message{Type: MsgTypePexAddresses, Payload: data}, nil
}
