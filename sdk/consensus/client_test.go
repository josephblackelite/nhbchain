package consensus_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	consensusv1 "nhbchain/proto/consensus/v1"
	consensusclient "nhbchain/sdk/consensus"
)

type envelopeRecorder struct {
	consensusv1.UnimplementedConsensusServiceServer
	mu       sync.Mutex
	requests []*consensusv1.SubmitTxEnvelopeRequest
}

func (e *envelopeRecorder) SubmitTxEnvelope(ctx context.Context, req *consensusv1.SubmitTxEnvelopeRequest) (*consensusv1.SubmitTxEnvelopeResponse, error) {
	e.mu.Lock()
	e.requests = append(e.requests, req)
	e.mu.Unlock()
	return &consensusv1.SubmitTxEnvelopeResponse{}, nil
}

func TestClientSubmitEnvelope(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	recorder := &envelopeRecorder{}
	consensusv1.RegisterConsensusServiceServer(server, recorder)

	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Stop()
	defer listener.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	client, err := consensusclient.Dial(ctx, "bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial consensus: %v", err)
	}
	defer client.Close()

	envelope := &consensusv1.SignedTxEnvelope{}
	if err := client.SubmitEnvelope(ctx, envelope); err != nil {
		t.Fatalf("submit envelope: %v", err)
	}

	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.requests) != 1 {
		t.Fatalf("expected 1 request, got %d", len(recorder.requests))
	}
	if !proto.Equal(recorder.requests[0].GetTx(), envelope) {
		t.Fatalf("unexpected envelope forwarded")
	}
}
