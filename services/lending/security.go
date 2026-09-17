package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"nhbchain/network"
)

func buildServerCredentials(certPath, keyPath, clientCAPath string, mtlsRequired bool) (credentials.TransportCredentials, bool, error) {
	certPath = strings.TrimSpace(certPath)
	keyPath = strings.TrimSpace(keyPath)
	clientCAPath = strings.TrimSpace(clientCAPath)

	if certPath == "" || keyPath == "" {
		return nil, false, fmt.Errorf("server certificate and key are required")
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, false, fmt.Errorf("load server certificate: %w", err)
	}

	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}

	var clientPool *x509.CertPool
	if clientCAPath != "" {
		pem, err := os.ReadFile(clientCAPath)
		if err != nil {
			return nil, false, fmt.Errorf("read client ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, false, fmt.Errorf("parse client ca: invalid pem data")
		}
		clientPool = pool
		tlsCfg.ClientCAs = pool
	}

	mtlsEnabled := false
	switch {
	case mtlsRequired:
		if clientPool == nil {
			return nil, false, fmt.Errorf("client ca bundle required for mtls")
		}
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
		mtlsEnabled = true
	case clientPool != nil:
		tlsCfg.ClientAuth = tls.VerifyClientCertIfGiven
	default:
		tlsCfg.ClientAuth = tls.NoClientCert
	}

	return credentials.NewTLS(tlsCfg), mtlsEnabled, nil
}

func newAuthInterceptors(auth network.Authenticator) (grpc.UnaryServerInterceptor, grpc.StreamServerInterceptor) {
	unary := func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		if auth != nil {
			if err := auth.Authorize(ctx); err != nil {
				return nil, err
			}
		}
		return handler(ctx, req)
	}
	stream := func(srv interface{}, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if auth != nil {
			if err := auth.Authorize(ss.Context()); err != nil {
				return err
			}
		}
		return handler(srv, ss)
	}
	return unary, stream
}
