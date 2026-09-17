package network

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"net/url"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

func TestTokenAuthenticatorConstantTime(t *testing.T) {
	t.Parallel()

	auth := NewTokenAuthenticator("x-nhb-token", "super-secret")
	if auth == nil {
		t.Fatalf("authenticator should not be nil")
	}

	directCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-nhb-token", "super-secret"))
	if err := auth.Authorize(directCtx); err != nil {
		t.Fatalf("direct token should authorize: %v", err)
	}

	bearerCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-nhb-token", "Bearer super-secret"))
	if err := auth.Authorize(bearerCtx); err != nil {
		t.Fatalf("bearer token should authorize: %v", err)
	}

	mismatchCtx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-nhb-token", "super-secret-with-extra"))
	if err := auth.Authorize(mismatchCtx); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated for mismatched token, got %v", err)
	}
}

func TestTLSAuthorizerMatchesSANsAndCN(t *testing.T) {
	t.Parallel()

	allowed := []string{"example.com", "spiffe://network/service"}
	auth := NewTLSAuthorizer(allowed)
	uri, err := url.Parse("spiffe://network/service")
	if err != nil {
		t.Fatalf("parse uri: %v", err)
	}
	cert := &x509.Certificate{
		DNSNames: []string{"Example.COM"},
		URIs:     []*url.URL{uri},
		Subject:  pkixName("ignored"),
	}
	ctx := tlsPeerContext(cert)
	if err := auth.Authorize(ctx); err != nil {
		t.Fatalf("SAN match should authorize: %v", err)
	}

	cnCert := &x509.Certificate{Subject: pkixName("Example.com")}
	cnCtx := tlsPeerContext(cnCert)
	if err := auth.Authorize(cnCtx); err != nil {
		t.Fatalf("CN match should authorize: %v", err)
	}

	mismatch := &x509.Certificate{Subject: pkixName("other")}
	mismatchCtx := tlsPeerContext(mismatch)
	if err := auth.Authorize(mismatchCtx); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected permission denied for mismatched cert, got %v", err)
	}
}

func TestNilReadAuthForbidden(t *testing.T) {
	t.Parallel()

	relay := NewRelay()
	auth := authenticatorFunc(func(context.Context) error { return nil })

	if _, err := NewService(relay, auth, WithReadAuthenticator(nil)); err == nil {
		t.Fatalf("expected error when read auth is nil without opt-in")
	}

	svc, err := NewService(relay, auth, WithReadAuthenticator(nil), WithAllowUnauthenticatedReads(true))
	if err != nil {
		t.Fatalf("unexpected error when opting into anonymous reads: %v", err)
	}
	if svc == nil {
		t.Fatalf("expected service instance")
	}
}

func tlsPeerContext(cert *x509.Certificate) context.Context {
	info := credentials.TLSInfo{State: tls.ConnectionState{PeerCertificates: []*x509.Certificate{cert}}}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: info})
}

func pkixName(cn string) pkix.Name {
	return pkix.Name{CommonName: cn}
}
