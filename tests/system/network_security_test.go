package system

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"nhbchain/config"
	"nhbchain/network"
	networkv1 "nhbchain/proto/network/v1"
)

func TestNetworkServiceSharedSecretAuth(t *testing.T) {
	relay := network.NewRelay()
	secret := "shared-secret"
	header := "x-nhb-network-token"

	server := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
	svc, err := network.NewService(relay, network.NewTokenAuthenticator(header, secret))
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	networkv1.RegisterNetworkServiceServer(server, svc)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		server.Stop()
		_ = lis.Close()
	})

	go func() {
		_ = server.Serve(lis)
	}()

	addr := lis.Addr().String()

	// Unauthenticated gossip stream should fail.
	unauthCtx, cancelUnauth := context.WithTimeout(context.Background(), 2*time.Second)
	t.Cleanup(cancelUnauth)
	unauthClient, err := network.Dial(unauthCtx, addr, true, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial unauth: %v", err)
	}
	if err := unauthClient.Run(unauthCtx, nil, nil); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated stream error, got %v", err)
	}
	_ = unauthClient.Close()

	// Unauthenticated unary calls should also be rejected.
	conn, err := grpc.DialContext(unauthCtx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial unauth conn: %v", err)
	}
	client := networkv1.NewNetworkServiceClient(conn)
	_, err = client.DialPeer(unauthCtx, &networkv1.DialPeerRequest{Target: "example"})
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated dial, got %v", err)
	}
	if _, err = client.GetView(unauthCtx, &networkv1.GetViewRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated view, got %v", err)
	}
	if _, err = client.ListPeers(unauthCtx, &networkv1.ListPeersRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated peer list, got %v", err)
	}
	_ = conn.Close()

	// Authenticated stream should remain connected until the context is cancelled.
	authCtx, cancelAuth := context.WithCancel(context.Background())
	defer cancelAuth()
	authClient, err := network.Dial(authCtx, addr, true,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(network.NewStaticTokenCredentialsAllowInsecure(header, secret)),
	)
	if err != nil {
		t.Fatalf("dial auth: %v", err)
	}
	streamDone := make(chan error, 1)
	go func() {
		streamDone <- authClient.Run(authCtx, nil, nil)
	}()
	time.Sleep(50 * time.Millisecond)
	cancelAuth()
	if err := <-streamDone; err != nil && status.Code(err) != codes.Canceled {
		t.Fatalf("expected stream cancellation, got %v", err)
	}
	_ = authClient.Close()

	// Authenticated unary calls should reach the service (even if the relay has no backing server).
	authConn, err := grpc.DialContext(context.Background(), addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(network.NewStaticTokenCredentialsAllowInsecure(header, secret)),
	)
	if err != nil {
		t.Fatalf("dial auth conn: %v", err)
	}
	defer authConn.Close()
	_, err = networkv1.NewNetworkServiceClient(authConn).BanPeer(context.Background(), &networkv1.BanPeerRequest{NodeId: "peer"})
	if status.Code(err) == codes.Unauthenticated {
		t.Fatalf("authenticated request should not be rejected: %v", err)
	}
}

func TestNetworkServiceAnonymousReadsOptIn(t *testing.T) {
	relay := network.NewRelay()
	header := "x-nhb-network-token"
	secret := "shared-secret"

	server := grpc.NewServer(grpc.Creds(insecure.NewCredentials()))
	svc, err := network.NewService(relay, network.NewTokenAuthenticator(header, secret),
		network.WithReadAuthenticator(nil),
		network.WithAllowUnauthenticatedReads(true),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	networkv1.RegisterNetworkServiceServer(server, svc)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() {
		server.Stop()
		_ = lis.Close()
	})

	go func() {
		_ = server.Serve(lis)
	}()

	addr := lis.Addr().String()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial unauth conn: %v", err)
	}
	defer conn.Close()

	client := networkv1.NewNetworkServiceClient(conn)

	if _, err := client.GetView(ctx, &networkv1.GetViewRequest{}); status.Code(err) == codes.Unauthenticated {
		t.Fatalf("anonymous view should bypass auth, got %v", err)
	}
	if _, err := client.ListPeers(ctx, &networkv1.ListPeersRequest{}); status.Code(err) == codes.Unauthenticated {
		t.Fatalf("anonymous peer list should bypass auth, got %v", err)
	}
	if _, err := client.BanPeer(ctx, &networkv1.BanPeerRequest{NodeId: "peer"}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated ban, got %v", err)
	}
}

func TestNetworkServerSecurityRequiresAuthenticator(t *testing.T) {
	cfg := &config.Config{
		NetworkSecurity: config.NetworkSecurity{
			AllowInsecure: true,
		},
	}
	if _, _, _, err := network.BuildServerSecurity(&cfg.NetworkSecurity, "", nil); err == nil || !strings.Contains(err.Error(), "shared secret or client certificate authentication") {
		t.Fatalf("expected missing authenticator error, got %v", err)
	}
}

func TestNetworkServerSecurityRequiresTLSUnlessExplicitlyInsecure(t *testing.T) {
	cfg := &config.Config{
		NetworkSecurity: config.NetworkSecurity{
			AllowInsecure: false,
			SharedSecret:  "token",
		},
	}
	if _, _, _, err := network.BuildServerSecurity(&cfg.NetworkSecurity, "", nil); err == nil || !strings.Contains(err.Error(), "missing TLS material") {
		t.Fatalf("expected missing TLS error, got %v", err)
	}
}
