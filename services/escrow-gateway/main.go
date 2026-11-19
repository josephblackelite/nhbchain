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
	// The queue holds the escrow.created events the server enqueues; nothing
	// delivered them until this worker was started. It delivers to the rows an
	// operator has put in the webhooks table (there is no endpoint to add one),
	// and only for the events the server itself raises: the gateway has no feed of
	// chain events, so the funded/released/refunded/... events are not produced.
	workerCtx, stopWorker := context.WithCancel(context.Background())
	defer stopWorker()
	go NewWebhookWorker(store, queue).Run(workerCtx)
	intents := NewPayIntentBuilder()
	server := NewServer(auth, node, store, queue, intents, cfg.MerchantConfigs)

	shutdownAuditPruner := startAuditPruner(store, cfg.UnauthenticatedAuditRetention, unauthenticatedAuditPruneEvery, time.Now)
	defer shutdownAuditPruner()

	srv := newHTTPServer(cfg.ListenAddress, otelhttp.NewHandler(server, "escrow-gateway"))

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

// The limits the gateway's HTTP server puts on a connection. Without them a
// client could hold a connection open indefinitely by sending a request slowly,
// and a request line and headers were bounded only by net/http's 1 MiB default.
// WriteTimeout runs from the end of the request headers, so it covers reading
// the body and the longest call to the node (20 seconds) as well as the reply.
const (
	httpReadHeaderTimeout = 10 * time.Second
	httpReadTimeout       = 30 * time.Second
	httpWriteTimeout      = 60 * time.Second
	httpIdleTimeout       = 60 * time.Second
	httpMaxHeaderBytes    = 16 << 10
)

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ReadTimeout:       httpReadTimeout,
		WriteTimeout:      httpWriteTimeout,
		IdleTimeout:       httpIdleTimeout,
		MaxHeaderBytes:    httpMaxHeaderBytes,
	}
}

// startAuditPruner deletes the audit rows of failed authentications older than
// retention, once now and then every interval, until the returned function is
// called. A retention of zero or less keeps them, and nothing is started.
func startAuditPruner(store *SQLiteStore, retention, interval time.Duration, nowFn func() time.Time) func() {
	if store == nil || retention <= 0 {
		return func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	pruneOnce := func() {
		pruneCtx, pruneCancel := context.WithTimeout(ctx, 30*time.Second)
		defer pruneCancel()
		removed, err := store.PruneUnauthenticatedAudit(pruneCtx, nowFn().Add(-retention))
		if err != nil {
			slog.Warn("escrow gateway audit prune failed", "error", err.Error())
			return
		}
		if removed > 0 {
			slog.Info("escrow gateway pruned audit rows of failed authentications", "removed", removed, "retention", retention.String())
		}
	}

	go func() {
		defer close(done)
		pruneOnce()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				pruneOnce()
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

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
