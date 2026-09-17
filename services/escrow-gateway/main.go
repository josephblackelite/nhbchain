package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"math/big"
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
	relayerKey, err := LoadPrivateKeyFromEnv(cfg.RelayerKMSEnv)
	if err != nil {
		log.Fatalf("configure relayer key: %v", err)
	}
	if err := node.InitRelayer(context.Background(), relayerKey); err != nil {
		log.Fatalf("init relayer: %v", err)
	}
	log.Printf("escrow gateway relayer address: %s", node.RelayerAddress())

	shutdownBalanceMonitor := startRelayerBalanceMonitor(node, node.RelayerAddress(), cfg.RelayerMinBalanceWei, cfg.RelayerBalanceCheckInterval)
	defer shutdownBalanceMonitor()
	queue := NewWebhookQueue(
		WithWebhookTaskCapacity(cfg.WebhookQueueCapacity),
		WithWebhookHistoryCapacity(cfg.WebhookHistorySize),
		WithWebhookTTL(cfg.WebhookQueueTTL),
	)
	intents := NewPayIntentBuilder()
	server := NewServer(auth, node, store, queue, intents, cfg.MerchantConfigs)

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

// startRelayerBalanceMonitor checks the relayer's own NHB balance every
// interval and logs a WARN when it's at or below minBalanceWei -- see
// Config.RelayerMinBalanceWei's doc comment for why this exists and why
// it's a log line rather than a Slack/PagerDuty call. Runs one check
// immediately (so a chronically-empty relayer is visible in the log from
// the very first minute, not just after the first interval elapses) before
// starting the ticker. Returns a function that stops the monitor; safe to
// call multiple times.
func startRelayerBalanceMonitor(node NodeClient, relayerAddress string, minBalanceWei *big.Int, interval time.Duration) func() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	checkOnce := func() {
		balanceCtx, balanceCancel := context.WithTimeout(ctx, 10*time.Second)
		defer balanceCancel()
		balance, err := node.RelayerBalance(balanceCtx)
		if err != nil {
			slog.Warn("escrow gateway relayer balance check failed", "error", err.Error())
			return
		}
		if minBalanceWei != nil && balance.Cmp(minBalanceWei) <= 0 {
			slog.Warn("escrow gateway relayer balance is low",
				"balanceWei", balance.String(),
				"minBalanceWei", minBalanceWei.String(),
				"address", relayerAddress,
			)
		}
	}

	go func() {
		defer close(done)
		checkOnce()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				checkOnce()
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}
