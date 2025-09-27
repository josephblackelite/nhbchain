package service

import (
	"context"
	"fmt"

	consensusv1 "nhbchain/proto/consensus/v1"
)

func (s *Server) QueryState(ctx context.Context, req *consensusv1.QueryStateRequest) (*consensusv1.QueryStateResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	result, err := s.node.QueryState(req.GetNamespace(), req.GetKey())
	if err != nil {
		return nil, err
	}
	resp := &consensusv1.QueryStateResponse{}
	if result != nil {
		if len(result.Value) > 0 {
			resp.Value = append([]byte(nil), result.Value...)
		}
		if len(result.Proof) > 0 {
			resp.Proof = append([]byte(nil), result.Proof...)
		}
	}
	return resp, nil
}

func (s *Server) QueryPrefix(req *consensusv1.QueryPrefixRequest, stream consensusv1.QueryService_QueryPrefixServer) error {
	if s == nil || s.node == nil {
		return fmt.Errorf("consensus service not initialised")
	}
	records, err := s.node.QueryPrefix(req.GetNamespace(), req.GetPrefix())
	if err != nil {
		return err
	}
	for _, record := range records {
		msg := &consensusv1.QueryPrefixResponse{Key: record.Key}
		if len(record.Value) > 0 {
			msg.Value = append([]byte(nil), record.Value...)
		}
		if len(record.Proof) > 0 {
			msg.Proof = append([]byte(nil), record.Proof...)
		}
		if err := stream.Send(msg); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) SimulateTx(ctx context.Context, req *consensusv1.SimulateTxRequest) (*consensusv1.SimulateTxResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	result, err := s.node.SimulateTx(req.GetTxBytes())
	if err != nil {
		return nil, err
	}
	resp := &consensusv1.SimulateTxResponse{}
	if result != nil {
		resp.GasUsed = result.GasUsed
		if result.GasCost != nil {
			resp.GasCost = result.GasCost.String()
		}
		if len(result.Events) > 0 {
			resp.Events = make([]*consensusv1.Event, len(result.Events))
			for i, evt := range result.Events {
				attributes := make(map[string]string, len(evt.Attributes))
				for k, v := range evt.Attributes {
					attributes[k] = v
				}
				resp.Events[i] = &consensusv1.Event{Type: evt.Type, Attributes: attributes}
			}
		}
	}
	return resp, nil
}
