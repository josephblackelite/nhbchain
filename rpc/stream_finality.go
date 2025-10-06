package rpc

import (
	"encoding/hex"
	"strings"

	"nhbchain/core"
	posv1 "nhbchain/proto/pos"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type FinalityStream struct {
	posv1.UnimplementedRealtimeServer
	node *core.Node
}

func NewFinalityStream(node *core.Node) *FinalityStream {
	return &FinalityStream{node: node}
}

func (s *FinalityStream) SubscribeFinality(req *posv1.SubscribeFinalityRequest, stream posv1.Realtime_SubscribeFinalityServer) error {
	if s == nil || s.node == nil {
		return status.Error(codes.Unavailable, "node unavailable")
	}
	ctx := stream.Context()
	cursor := ""
	if req != nil {
		cursor = strings.TrimSpace(req.GetCursor())
	}
	updates, cancel, backlog, err := s.node.POSFinalitySubscribe(ctx, cursor)
	if err != nil {
		return status.Errorf(codes.Internal, "subscribe: %v", err)
	}
	defer cancel()

	for _, update := range backlog {
		if err := stream.Send(&posv1.SubscribeFinalityResponse{Update: convertFinalityUpdate(update)}); err != nil {
			return err
		}
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			if err := stream.Send(&posv1.SubscribeFinalityResponse{Update: convertFinalityUpdate(update)}); err != nil {
				return err
			}
		}
	}
}

func convertFinalityUpdate(update core.POSFinalityUpdate) *posv1.FinalityUpdate {
	if (len(update.IntentRef) == 0 && len(update.TxHash) == 0) && update.Cursor == "" {
		return nil
	}
	msg := &posv1.FinalityUpdate{
		Cursor:    update.Cursor,
		IntentRef: append([]byte(nil), update.IntentRef...),
		TxHash:    append([]byte(nil), update.TxHash...),
		Status:    mapFinalityStatus(update.Status),
		Height:    update.Height,
		Timestamp: update.Timestamp,
	}
	if len(update.BlockHash) > 0 {
		msg.BlockHash = append([]byte(nil), update.BlockHash...)
	}
	return msg
}

func mapFinalityStatus(status core.POSFinalityStatus) posv1.FinalityStatus {
	switch status {
	case core.POSFinalityStatusPending:
		return posv1.FinalityStatus_FINALITY_STATUS_PENDING
	case core.POSFinalityStatusFinalized:
		return posv1.FinalityStatus_FINALITY_STATUS_FINALIZED
	default:
		return posv1.FinalityStatus_FINALITY_STATUS_UNSPECIFIED
	}
}

type finalityUpdatePayload struct {
	Type      string `json:"type"`
	Cursor    string `json:"cursor"`
	IntentRef string `json:"intentRef"`
	TxHash    string `json:"txHash"`
	Status    string `json:"status"`
	Block     string `json:"block,omitempty"`
	Height    uint64 `json:"height,omitempty"`
	Timestamp int64  `json:"ts"`
}

func finalityUpdatePayloadFrom(update core.POSFinalityUpdate) finalityUpdatePayload {
	payload := finalityUpdatePayload{
		Type:      "tx_update",
		Cursor:    update.Cursor,
		IntentRef: encodeHex(update.IntentRef),
		TxHash:    encodeHex(update.TxHash),
		Status:    string(update.Status),
		Height:    update.Height,
		Timestamp: update.Timestamp,
	}
	if payload.Status == "" {
		payload.Status = string(core.POSFinalityStatusPending)
	}
	if len(update.BlockHash) > 0 {
		payload.Block = encodeHex(update.BlockHash)
	}
	return payload
}

func encodeHex(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return "0x" + hex.EncodeToString(data)
}
