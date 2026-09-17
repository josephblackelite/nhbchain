package netsec

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"testing"

	"nhbchain/network"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type authFunc func(context.Context) error

func (f authFunc) Authorize(ctx context.Context) error { return f(ctx) }

func TestChainAuthenticatorsShortCircuits(t *testing.T) {
	invoked := 0
	deny := authFunc(func(context.Context) error {
		invoked++
		return status.Error(codes.PermissionDenied, "denied")
	})
	never := authFunc(func(context.Context) error {
		invoked++
		return nil
	})
	chained := network.ChainAuthenticators(deny, never)
	if err := chained.Authorize(context.Background()); err == nil {
		t.Fatal("expected error")
	}
	if invoked != 1 {
		t.Fatalf("expected single invocation, got %d", invoked)
	}
}

func TestTokenAuthenticatorAcceptsBearer(t *testing.T) {
	auth := network.NewTokenAuthenticator("Authorization", "secret-token")
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer secret-token"))
	if err := auth.Authorize(ctx); err != nil {
		t.Fatalf("expected bearer token to pass: %v", err)
	}
	bad := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer wrong"))
	if err := auth.Authorize(bad); err == nil {
		t.Fatal("expected invalid token to be rejected")
	}
}

func TestTLSAuthorizerMatchesAllowlist(t *testing.T) {
	cert := &x509.Certificate{Subject: pkix.Name{CommonName: "audit-client"}}
	tlsInfo := credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}}
	ctx := peer.NewContext(context.Background(), &peer.Peer{AuthInfo: tlsInfo})

	allow := network.NewTLSAuthorizer([]string{"audit-client"})
	if err := allow.Authorize(ctx); err != nil {
		t.Fatalf("expected certificate allow list to pass: %v", err)
	}

	deny := network.NewTLSAuthorizer([]string{"other"})
	if err := deny.Authorize(ctx); err == nil {
		t.Fatal("expected mismatch to be rejected")
	}
}
