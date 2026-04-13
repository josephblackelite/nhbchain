package service

import (
	"context"
	"fmt"

	"google.golang.org/grpc"

	"nhbchain/consensus/codec"
	"nhbchain/core"
	consensusv1 "nhbchain/proto/consensus/v1"
)

// Server exposes consensus functionality over gRPC.
type Server struct {
	consensusv1.UnimplementedConsensusServiceServer
	consensusv1.UnimplementedQueryServiceServer
	node core.ConsensusAPI
	auth Authorizer
}

// Authorizer evaluates whether an incoming request should be allowed.
type Authorizer interface {
	Authorize(context.Context) error
}

// ServerOption mutates server defaults during construction.
type ServerOption func(*Server)

// WithAuthorizer injects an authorizer evaluated for every RPC.
func WithAuthorizer(authorizer Authorizer) ServerOption {
	return func(s *Server) {
		if s != nil {
			s.auth = authorizer
		}
	}
}

// NewServer constructs a gRPC consensus server backed by the provided node API.
func NewServer(node core.ConsensusAPI, opts ...ServerOption) *Server {
	srv := &Server{node: node}
	for _, opt := range opts {
		if opt != nil {
			opt(srv)
		}
	}
	return srv
}

// SubmitTransaction injects a transaction into the node's mempool.
func (s *Server) SubmitTransaction(ctx context.Context, req *consensusv1.SubmitTransactionRequest) (*consensusv1.SubmitTransactionResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	tx, err := codec.TransactionFromProto(req.GetTransaction())
	if err != nil {
		return nil, err
	}
	if err := s.node.SubmitTransaction(tx); err != nil {
		return nil, err
	}
	return &consensusv1.SubmitTransactionResponse{}, nil
}

// SubmitTxEnvelope injects a signed transaction envelope into the node's mempool.
func (s *Server) SubmitTxEnvelope(ctx context.Context, req *consensusv1.SubmitTxEnvelopeRequest) (*consensusv1.SubmitTxEnvelopeResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	if _, err := codec.TransactionFromEnvelope(req.GetTx()); err != nil {
		return nil, err
	}
	if err := s.node.SubmitTxEnvelope(req.GetTx()); err != nil {
		return nil, err
	}
	return &consensusv1.SubmitTxEnvelopeResponse{}, nil
}

// GetValidatorSet returns the validator set tracked by the node.
func (s *Server) GetValidatorSet(ctx context.Context, _ *consensusv1.GetValidatorSetRequest) (*consensusv1.GetValidatorSetResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	snapshot := s.node.GetValidatorSet()
	validators := make([]*consensusv1.Validator, 0, len(snapshot))
	for key, power := range snapshot {
		validator := &consensusv1.Validator{
			Address: append([]byte(nil), []byte(key)...),
		}
		if power != nil {
			validator.Power = power.String()
		}
		validators = append(validators, validator)
	}
	return &consensusv1.GetValidatorSetResponse{Validators: validators}, nil
}

// GetBlockByHeight fetches a block from the canonical chain.
func (s *Server) GetBlockByHeight(ctx context.Context, req *consensusv1.GetBlockByHeightRequest) (*consensusv1.GetBlockByHeightResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	block, err := s.node.GetBlockByHeight(req.GetHeight())
	if err != nil {
		return nil, err
	}
	pbBlock, err := codec.BlockToProto(block)
	if err != nil {
		return nil, err
	}
	return &consensusv1.GetBlockByHeightResponse{Block: pbBlock}, nil
}

// GetHeight reports the node's current chain height.
func (s *Server) GetHeight(ctx context.Context, _ *consensusv1.GetHeightRequest) (*consensusv1.GetHeightResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return &consensusv1.GetHeightResponse{Height: s.node.GetHeight()}, nil
}

// GetMempool retrieves queued transactions that have not yet been committed.
func (s *Server) GetMempool(ctx context.Context, _ *consensusv1.GetMempoolRequest) (*consensusv1.GetMempoolResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	txs, err := codec.TransactionsToProto(s.node.GetMempool())
	if err != nil {
		return nil, err
	}
	return &consensusv1.GetMempoolResponse{Transactions: txs}, nil
}

// CreateBlock synthesises a block from the supplied transactions.
func (s *Server) CreateBlock(ctx context.Context, req *consensusv1.CreateBlockRequest) (*consensusv1.CreateBlockResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
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
	return &consensusv1.CreateBlockResponse{Block: pbBlock}, nil
}

// CommitBlock persists the supplied block into the chain and state.
func (s *Server) CommitBlock(ctx context.Context, req *consensusv1.CommitBlockRequest) (*consensusv1.CommitBlockResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	block, err := codec.BlockFromProto(req.GetBlock())
	if err != nil {
		return nil, err
	}
	if err := s.node.CommitBlock(block); err != nil {
		return nil, err
	}
	return &consensusv1.CommitBlockResponse{}, nil
}

// GetLastCommitHash returns the commit hash used for proposer selection.
func (s *Server) GetLastCommitHash(ctx context.Context, _ *consensusv1.GetLastCommitHashRequest) (*consensusv1.GetLastCommitHashResponse, error) {
	if s == nil || s.node == nil {
		return nil, fmt.Errorf("consensus service not initialised")
	}
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	hash := s.node.GetLastCommitHash()
	return &consensusv1.GetLastCommitHashResponse{Hash: append([]byte(nil), hash...)}, nil
}

// UnaryAuthInterceptor enforces authorization on unary gRPC handlers.
func UnaryAuthInterceptor(auth Authorizer) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if auth != nil {
			if err := auth.Authorize(ctx); err != nil {
				return nil, err
			}
		}
		return handler(ctx, req)
	}
}

// StreamAuthInterceptor enforces authorization on streaming gRPC handlers.
func StreamAuthInterceptor(auth Authorizer) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if auth != nil {
			if err := auth.Authorize(ss.Context()); err != nil {
				return err
			}
		}
		return handler(srv, ss)
	}
}

func (s *Server) authorize(ctx context.Context) error {
	if s == nil || s.auth == nil {
		return nil
	}
	return s.auth.Authorize(ctx)
}
