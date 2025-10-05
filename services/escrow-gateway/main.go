package main

import (
	"context"
	"fmt"
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

func main() {
	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("escrow-gateway", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "escrow-gateway",
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

	auth := NewAuthenticator(cfg.APIKeys, cfg.AllowedTimestampSkew, cfg.NonceTTL, cfg.NonceCapacity, nil)
	node := NewRPCNodeClient(cfg.NodeURL, cfg.NodeAuthToken)
	queue := NewWebhookQueue(
		WithWebhookTaskCapacity(cfg.WebhookQueueCapacity),
		WithWebhookHistoryCapacity(cfg.WebhookHistorySize),
		WithWebhookTTL(cfg.WebhookQueueTTL),
	)
	intents := NewPayIntentBuilder()
	server := NewServer(auth, node, store, queue, intents)

	srv := &http.Server{
		Addr:    cfg.ListenAddress,
		Handler: otelhttp.NewHandler(server, "escrow-gateway"),
	}

	go func() {
		log.Printf("escrow gateway listening on %s", cfg.ListenAddress)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Printf("shutting down escrow gateway")
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "graceful shutdown failed: %v\n", err)
	}
}

const shutdownTimeout = 10 * time.Second
