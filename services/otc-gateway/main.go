package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	"nhbchain/services/otc-gateway/config"
	"nhbchain/services/otc-gateway/hsm"
	"nhbchain/services/otc-gateway/identity"
	"nhbchain/services/otc-gateway/models"
	"nhbchain/services/otc-gateway/recon"
	"nhbchain/services/otc-gateway/server"
	"nhbchain/services/otc-gateway/swaprpc"
)

func main() {
	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("otc-gateway", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "otc-gateway",
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

	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	db, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{})
	if err != nil {
		log.Fatalf("database connection error: %v", err)
	}

	if err := models.AutoMigrate(db); err != nil {
		log.Fatalf("auto migrate error: %v", err)
	}

	chainID, err := strconv.ParseUint(cfg.ChainID, 10, 64)
	if err != nil {
		log.Fatalf("invalid chain id %s: %v", cfg.ChainID, err)
	}

	signer, err := hsm.NewClient(hsm.Config{
		BaseURL:    cfg.HSMBaseURL,
		CACertPath: cfg.HSMCACert,
		ClientCert: cfg.HSMClientCert,
		ClientKey:  cfg.HSMClientKey,
		KeyLabel:   cfg.HSMKeyLabel,
		OverrideDN: cfg.HSMOverrideDN,
	})
	if err != nil {
		log.Fatalf("hsm client error: %v", err)
	}

	identityClient, err := identity.NewClient(identity.Config{
		BaseURL: cfg.IdentityBaseURL,
		APIKey:  cfg.IdentityAPIKey,
		Timeout: cfg.IdentityTimeout,
	})
	if err != nil {
		log.Fatalf("identity client error: %v", err)
	}

	swapClient, err := swaprpc.NewClient(swaprpc.Config{
		URL:               cfg.SwapRPCBase,
		Provider:          cfg.SwapProvider,
		APIKey:            cfg.SwapAPIKey,
		APISecret:         cfg.SwapAPISecret,
		AllowedMethods:    cfg.SwapMethodAllow,
		RequestsPerMinute: cfg.SwapRateLimit,
	})
	if err != nil {
		log.Fatalf("swap client error: %v", err)
	}

	srv := server.New(server.Config{
		DB:                db,
		TZ:                cfg.DefaultTZ,
		ChainID:           chainID,
		S3Bucket:          cfg.S3Bucket,
		SwapClient:        swapClient,
		Identity:          identityClient,
		Signer:            signer,
		VoucherTTL:        cfg.VoucherTTL,
		Provider:          cfg.SwapProvider,
		PollInterval:      cfg.MintPollInterval,
		RootAdminSubjects: cfg.RootAdminSubjects,
	})

	reconciler, err := recon.NewReconciler(recon.Config{
		DB:        db,
		TZ:        cfg.DefaultTZ,
		Exporter:  swapClient,
		OutputDir: cfg.ReconOutputDir,
		DryRun:    cfg.ReconDryRun,
	})
	if err != nil {
		log.Fatalf("reconciler init error: %v", err)
	}
	scheduler := recon.NewScheduler(recon.SchedulerConfig{
		Reconciler: reconciler,
		Window:     cfg.ReconWindow,
		RunHour:    cfg.ReconRunHour,
		RunMinute:  cfg.ReconRunMinute,
		Location:   cfg.DefaultTZ,
	})
	go scheduler.Start(context.Background())

	handler := otelhttp.NewHandler(srv.Handler(), "otc-gateway")

	addr := ":" + cfg.Port
	log.Printf("starting otc-gateway on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
