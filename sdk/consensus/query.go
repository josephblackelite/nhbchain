package consensus

import (
	"context"
	"fmt"
	"io"
	"math/big"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	consensusv1 "nhbchain/proto/consensus/v1"
)

// QueryRecord captures an individual key/value pair returned from a prefix query.
type QueryRecord struct {
	Key   string
	Value []byte
	Proof []byte
}

// SimulationResult represents the execution metadata returned by a transaction simulation.
type SimulationResult struct {
	GasUsed uint64
	GasCost *big.Int
	Events  []*consensusv1.Event
}

// QueryState fetches a value from the consensus query service.
func (c *Client) QueryState(ctx context.Context, namespace, key string) ([]byte, []byte, error) {
	if c == nil {
		return nil, nil, grpc.ErrClientConnClosing
	}
	resp, err := c.query.QueryState(ctx, &consensusv1.QueryStateRequest{Namespace: namespace, Key: key})
	if err != nil {
		return nil, nil, err
	}
	return resp.GetValue(), resp.GetProof(), nil
}

// QueryPrefix streams key/value pairs matching the supplied prefix.
func (c *Client) QueryPrefix(ctx context.Context, namespace, prefix string) ([]QueryRecord, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	stream, err := c.query.QueryPrefix(ctx, &consensusv1.QueryPrefixRequest{Namespace: namespace, Prefix: prefix})
	if err != nil {
		return nil, err
	}
	records := make([]QueryRecord, 0)
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		record := QueryRecord{
			Key:   msg.GetKey(),
			Value: append([]byte(nil), msg.GetValue()...),
			Proof: append([]byte(nil), msg.GetProof()...),
		}
		records = append(records, record)
	}
	return records, nil
}

// SimulateTx executes the provided transaction bytes against a copy of state and returns execution metadata.
func (c *Client) SimulateTx(ctx context.Context, tx *consensusv1.Transaction) (*SimulationResult, error) {
	if c == nil {
		return nil, grpc.ErrClientConnClosing
	}
	if tx == nil {
		return nil, fmt.Errorf("transaction required")
	}
	payload, err := proto.Marshal(tx)
	if err != nil {
		return nil, err
	}
	resp, err := c.query.SimulateTx(ctx, &consensusv1.SimulateTxRequest{TxBytes: payload})
	if err != nil {
		return nil, err
	}
	result := &SimulationResult{
		GasUsed: resp.GetGasUsed(),
		Events:  resp.GetEvents(),
	}
	if cost := resp.GetGasCost(); cost != "" {
		if parsed, ok := new(big.Int).SetString(cost, 10); ok {
			result.GasCost = parsed
		}
	}
	return result, nil
}
