package service

import (
	"context"
	"fmt"

	"nhbchain/consensus/codec"
	"nhbchain/core"
	consensuspb "nhbchain/proto/consensus"
)

// Server exposes consensus functionality over gRPC.
type Server struct {
	consensuspb.UnimplementedConsensusServer
	node core.ConsensusAPI
}

// NewServer constructs a gRPC consensus server backed by the provided node API.
func NewServer(node core.ConsensusAPI) *Server {
	return &Server{node: node}
}

// SubmitTransaction injects a transaction into the node's mempool.
func (s *Server) SubmitTransaction(ctx context.Context, req *consensuspb.SubmitTransactionRequest) (*consensuspb.SubmitTransactionResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	tx, err := codec.TransactionFromProto(req.GetTransaction())
	if err != nil {
		return nil, err
	}
	if err := s.node.SubmitTransaction(tx); err != nil {
		return nil, err
	}
	return &consensuspb.SubmitTransactionResponse{}, nil
}

// GetValidatorSet returns the validator set tracked by the node.
func (s *Server) GetValidatorSet(ctx context.Context, _ *consensuspb.GetValidatorSetRequest) (*consensuspb.GetValidatorSetResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	snapshot := s.node.GetValidatorSet()
	validators := make([]*consensuspb.Validator, 0, len(snapshot))
	for key, power := range snapshot {
		validator := &consensuspb.Validator{
			Address: append([]byte(nil), []byte(key)...),
		}
		if power != nil {
			validator.Power = power.String()
		}
		validators = append(validators, validator)
	}
	return &consensuspb.GetValidatorSetResponse{Validators: validators}, nil
}

// GetBlockByHeight fetches a block from the canonical chain.
func (s *Server) GetBlockByHeight(ctx context.Context, req *consensuspb.GetBlockByHeightRequest) (*consensuspb.GetBlockByHeightResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	block, err := s.node.GetBlockByHeight(req.GetHeight())
	if err != nil {
		return nil, err
	}
	pbBlock, err := codec.BlockToProto(block)
	if err != nil {
		return nil, err
	}
	return &consensuspb.GetBlockByHeightResponse{Block: pbBlock}, nil
}

// GetHeight reports the node's current chain height.
func (s *Server) GetHeight(ctx context.Context, _ *consensuspb.GetHeightRequest) (*consensuspb.GetHeightResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	return &consensuspb.GetHeightResponse{Height: s.node.GetHeight()}, nil
}

// GetMempool drains the current mempool contents.
func (s *Server) GetMempool(ctx context.Context, _ *consensuspb.GetMempoolRequest) (*consensuspb.GetMempoolResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	txs, err := codec.TransactionsToProto(s.node.GetMempool())
	if err != nil {
		return nil, err
	}
	return &consensuspb.GetMempoolResponse{Transactions: txs}, nil
}

// CreateBlock synthesises a block from the supplied transactions.
func (s *Server) CreateBlock(ctx context.Context, req *consensuspb.CreateBlockRequest) (*consensuspb.CreateBlockResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	txs, err := codec.TransactionsFromProto(req.GetTransactions())
	if err != nil {
		return nil, err
	}
	block, err := s.node.CreateBlock(txs)
	if err != nil {
		return nil, err
	}
	pbBlock, err := codec.BlockToProto(block)
	if err != nil {
		return nil, err
	}
	return &consensuspb.CreateBlockResponse{Block: pbBlock}, nil
}

// CommitBlock persists the supplied block into the chain and state.
func (s *Server) CommitBlock(ctx context.Context, req *consensuspb.CommitBlockRequest) (*consensuspb.CommitBlockResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	block, err := codec.BlockFromProto(req.GetBlock())
	if err != nil {
		return nil, err
	}
	if err := s.node.CommitBlock(block); err != nil {
		return nil, err
	}
	return &consensuspb.CommitBlockResponse{}, nil
}

// GetLastCommitHash returns the commit hash used for proposer selection.
func (s *Server) GetLastCommitHash(ctx context.Context, _ *consensuspb.GetLastCommitHashRequest) (*consensuspb.GetLastCommitHashResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	hash := s.node.GetLastCommitHash()
	return &consensuspb.GetLastCommitHashResponse{Hash: append([]byte(nil), hash...)}, nil
}
