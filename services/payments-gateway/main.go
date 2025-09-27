package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
)

const shutdownTimeout = 10 * time.Second

func main() {
	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("payments-gateway", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "payments-gateway",
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

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	store, err := NewSQLiteStore(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	oracle := NewOracle(cfg.OracleTTL, cfg.OracleMaxDeviation, cfg.OracleCircuitBreaker)
	signer, err := NewEnvKMSSigner(cfg.MinterKMSEnv)
	if err != nil {
		log.Fatalf("configure kms signer: %v", err)
	}
	nowClient := NewHTTPNowPaymentsClient(cfg.NowPaymentsBaseURL, cfg.NowPaymentsAPIKey)
	nodeClient := NewRPCNodeClient(cfg.NodeURL, cfg.NodeAuthToken)

	server := NewServer(store, oracle, nowClient, nodeClient, signer, cfg.QuoteTTL, cfg.QuoteCurrency, cfg.NowPaymentsIPNSecret)
	srv := &http.Server{Addr: cfg.ListenAddress, Handler: otelhttp.NewHandler(server, "payments-gateway")}

	go func() {
		log.Printf("payments gateway listening on %s", cfg.ListenAddress)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Printf("shutting down payments gateway")
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
