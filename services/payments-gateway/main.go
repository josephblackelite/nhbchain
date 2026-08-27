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
	"nhbchain/services/swapd/settlement"
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

	// Acquire the single-instance lock BEFORE opening the SQLite database --
	// see AcquireInstanceLock's doc comment for the double-payout risk this
	// guards against if two processes (e.g. an overlapping deploy) ever ran
	// against the same database concurrently.
	releaseInstanceLock, err := AcquireInstanceLock(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("payments-gateway: %v", err)
	}
	defer releaseInstanceLock()

	store, err := NewSQLiteStore(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()
	if err := store.initRedemptionTables(); err != nil {
		log.Fatalf("init redemption tables: %v", err)
	}

	oracle := NewOracle(cfg.OracleTTL, cfg.OracleMaxDeviation, cfg.OracleCircuitBreaker)
	signer, err := NewEnvKMSSigner(cfg.MinterKMSEnv)
	if err != nil {
		log.Fatalf("configure kms signer: %v", err)
	}
	nowClient := NewHTTPNowPaymentsClient(cfg.NowPaymentsBaseURL, cfg.NowPaymentsAPIKey)
	nodeClient := NewRPCNodeClient(cfg.NodeURL, cfg.NodeAuthToken)

	server := NewServer(store, oracle, nowClient, nodeClient, signer, cfg.QuoteTTL, cfg.QuoteCurrency, cfg.DefaultMintAsset, cfg.ServiceFeeBps, cfg.NowPaymentsIPNSecret, cfg.PublicIPNCallbackURL)
	server.SetAdminToken(cfg.AdminToken)

	// Redemption (Swap Out) payout side: only wired once every credential it
	// needs is present -- see Config.RedemptionEnabled's doc comment for why
	// this is optional rather than a hard startup requirement. A deployment
	// still waiting on the chain-side rollout (swap_listPendingRedemptions,
	// the RoleSwapPayoutAttestor governance grant) keeps serving the
	// existing mint/deposit flows normally.
	var redeemWatcher *RedeemWatcher
	if cfg.RedemptionEnabled() {
		attestorKey, err := LoadPrivateKeyFromEnv(cfg.AttestorKMSEnv)
		if err != nil {
			log.Fatalf("configure redemption attestor key: %v", err)
		}
		if err := nodeClient.InitAttestor(context.Background(), attestorKey); err != nil {
			log.Fatalf("init redemption attestor nonce: %v", err)
		}
		log.Printf("payments-gateway: redemption attestor address %s -- ensure it holds enough NHB to pay attestation gas; a starved attestor silently stalls every pending payout", nodeClient.AttestorAddress())

		payoutClient, err := settlement.NewHTTPPayoutClient(settlement.NowPaymentsConfig{
			Email:      cfg.PayoutNowPaymentsEmail,
			Password:   cfg.PayoutNowPaymentsPassword,
			APIKey:     cfg.PayoutNowPaymentsAPIKey,
			BaseURL:    cfg.PayoutNowPaymentsBaseURL,
			TOTPSecret: cfg.PayoutNowPaymentsTOTPSecret,
		})
		if err != nil {
			log.Fatalf("configure redemption payout client: %v", err)
		}
		// DefaultRail is always RailNowPayments and no PartnerRails override
		// is configured -- every redemption settlement uses the NOWPayments
		// mass-payout rail; manual_treasury is never reachable for this
		// feature.
		settlementMgr, err := settlement.NewManager(store, settlement.Config{DefaultRail: settlement.RailNowPayments}, payoutClient)
		if err != nil {
			log.Fatalf("configure redemption settlement manager: %v", err)
		}

		// payoutClient doubles as the redeem watcher's PayoutStatusChecker --
		// it's the same NOWPayments account/credentials either way, just a
		// different method on the same client (GetPayoutStatus vs
		// CreatePayout), so no separate client/config is needed.
		redeemWatcher = NewRedeemWatcher(store, nodeClient, settlementMgr, payoutClient, cfg.RedeemWatcherInterval)
		// The admin confirm/fail/retry-payout endpoints call into the
		// watcher itself (not settlementMgr directly) so every operator
		// action is serialized against the watcher's own tick -- see
		// RedeemWatcher.mu's doc comment.
		server.SetRedeemWatcher(redeemWatcher)
		server.SetRedemptionFeeEstimator(payoutClient)
		if err := redeemWatcher.Recover(context.Background()); err != nil {
			log.Fatalf("redeem watcher recovery scan: %v", err)
		}
	} else {
		log.Printf("payments-gateway: redemption (Swap Out) payout watcher disabled -- set %s and PAY_GATEWAY_PAYOUT_NOW_EMAIL/_PASSWORD/_API_KEY to enable it", envAttestorKMSEnv)
	}

	srv := &http.Server{Addr: cfg.ListenAddress, Handler: otelhttp.NewHandler(server, "payments-gateway")}

	reconcilerCtx, stopReconciler := context.WithCancel(context.Background())
	go server.runPaymentReconciler(reconcilerCtx)

	var stopRedeemWatcher context.CancelFunc
	if redeemWatcher != nil {
		var redeemCtx context.Context
		redeemCtx, stopRedeemWatcher = context.WithCancel(context.Background())
		go redeemWatcher.Run(redeemCtx)
	}

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
	stopReconciler()
	if stopRedeemWatcher != nil {
		stopRedeemWatcher()
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
