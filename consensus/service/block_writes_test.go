package service

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"nhbchain/consensus/codec"
	"nhbchain/core/types"
	consensusv1 "nhbchain/proto/consensus/v1"
)

// Ledger PL-EXT-47 (bug-bounty report of 2026-11-18): CommitBlock over this service reaches node.CommitBlock, which checks the
// quorum certificate only for a block that arrives from a peer, and the block message cannot carry one. So whoever holds the
// service's one credential could commit a block no validator voted for. Block writes are off unless the server is built
// WithBlockWrites; nothing in the tree calls the client's CreateBlock or CommitBlock.

func commitRequest(t *testing.T) *consensusv1.CommitBlockRequest {
	t.Helper()
	pb, err := codec.BlockToProto(&types.Block{Header: &types.BlockHeader{Height: 1}})
	if err != nil {
		t.Fatalf("block to proto: %v", err)
	}
	return &consensusv1.CommitBlockRequest{Block: pb}
}

func TestBlockWritesAreOffUnlessAskedFor(t *testing.T) {
	node := newFakeConsensusNode()
	srv := NewServer(node)

	if _, err := srv.CommitBlock(context.Background(), commitRequest(t)); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("CommitBlock with block writes off: got %v, want PermissionDenied", err)
	}
	if _, err := srv.CreateBlock(context.Background(), &consensusv1.CreateBlockRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("CreateBlock with block writes off: got %v, want PermissionDenied", err)
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.commits != 0 {
		t.Fatalf("the node was asked to commit %d block(s) although block writes are off", node.commits)
	}
}

func TestBlockWritesWorkWhenTheServerIsBuiltWithThem(t *testing.T) {
	node := newFakeConsensusNode()
	srv := NewServer(node, WithBlockWrites())

	if _, err := srv.CommitBlock(context.Background(), commitRequest(t)); err != nil {
		t.Fatalf("CommitBlock with block writes on: %v", err)
	}
	node.mu.Lock()
	defer node.mu.Unlock()
	if node.commits != 1 {
		t.Fatalf("expected one commit, got %d", node.commits)
	}
}

func TestTheCredentialIsCheckedBeforeBlockWrites(t *testing.T) {
	// Without a valid credential the caller learns nothing about the switch: the authorizer answers first.
	node := newFakeConsensusNode()
	srv := NewServer(node, WithAuthorizer(denyAll{}))
	if _, err := srv.CommitBlock(context.Background(), commitRequest(t)); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("CommitBlock without a credential: got %v, want Unauthenticated", err)
	}
	if _, err := srv.CreateBlock(context.Background(), &consensusv1.CreateBlockRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("CreateBlock without a credential: got %v, want Unauthenticated", err)
	}
}

type denyAll struct{}

func (denyAll) Authorize(context.Context) error {
	return status.Error(codes.Unauthenticated, "no credential")
}
