package server

import (
	"context"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	lendingv1 "nhbchain/proto/lending/v1"
	"nhbchain/services/lendingd/config"
)

func TestMsgRPCAuthentication(t *testing.T) {
	cfg := config.AuthConfig{APITokens: []string{"secret-token"}}
	unaryAuth, streamAuth := NewAuthInterceptors(cfg)

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(unaryAuth),
		grpc.ChainStreamInterceptor(streamAuth),
	)
	lendingv1.RegisterLendingServiceServer(grpcServer, New())

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	go func() {
		_ = grpcServer.Serve(lis)
	}()
	defer grpcServer.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	client := lendingv1.NewLendingServiceClient(conn)

	// Unauthenticated Msg RPCs should be rejected.
	_, err = client.SupplyAsset(ctx, &lendingv1.SupplyAssetRequest{})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated error, got %v", err)
	}

	// Query-style RPCs remain accessible.
	_, err = client.GetMarket(ctx, &lendingv1.GetMarketRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected query RPC to reach handler, got %v", err)
	}

	// Authenticated requests should pass through to the handler.
	authCtx := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer secret-token")
	_, err = client.SupplyAsset(authCtx, &lendingv1.SupplyAssetRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected authenticated Msg RPC to reach handler, got %v", err)
	}
}
