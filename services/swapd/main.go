package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	"nhbchain/services/swapd/adapters"
	"nhbchain/services/swapd/config"
	"nhbchain/services/swapd/oracle"
	"nhbchain/services/swapd/server"
	"nhbchain/services/swapd/stable"
	"nhbchain/services/swapd/storage"
)

func main() {
	var (
		cfgPath                       string
		allowInsecureBearerWithoutTLS bool
	)
	flag.StringVar(&cfgPath, "config", "services/swapd/config.yaml", "path to swapd configuration file")
	flag.BoolVar(&allowInsecureBearerWithoutTLS, "allow-insecure-bearer-without-tls", false, "allow admin bearer authentication without TLS (dev only)")
	flag.Parse()

	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("swapd", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "swapd",
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

	var loadOptions []config.Option
	if allowInsecureBearerWithoutTLS {
		if env != "dev" {
			log.Fatalf("swapd: --allow-insecure-bearer-without-tls requires NHB_ENV=dev")
		}
		log.Printf("swapd: WARNING: allowing admin bearer token without TLS (development override)")
		loadOptions = append(loadOptions, config.WithAllowInsecureBearerWithoutTLS())
	}

	cfg, err := config.Load(cfgPath, loadOptions...)
	if err != nil {
		log.Fatalf("swapd: load config: %v", err)
	}

	dsn, err := storage.FileDSN(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("swapd: resolve storage DSN: %v", err)
	}
	store, err := storage.Open(dsn)
	if err != nil {
		log.Fatalf("swapd: open storage: %v", err)
	}
	defer store.Close()

	ctx := context.Background()
	policy := storage.Policy{
		ID:          cfg.Policy.ID,
		MintLimit:   cfg.Policy.MintLimit,
		RedeemLimit: cfg.Policy.RedeemLimit,
		Window:      cfg.Policy.Window.Duration,
	}
	if policy.Window <= 0 {
		policy.Window = time.Hour
	}
	if err := store.SavePolicy(ctx, policy); err != nil {
		log.Printf("swapd: save policy: %v", err)
	}

	registry := adapters.NewRegistry()
	sources := make([]oracle.Source, 0, len(cfg.Sources))
	for _, src := range cfg.Sources {
		built, err := registry.Build(src.Name, src.Type, src.Endpoint, src.APIKey, src.Assets)
		if err != nil {
			log.Fatalf("swapd: build source %s: %v", src.Name, err)
		}
		sources = append(sources, built)
	}

	pairs := make([]oracle.Pair, 0, len(cfg.Pairs))
	for _, pair := range cfg.Pairs {
		pairs = append(pairs, oracle.Pair{Base: pair.Base, Quote: pair.Quote})
	}

	mgr, err := oracle.New(store, sources, pairs, cfg.Oracle.Interval.Duration, cfg.Oracle.MaxAge.Duration, cfg.Oracle.MinFeeds)
	if err != nil {
		log.Fatalf("swapd: oracle manager: %v", err)
	}

	stableRuntime := server.StableRuntime{}
	if !cfg.Stable.Paused {
		defaultTTL := cfg.Stable.QuoteTTL.Duration
		if defaultTTL <= 0 {
			defaultTTL = time.Minute
		}
		defaultSlippage := cfg.Stable.MaxSlippage
		if defaultSlippage <= 0 {
			defaultSlippage = 50
		}
		defaultInventory := cfg.Stable.SoftInventory
		if defaultInventory <= 0 {
			defaultInventory = 1_000_000
		}
		assets := make([]stable.Asset, 0, len(cfg.Stable.Assets))
		for _, asset := range cfg.Stable.Assets {
			ttl := asset.QuoteTTL.Duration
			if ttl <= 0 {
				ttl = defaultTTL
			}
			slippage := asset.MaxSlippage
			if slippage <= 0 {
				slippage = defaultSlippage
			}
			inventory := asset.SoftInventory
			if inventory <= 0 {
				inventory = defaultInventory
			}
			assets = append(assets, stable.Asset{
				Symbol:         strings.ToUpper(strings.TrimSpace(asset.Symbol)),
				BasePair:       strings.TrimSpace(asset.BasePair),
				QuotePair:      strings.TrimSpace(asset.QuotePair),
				QuoteTTL:       ttl,
				MaxSlippageBps: slippage,
				SoftInventory:  inventory,
			})
		}
		limits := stable.Limits{DailyCap: int64(cfg.Policy.MintLimit)}
		engine, err := stable.NewEngine(assets, limits)
		if err != nil {
			log.Fatalf("swapd: stable engine: %v", err)
		}
		engine.WithDailyUsageStore(store)
		stableRuntime = server.StableRuntime{
			Enabled: true,
			Engine:  engine,
			Limits:  limits,
			Assets:  assets,
		}
	}

	authConfig := server.AuthConfig{
		BearerToken: cfg.Admin.BearerToken,
		AllowMTLS:   cfg.Admin.MTLS.Enabled,
	}
	authenticator, err := server.NewAuthenticator(authConfig)
	if err != nil {
		log.Fatalf("swapd: configure admin auth: %v", err)
	}

	if stableRuntime.Enabled && cfg.Admin.TLS.Disable {
		log.Fatalf("swapd: stable runtime requires admin TLS to be enabled")
	}

	var tlsConfig *tls.Config
	if !cfg.Admin.TLS.Disable {
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
		if cfg.Admin.MTLS.Enabled {
			caPath := strings.TrimSpace(cfg.Admin.MTLS.ClientCAPath)
			if caPath == "" {
				log.Fatalf("swapd: admin mTLS requires client_ca to be configured")
			}
			caData, err := os.ReadFile(caPath)
			if err != nil {
				log.Fatalf("swapd: load admin client CA: %v", err)
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(caData) {
				log.Fatalf("swapd: parse admin client CA: %s", caPath)
			}
			tlsConfig.ClientCAs = pool
			tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		}
	}

	srv, err := server.New(server.Config{
		ListenAddress: cfg.ListenAddress,
		PolicyID:      policy.ID,
		TLS: server.TLSConfig{
			Disabled: cfg.Admin.TLS.Disable,
			CertFile: cfg.Admin.TLS.CertPath,
			KeyFile:  cfg.Admin.TLS.KeyPath,
			Config:   tlsConfig,
		},
	}, store, log.Default(), stableRuntime, authenticator)
	if err != nil {
		log.Fatalf("swapd: server: %v", err)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		if err := mgr.Run(rootCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("swapd: oracle manager exited: %v", err)
			stop()
		}
	}()

	if err := srv.Run(rootCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("swapd: http server error: %v", err)
		os.Exit(1)
	}
}
