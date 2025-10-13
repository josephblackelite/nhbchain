package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	lendingv1 "nhbchain/proto/lending/v1"
	"nhbchain/services/lending/engine"
	"nhbchain/services/lending/engine/rpcclient"
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
	flag.StringVar(&cfg.NodeRPCToken, "node-rpc-token", cfg.NodeRPCToken, "JWT or bearer token for node RPC requests")
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
	logger := logging.Setup("lendingd", env)
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

	logger.Info("effective config", slog.Any("config", cfg.Sanitized()))

	rpcClient, err := rpcclient.NewClient(rpcclient.Config{
		BaseURL:            cfg.NodeRPCURL,
		BearerToken:        cfg.NodeRPCToken,
		SharedSecretHeader: cfg.SharedSecretHeader,
		SharedSecretValue:  cfg.SharedSecretValue,
		TLSClientCAFile:    cfg.TLSClientCAFile,
		AllowInsecure:      cfg.AllowInsecure,
	})
	if err != nil {
		log.Fatalf("create rpc client: %v", err)
	}
	eng := engine.NewNodeAdapter(rpcClient)

	srvCfg := lendingserver.Config{
		TLSCertFile:      cfg.TLSCertFile,
		TLSKeyFile:       cfg.TLSKeyFile,
		TLSClientCAFile:  cfg.TLSClientCAFile,
		AllowInsecure:    cfg.AllowInsecure,
		MTLSRequired:     cfg.MTLSRequired,
		AllowedClientCNs: cfg.AllowedClientCNs,
		RateLimitPerMin:  cfg.RateLimitPerMin,
		Logger:           logger,
	}
	if token := strings.TrimSpace(cfg.SharedSecretValue); token != "" {
		srvCfg.APITokens = append(srvCfg.APITokens, token)
	}
	if os.Getenv("LEND_API_TOKEN") == "" && len(srvCfg.APITokens) == 0 && !srvCfg.MTLSRequired && len(srvCfg.AllowedClientCNs) == 0 {
		log.Fatalf("lendingd requires an API token or mTLS configuration for authentication")
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.Listen, err)
	}

	options, err := lendingserver.Interceptors(srvCfg)
	if err != nil {
		log.Fatalf("build grpc interceptors: %v", err)
	}
	if cfg.AllowInsecure {
		logger.Warn("DEV ONLY: starting without TLS credentials")
	} else {
		credsOpt, err := lendingserver.GrpcServerCreds(srvCfg)
		if err != nil {
			log.Fatalf("configure tls: %v", err)
		}
		if credsOpt != nil {
			options = append(options, credsOpt)
		}
	}

	options = append(options,
		grpc.ChainUnaryInterceptor(otelgrpc.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(otelgrpc.StreamServerInterceptor()),
	)

	grpcServer := grpc.NewServer(options...)
	service := lendingserver.New(eng, logger, lendingserver.NewInterceptorAuthorizer())
	lendingv1.RegisterLendingServiceServer(grpcServer, service)

	healthServer, _ := startHealthServer(logger, rpcClient)
	defer func() {
		if healthServer != nil {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = healthServer.Shutdown(shutdownCtx)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("lendingd listening", slog.String("listen", cfg.Listen), slog.Bool("tls_enabled", !cfg.AllowInsecure), slog.Bool("mtls_required", cfg.MTLSRequired))
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
			if healthServer != nil {
				_ = healthServer.Shutdown(shutdownCtx)
			}
		case <-shutdownCtx.Done():
			log.Println("forcing server stop")
			grpcServer.Stop()
			if healthServer != nil {
				_ = healthServer.Shutdown(context.Background())
			}
		}
	case err := <-serverErr:
		if err != nil {
			if healthServer != nil {
				_ = healthServer.Shutdown(context.Background())
			}
			log.Fatalf("serve gRPC: %v", err)
		}
	}
}

func startHealthServer(logger *slog.Logger, cli *rpcclient.Client) (*http.Server, net.Listener) {
	if logger == nil {
		logger = slog.Default()
	}
	if cli == nil {
		return nil, nil
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		logger.Warn("failed to start health listener", "error", err)
		return nil, nil
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()

		var height any
		if err := cli.Call(ctx, "nhb_getHeight", []any{}, &height); err != nil {
			logger.Error("health check failed", "error", err)
			http.Error(w, "unhealthy", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{Handler: mux}
	go func() {
		logger.Info("health check server listening", slog.String("addr", listener.Addr().String()))
		if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("health server stopped unexpectedly", "error", err)
		}
	}()

	return srv, listener
}
