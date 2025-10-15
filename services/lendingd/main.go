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

	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	lendingv1 "nhbchain/proto/lending/v1"
	lendingserver "nhbchain/services/lending/server"
	"nhbchain/services/lendingd/config"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "services/lending/config.yaml", "path to lendingd config")
	flag.Parse()

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

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.ListenAddress, err)
	}
	if cfg.TLS.AllowInsecure {
		tcpAddr, _ := listener.Addr().(*net.TCPAddr)
		loopback := tcpAddr != nil && tcpAddr.IP != nil && tcpAddr.IP.IsLoopback()
		if !strings.EqualFold(env, "dev") && !loopback {
			log.Fatalf("plaintext lendingd mode is restricted to loopback listeners or dev environment")
		}
	}
	creds, err := loadServerCredentials(cfg.TLS)
	if err != nil {
		log.Fatalf("configure tls: %v", err)
	}
	authCfg := lendingserver.AuthConfig{
		APITokens:        cfg.Auth.APITokens,
		AllowedClientCNs: cfg.Auth.MTLS.AllowedCommonNames,
		MTLSRequired:     cfg.TLS.MTLSEnabled(),
	}
	unaryAuth, streamAuth := lendingserver.NewAuthInterceptors(authCfg)

	unaryChain := grpc.ChainUnaryInterceptor(
		otelgrpc.UnaryServerInterceptor(),
		unaryAuth,
	)
	streamChain := grpc.ChainStreamInterceptor(
		otelgrpc.StreamServerInterceptor(),
		streamAuth,
	)
	options := []grpc.ServerOption{unaryChain, streamChain}
	if creds != nil {
		options = append(options, grpc.Creds(creds))
	}
	grpcServer := grpc.NewServer(options...)

	service := lendingserver.New(nil, nil, nil)
	lendingv1.RegisterLendingServiceServer(grpcServer, service)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("lendingd listening on %s", cfg.ListenAddress)
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

func loadServerCredentials(cfg config.TLSConfig) (credentials.TransportCredentials, error) {
	if cfg.CertPath == "" || cfg.KeyPath == "" {
		if cfg.AllowInsecure {
			return nil, nil
		}
		return nil, fmt.Errorf("tls credentials are required")
	}
	cert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("load tls keypair: %w", err)
	}
	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}
	if cfg.ClientCAPath != "" {
		pem, err := os.ReadFile(cfg.ClientCAPath)
		if err != nil {
			return nil, fmt.Errorf("read client ca: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("parse client ca: invalid pem data")
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		tlsCfg.ClientAuth = tls.NoClientCert
	}
	return credentials.NewTLS(tlsCfg), nil
}
