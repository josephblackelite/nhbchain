package service

import (
	"context"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"nhbchain/consensus/codec"
	"nhbchain/core"
	"nhbchain/core/types"
	"nhbchain/network"
	consensusv1 "nhbchain/proto/consensus/v1"
)

type fakeConsensusNode struct {
	mu         sync.Mutex
	validators map[string]*big.Int
	commits    int
}

func newFakeConsensusNode() *fakeConsensusNode {
	return &fakeConsensusNode{validators: map[string]*big.Int{"validator-0": big.NewInt(1)}}
}

func (f *fakeConsensusNode) SubmitTransaction(tx *types.Transaction) error { return nil }

func (f *fakeConsensusNode) GetValidatorSet() map[string]*big.Int {
	f.mu.Lock()
	defer f.mu.Unlock()
	snapshot := make(map[string]*big.Int, len(f.validators))
	for addr, power := range f.validators {
		if power != nil {
			snapshot[addr] = new(big.Int).Set(power)
		} else {
			snapshot[addr] = nil
		}
	}
	return snapshot
}

func (f *fakeConsensusNode) GetBlockByHeight(height uint64) (*types.Block, error) { return nil, nil }
func (f *fakeConsensusNode) GetHeight() uint64                                    { return 0 }
func (f *fakeConsensusNode) GetMempool() []*types.Transaction                     { return nil }
func (f *fakeConsensusNode) CreateBlock(txs []*types.Transaction) (*types.Block, error) {
	return nil, nil
}
func (f *fakeConsensusNode) CommitBlock(block *types.Block) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commits++
	return nil
}
func (f *fakeConsensusNode) GetLastCommitHash() []byte { return nil }
func (f *fakeConsensusNode) QueryState(namespace, key string) (*core.QueryResult, error) {
	return nil, nil
}
func (f *fakeConsensusNode) QueryPrefix(namespace, prefix string) ([]core.QueryRecord, error) {
	return nil, nil
}
func (f *fakeConsensusNode) SimulateTx(txBytes []byte) (*core.SimulationResult, error) {
	return nil, nil
}

func TestServerGetValidatorSetConcurrentMutation(t *testing.T) {
	node := newFakeConsensusNode()
	srv := NewServer(node)

	const iterations = 1000
	ctx := context.Background()
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			node.mu.Lock()
			key := fmt.Sprintf("validator-%d", i%5)
			node.validators[key] = big.NewInt(int64(i))
			if i%3 == 0 {
				delete(node.validators, key)
			}
			node.mu.Unlock()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer func() {
			if r := recover(); r != nil {
				errCh <- fmt.Errorf("panic: %v", r)
			}
		}()
		for i := 0; i < iterations; i++ {
			resp, err := srv.GetValidatorSet(ctx, &consensusv1.GetValidatorSetRequest{})
			if err != nil {
				errCh <- err
				return
			}
			for range resp.GetValidators() {
				// Iterate to mimic downstream processing.
			}
		}
	}()

	wg.Wait()

	select {
	case err := <-errCh:
		t.Fatalf("GetValidatorSet encountered error: %v", err)
	default:
	}
}

func TestServerAuthorization(t *testing.T) {
	node := newFakeConsensusNode()
	secret := "shared-secret"
	header := "x-nhb-consensus-token"
	authorizer := network.NewTokenAuthenticator(header, secret)

	server := grpc.NewServer(
		grpc.Creds(insecure.NewCredentials()),
		grpc.ChainUnaryInterceptor(UnaryAuthInterceptor(authorizer)),
		grpc.ChainStreamInterceptor(StreamAuthInterceptor(authorizer)),
	)
	srv := NewServer(node, WithAuthorizer(authorizer))
	if srv.auth == nil {
		t.Fatalf("expected server authorizer to be configured")
	}
	if err := srv.authorize(context.Background()); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected authorizer to reject missing metadata, got %v", err)
	}
	consensusv1.RegisterConsensusServiceServer(server, srv)
	consensusv1.RegisterQueryServiceServer(server, srv)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	go server.Serve(lis)
	defer server.Stop()

	dialCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	unauthConn, err := grpc.DialContext(dialCtx, lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial unauthenticated: %v", err)
	}
	defer unauthConn.Close()

	block := &types.Block{Header: &types.BlockHeader{Height: 1}}
	pbBlock, err := codec.BlockToProto(block)
	if err != nil {
		t.Fatalf("block to proto: %v", err)
	}

	_, err = consensusv1.NewConsensusServiceClient(unauthConn).CommitBlock(dialCtx, &consensusv1.CommitBlockRequest{Block: pbBlock})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated error, got %v", err)
	}

	queryUnauth := consensusv1.NewQueryServiceClient(unauthConn)

	if _, err := queryUnauth.QueryState(context.Background(), &consensusv1.QueryStateRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated query state error, got %v", err)
	}

	if _, err := queryUnauth.SimulateTx(context.Background(), &consensusv1.SimulateTxRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated simulate tx error, got %v", err)
	}

	unauthStreamCtx, cancelUnauthStream := context.WithTimeout(context.Background(), time.Second)
	defer cancelUnauthStream()
	unauthStream, err := queryUnauth.QueryPrefix(unauthStreamCtx, &consensusv1.QueryPrefixRequest{})
	if err == nil {
		_, err = unauthStream.Recv()
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated query prefix error, got %v", err)
	}

	authConn, err := grpc.DialContext(
		dialCtx,
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(network.NewStaticTokenCredentialsAllowInsecure(header, secret)),
	)
	if err != nil {
		t.Fatalf("dial authenticated: %v", err)
	}
	defer authConn.Close()

	_, err = consensusv1.NewConsensusServiceClient(authConn).CommitBlock(context.Background(), &consensusv1.CommitBlockRequest{Block: pbBlock})
	if err != nil {
		t.Fatalf("authenticated commit failed: %v", err)
	}

	queryAuth := consensusv1.NewQueryServiceClient(authConn)

	if _, err := queryAuth.QueryState(context.Background(), &consensusv1.QueryStateRequest{}); err != nil {
		t.Fatalf("authenticated query state failed: %v", err)
	}

	if _, err := queryAuth.SimulateTx(context.Background(), &consensusv1.SimulateTxRequest{}); err != nil {
		t.Fatalf("authenticated simulate tx failed: %v", err)
	}

	authStreamCtx, cancelAuthStream := context.WithTimeout(context.Background(), time.Second)
	defer cancelAuthStream()
	authStream, err := queryAuth.QueryPrefix(authStreamCtx, &consensusv1.QueryPrefixRequest{})
	if err != nil {
		t.Fatalf("authenticated query prefix failed to open: %v", err)
	}
	if _, err := authStream.Recv(); err != io.EOF {
		t.Fatalf("expected EOF from authorized query prefix, got %v", err)
	}

	node.mu.Lock()
	commits := node.commits
	node.mu.Unlock()
	if commits != 1 {
		t.Fatalf("expected exactly one committed block, got %d", commits)
	}
}
