package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
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

	"nhbchain/crypto"
	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	govv1 "nhbchain/proto/gov/v1"
	cons "nhbchain/sdk/consensus"
	"nhbchain/services/governd/config"
	"nhbchain/services/governd/server"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "services/governd/config.yaml", "path to governd config")
	flag.Parse()

	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("governd", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "governd",
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
	keyBytes, err := hex.DecodeString(strings.TrimSpace(cfg.SignerKey))
	if err != nil {
		log.Fatalf("decode signer key: %v", err)
	}
	signer, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		log.Fatalf("load signer key: %v", err)
	}

	dialOpts, err := consensusDialOptions(cfg.ConsensusClient)
	if err != nil {
		log.Fatalf("configure consensus client: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	client, err := cons.Dial(ctx, cfg.ConsensusEndpoint, dialOpts...)
	cancel()
	if err != nil {
		log.Fatalf("dial consensus: %v", err)
	}
	defer func() { _ = client.Close() }()

	service := server.New(client, signer, cfg)

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.ListenAddress, err)
	}
	creds, err := loadServerCredentials(cfg.TLS)
	if err != nil {
		log.Fatalf("load tls credentials: %v", err)
	}
	unaryAuth, streamAuth := server.NewAuthInterceptors(cfg.Auth)
	grpcServer := grpc.NewServer(
		grpc.Creds(creds),
		grpc.ChainUnaryInterceptor(
			otelgrpc.UnaryServerInterceptor(),
			unaryAuth,
		),
		grpc.ChainStreamInterceptor(
			otelgrpc.StreamServerInterceptor(),
			streamAuth,
		),
	)
	govv1.RegisterQueryServer(grpcServer, service)
	govv1.RegisterMsgServer(grpcServer, service)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("governd listening on %s", cfg.ListenAddress)
		serverErr <- grpcServer.Serve(listener)
	}()

	select {
	case <-rootCtx.Done():
		log.Println("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-shutdownCtx.Done():
			log.Println("forcing shutdown")
			grpcServer.Stop()
		}
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("serve gRPC: %v", err)
		}
	}
}

func loadServerCredentials(cfg config.TLSConfig) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(cfg.CertPath, cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("load server key pair: %w", err)
	}
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{cert},
	}
	if cfg.ClientCAPath != "" {
		pem, err := os.ReadFile(cfg.ClientCAPath)
		if err != nil {
			return nil, fmt.Errorf("read client ca: %w", err)
		}
		pool := x509.NewCertPool()
		if ok := pool.AppendCertsFromPEM(pem); !ok {
			return nil, fmt.Errorf("parse client ca: invalid pem data")
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	} else {
		tlsCfg.ClientAuth = tls.NoClientCert
	}
	return credentials.NewTLS(tlsCfg), nil
}
