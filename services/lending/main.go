package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"nhbchain/network"
	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	lendingv1 "nhbchain/proto/lending/v1"
	lendingserver "nhbchain/services/lending/server"
)

type stringListFlag struct {
	values []string
}

func newStringListFlag(initial []string) *stringListFlag {
	filtered := make([]string, 0, len(initial))
	for _, value := range initial {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			filtered = append(filtered, trimmed)
		}
	}
	return &stringListFlag{values: filtered}
}

func (f *stringListFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(f.values, ",")
}

func (f *stringListFlag) Set(value string) error {
	if f == nil {
		return nil
	}
	trimmed := strings.TrimSpace(value)
	if trimmed != "" {
		f.values = append(f.values, trimmed)
	}
	return nil
}

func (f *stringListFlag) Values() []string {
	if f == nil {
		return nil
	}
	out := make([]string, len(f.values))
	copy(out, f.values)
	return out
}

func main() {
	cfg := LoadConfigFromEnv()

	flag.StringVar(&cfg.NodeRPCURL, "node-rpc-url", cfg.NodeRPCURL, "URL for the node RPC endpoint")
	flag.StringVar(&cfg.NodeRPCToken, "node-rpc-token", cfg.NodeRPCToken, "bearer token for node RPC requests")
	flag.StringVar(&cfg.SharedSecretHeader, "shared-secret-header", cfg.SharedSecretHeader, "metadata header carrying the shared secret")
	flag.StringVar(&cfg.SharedSecretValue, "shared-secret", cfg.SharedSecretValue, "shared secret required for token authentication")
	flag.StringVar(&cfg.TLSCertFile, "tls-cert", cfg.TLSCertFile, "path to the TLS certificate for lendingd")
	flag.StringVar(&cfg.TLSKeyFile, "tls-key", cfg.TLSKeyFile, "path to the TLS private key for lendingd")
	flag.StringVar(&cfg.TLSClientCAFile, "tls-client-ca", cfg.TLSClientCAFile, "path to the client CA bundle for mTLS")
	flag.BoolVar(&cfg.AllowInsecure, "allow-insecure", cfg.AllowInsecure, "allow lendingd to listen without TLS (development only)")
	flag.StringVar(&cfg.Listen, "listen", cfg.Listen, "address for lendingd to listen on")
	flag.IntVar(&cfg.RateLimitPerMin, "rate-limit-per-min", cfg.RateLimitPerMin, "maximum number of requests per minute")
	flag.BoolVar(&cfg.MTLSRequired, "mtls-required", cfg.MTLSRequired, "require mutual TLS for authentication")

	allowedCNFlag := newStringListFlag(cfg.AllowedClientCNs)
	flag.Var(allowedCNFlag, "mtls-allowed-cn", "allowed client certificate common name (repeatable)")

	flag.Parse()

	cfg.AllowedClientCNs = allowedCNFlag.Values()

	if err := cfg.Validate(); err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("lendingd", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "lendingd",
		Environment: env,
		Endpoint:    otlpEndpoint,
		Insecure:    insecure,
		Headers:     otlpHeaders,
		Metrics:     true,
		Traces:      true,
	})
	if err != nil {
		log.Fatalf("init telemetry: %v", err)
	}
	defer func() {
		if shutdownTelemetry != nil {
			_ = shutdownTelemetry(context.Background())
		}
	}()

	log.Printf("effective config: %+v", cfg.Sanitized())

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.Listen, err)
	}
	creds, mtlsEnabled, err := buildServerCredentials(cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSClientCAFile, cfg.AllowInsecure)
	if err != nil {
		log.Fatalf("configure tls: %v", err)
	}

	allowedCommonNames := cfg.AllowedClientCNs
	if len(allowedCommonNames) > 0 && !mtlsEnabled {
		log.Fatalf("mTLS allowed common names require a client CA bundle")
	}
	if cfg.MTLSRequired && !mtlsEnabled {
		log.Fatalf("mTLS is required but no client CA was provided")
	}

	var authenticators []network.Authenticator
	if auth := network.NewTokenAuthenticator(cfg.SharedSecretHeader, cfg.SharedSecretValue); auth != nil {
		authenticators = append(authenticators, auth)
	}
	if mtlsEnabled {
		authenticators = append(authenticators, network.NewTLSAuthorizer(allowedCommonNames))
	}
	if len(authenticators) == 0 {
		log.Fatalf("lendingd requires a shared secret or mTLS configuration for authentication")
	}
	authChain := network.ChainAuthenticators(authenticators...)
	unaryAuth, streamAuth := newAuthInterceptors(authChain)

	grpcServer := grpc.NewServer(
		grpc.Creds(creds),
		grpc.ChainUnaryInterceptor(
			unaryAuth,
			otelgrpc.UnaryServerInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			streamAuth,
			otelgrpc.StreamServerInterceptor(),
		),
	)
	service := lendingserver.New(nil, nil, authChain)
	lendingv1.RegisterLendingServiceServer(grpcServer, service)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("lendingd listening on %s", cfg.Listen)
		serverErr <- grpcServer.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		log.Println("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-shutdownCtx.Done():
			log.Println("forcing server stop")
			grpcServer.Stop()
		}
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("serve gRPC: %v", err)
		}
	}
}

func buildServerCredentials(certPath, keyPath, clientCAPath string, allowInsecure bool) (credentials.TransportCredentials, bool, error) {
	certPath = strings.TrimSpace(certPath)
	keyPath = strings.TrimSpace(keyPath)
	clientCAPath = strings.TrimSpace(clientCAPath)

	hasCert := certPath != ""
	hasKey := keyPath != ""
	if hasCert != hasKey {
		return nil, false, fmt.Errorf("tls requires both certificate and key")
	}
	switch {
	case hasCert && hasKey:
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, false, fmt.Errorf("load tls keypair: %w", err)
		}
		tlsCfg := &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
		mtlsEnabled := false
		if clientCAPath != "" {
			pem, err := os.ReadFile(clientCAPath)
			if err != nil {
				return nil, false, fmt.Errorf("read client ca: %w", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, false, fmt.Errorf("parse client ca: invalid pem data")
			}
			tlsCfg.ClientCAs = pool
			tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
			mtlsEnabled = true
		} else {
			tlsCfg.ClientAuth = tls.NoClientCert
		}
		return credentials.NewTLS(tlsCfg), mtlsEnabled, nil
	case allowInsecure:
		return insecure.NewCredentials(), false, nil
	default:
		return nil, false, fmt.Errorf("tls certificate and key are required unless --allow-insecure is set")
	}
}
