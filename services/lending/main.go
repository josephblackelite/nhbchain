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
	"gopkg.in/yaml.v3"

	"nhbchain/network"
	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	lendingv1 "nhbchain/proto/lending/v1"
	lendingserver "nhbchain/services/lending/server"
)

const (
	tlsCertEnv       = "LENDINGD_TLS_CERT"
	tlsKeyEnv        = "LENDINGD_TLS_KEY"
	tlsClientCAEnv   = "LENDINGD_TLS_CLIENT_CA"
	tlsAllowInsecure = "LENDINGD_TLS_ALLOW_INSECURE"
	authSecretEnv    = "LENDINGD_AUTH_SHARED_SECRET"
	authHeaderEnv    = "LENDINGD_AUTH_HEADER"
	authAllowedCNEnv = "LENDINGD_AUTH_ALLOWED_CNS"
)

type Config struct {
	ListenAddress string `yaml:"listen"`
}

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

func loadConfig(path string) (Config, error) {
	cfg := Config{ListenAddress: ":50053"}
	if path == "" {
		return cfg, fmt.Errorf("config path required")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = ":50053"
	}
	return cfg, nil
}

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "services/lending/config.yaml", "path to lendingd config")

	certDefault := stringFromEnv(tlsCertEnv, "")
	keyDefault := stringFromEnv(tlsKeyEnv, "")
	clientCADefault := stringFromEnv(tlsClientCAEnv, "")
	allowInsecureDefault := boolFromEnv(tlsAllowInsecure, false)
	sharedSecretDefault := stringFromEnv(authSecretEnv, "")
	headerDefault := stringFromEnv(authHeaderEnv, "authorization")
	allowedCNDefault := strings.Split(stringFromEnv(authAllowedCNEnv, ""), ",")

	var tlsCertPath, tlsKeyPath, tlsClientCAPath string
	flag.StringVar(&tlsCertPath, "tls-cert", certDefault, "path to the TLS certificate for lendingd")
	flag.StringVar(&tlsKeyPath, "tls-key", keyDefault, "path to the TLS private key for lendingd")
	flag.StringVar(&tlsClientCAPath, "tls-client-ca", clientCADefault, "path to the client CA bundle for mTLS")

	var allowInsecure bool
	flag.BoolVar(&allowInsecure, "tls-allow-insecure", allowInsecureDefault, "allow lendingd to listen without TLS (development only)")

	var sharedSecret, authHeader string
	flag.StringVar(&sharedSecret, "auth-shared-secret", sharedSecretDefault, "shared secret required for token authentication")
	flag.StringVar(&authHeader, "auth-header", headerDefault, "metadata header carrying the shared secret token")

	allowedCNFlag := newStringListFlag(allowedCNDefault)
	flag.Var(allowedCNFlag, "mtls-allowed-cn", "allowed client certificate common name (repeatable)")

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

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.ListenAddress, err)
	}
	creds, mtlsEnabled, err := buildServerCredentials(tlsCertPath, tlsKeyPath, tlsClientCAPath, allowInsecure)
	if err != nil {
		log.Fatalf("configure tls: %v", err)
	}

	allowedCommonNames := allowedCNFlag.Values()
	if len(allowedCommonNames) > 0 && !mtlsEnabled {
		log.Fatalf("mTLS allowed common names require a client CA bundle")
	}

	var authenticators []network.Authenticator
	if auth := network.NewTokenAuthenticator(authHeader, sharedSecret); auth != nil {
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
	service := lendingserver.New()
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
		return nil, false, fmt.Errorf("tls certificate and key are required unless --tls-allow-insecure is set")
	}
}

func stringFromEnv(key, fallback string) string {
	trimmed := strings.TrimSpace(os.Getenv(key))
	if trimmed == "" {
		return fallback
	}
	return trimmed
}

func boolFromEnv(key string, fallback bool) bool {
	trimmed := strings.TrimSpace(os.Getenv(key))
	if trimmed == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(trimmed)
	if err != nil {
		log.Printf("invalid boolean value for %s: %q, using default %v", key, trimmed, fallback)
		return fallback
	}
	return parsed
}
