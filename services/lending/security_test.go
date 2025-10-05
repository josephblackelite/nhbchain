package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"nhbchain/network"
	lendingv1 "nhbchain/proto/lending/v1"
	lendingserver "nhbchain/services/lending/server"
)

func TestLendingServerRejectsInsecureAndUnauthenticated(t *testing.T) {
	tmpDir := t.TempDir()
	serverCertPath, serverKeyPath, caPEM := writeTestServerCredentials(t, tmpDir)

	creds, mtlsEnabled, err := buildServerCredentials(serverCertPath, serverKeyPath, "", false)
	if err != nil {
		t.Fatalf("build server credentials: %v", err)
	}
	if mtlsEnabled {
		t.Fatalf("expected mTLS to be disabled when client CA path is empty")
	}

	token := "shared-secret"
	header := "x-nhb-auth"
	auth := network.NewTokenAuthenticator(header, token)
	if auth == nil {
		t.Fatal("token authenticator should not be nil when secret provided")
	}
	unaryAuth, streamAuth := newAuthInterceptors(auth)

	server := grpc.NewServer(
		grpc.Creds(creds),
		grpc.ChainUnaryInterceptor(unaryAuth),
		grpc.ChainStreamInterceptor(streamAuth),
	)
	lendingv1.RegisterLendingServiceServer(server, lendingserver.New())

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	go func() {
		_ = server.Serve(lis)
	}()
	defer server.Stop()

	addr := lis.Addr().String()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Plaintext connections should fail once the RPC is attempted.
	plaintextConn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial plaintext: %v", err)
	}
	_, plaintextErr := lendingv1.NewLendingServiceClient(plaintextConn).GetMarket(ctx, &lendingv1.GetMarketRequest{Key: &lendingv1.MarketKey{Symbol: "demo"}})
	_ = plaintextConn.Close()
	if plaintextErr == nil {
		t.Fatal("expected plaintext call to fail tls handshake")
	}
	if status.Code(plaintextErr) != codes.Unavailable || (!strings.Contains(plaintextErr.Error(), "authentication handshake failed") && !strings.Contains(plaintextErr.Error(), "server preface")) {
		t.Fatalf("unexpected plaintext error: %v", plaintextErr)
	}

	// TLS connections without authentication should be rejected by the interceptor.
	rootPool := x509.NewCertPool()
	if !rootPool.AppendCertsFromPEM(caPEM) {
		t.Fatal("failed to add CA to pool")
	}
	tlsCfg := &tls.Config{RootCAs: rootPool}

	unauthConn, err := grpc.DialContext(ctx, addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		t.Fatalf("dial tls: %v", err)
	}
	_, err = lendingv1.NewLendingServiceClient(unauthConn).GetMarket(ctx, &lendingv1.GetMarketRequest{Key: &lendingv1.MarketKey{Symbol: "demo"}})
	_ = unauthConn.Close()
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected unauthenticated error, got %v", err)
	}

	// Authenticated connections should reach the handler.
	authConn, err := grpc.DialContext(ctx, addr,
		grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)),
		grpc.WithPerRPCCredentials(network.NewStaticTokenCredentials(header, token)),
	)
	if err != nil {
		t.Fatalf("dial authenticated: %v", err)
	}
	defer authConn.Close()
	_, err = lendingv1.NewLendingServiceClient(authConn).GetMarket(ctx, &lendingv1.GetMarketRequest{Key: &lendingv1.MarketKey{Symbol: "demo"}})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("expected unimplemented error from placeholder handler, got %v", err)
	}
}

func writeTestServerCredentials(t *testing.T, dir string) (certPath, keyPath string, caPEM []byte) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate ca key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create ca cert: %v", err)
	}
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})

	serverKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serverTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}
	serverCertPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER})
	serverKeyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(serverKey)})

	certPath = filepath.Join(dir, "server.pem")
	keyPath = filepath.Join(dir, "server.key")
	if err := os.WriteFile(certPath, serverCertPEM, 0600); err != nil {
		t.Fatalf("write server cert: %v", err)
	}
	if err := os.WriteFile(keyPath, serverKeyPEM, 0600); err != nil {
		t.Fatalf("write server key: %v", err)
	}
	return certPath, keyPath, caPEM
}
