package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	"nhbchain/services/otc-gateway/hsm"
	"nhbchain/services/swapd/adapters"
	"nhbchain/services/swapd/config"
	"nhbchain/services/swapd/oracle"
	"nhbchain/services/swapd/priceproof"
	"nhbchain/services/swapd/server"
	"nhbchain/services/swapd/settlement"
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

	// The stable engine is built before the oracle manager, not after, so
	// its RecordPrice method can be wired in as the oracle's Publisher at
	// construction time (oracle.New has no way to attach one after the
	// fact -- WithPublisher is construction-time only). Previously these
	// were built independently with no link between them at all: the
	// oracle computed and stored real median prices every tick, but
	// oracle.New's default no-op publisher meant the stable engine never
	// received them, so PriceQuote/quote requests had no live price outside
	// of tests that call RecordPrice directly.
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
		engine, err := stable.NewEngine(assets, limits, store)
		if err != nil {
			log.Fatalf("swapd: stable engine: %v", err)
		}
		engine.WithDailyUsageStore(store)
		partners := make([]server.Partner, 0, len(cfg.Stable.Partners))
		for _, partner := range cfg.Stable.Partners {
			id := strings.TrimSpace(partner.ID)
			apiKey := strings.TrimSpace(partner.APIKey)
			secret := strings.TrimSpace(partner.Secret)
			if id == "" || apiKey == "" || secret == "" {
				log.Fatalf("swapd: stable partner configuration incomplete")
			}
			var dailyQuota int64
			if partner.Quota.Daily > 0 {
				units, err := server.ToStableAmountUnits(partner.Quota.Daily)
				if err != nil {
					log.Fatalf("swapd: parse partner quota for %s: %v", id, err)
				}
				dailyQuota = units
			}
			partners = append(partners, server.Partner{ID: id, APIKey: apiKey, Secret: secret, DailyQuota: dailyQuota})
		}

		settlementMgr, err := buildSettlementManager(cfg.Stable, store)
		if err != nil {
			log.Fatalf("swapd: settlement: %v", err)
		}

		stableRuntime = server.StableRuntime{
			Enabled:    true,
			Engine:     engine,
			Limits:     limits,
			Assets:     assets,
			Partners:   partners,
			Settlement: settlementMgr,
		}
	}

	var oracleOpts []oracle.Option
	if stableRuntime.Enabled && stableRuntime.Engine != nil {
		oracleOpts = append(oracleOpts, oracle.WithPublisher(&stableEnginePublisher{engine: stableRuntime.Engine}))
	}
	mgr, err := oracle.New(store, sources, pairs, cfg.Oracle.Interval.Duration, cfg.Oracle.MaxAge.Duration, cfg.Oracle.MinFeeds, oracleOpts...)
	if err != nil {
		log.Fatalf("swapd: oracle manager: %v", err)
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

	if cfg.PriceProof.Enabled {
		signerClient, err := hsm.NewClient(hsm.Config{
			BaseURL:    cfg.PriceProof.Signer.BaseURL,
			KeyLabel:   cfg.PriceProof.Signer.KeyLabel,
			CACertPath: cfg.PriceProof.Signer.CACertPath,
			ClientCert: cfg.PriceProof.Signer.ClientCertPath,
			ClientKey:  cfg.PriceProof.Signer.ClientKeyPath,
			SignPath:   cfg.PriceProof.Signer.SignPath,
		})
		if err != nil {
			log.Fatalf("swapd: configure price proof signer: %v", err)
		}
		priceProofService, err := priceproof.New(mgr, signerClient, cfg.PriceProof.Provider)
		if err != nil {
			log.Fatalf("swapd: configure price proof service: %v", err)
		}
		priceProofPartners := make([]server.Partner, 0, len(cfg.PriceProof.Partners))
		for _, partner := range cfg.PriceProof.Partners {
			id := strings.TrimSpace(partner.ID)
			apiKey := strings.TrimSpace(partner.APIKey)
			secret := strings.TrimSpace(partner.Secret)
			if id == "" || apiKey == "" || secret == "" {
				log.Fatalf("swapd: price proof partner configuration incomplete")
			}
			priceProofPartners = append(priceProofPartners, server.Partner{ID: id, APIKey: apiKey, Secret: secret})
		}
		if err := srv.SetPriceProofRuntime(server.PriceProofRuntime{Service: priceProofService, Partners: priceProofPartners}); err != nil {
			log.Fatalf("swapd: price proof runtime: %v", err)
		}
		log.Printf("swapd: price proof signing endpoint enabled (provider=%s pairs=%v)", cfg.PriceProof.Provider, cfg.PriceProof.Pairs)
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

// stableEnginePublisher adapts the stable engine's RecordPrice into an
// oracle.Publisher, so every real oracle tick (a median across configured
// sources, already computed and persisted via storage.RecordSnapshot)
// reaches the engine's live price cache instead of going nowhere. Without
// this, PriceQuote/quote requests only ever saw a price if something else
// (a test, an admin tool) called RecordPrice directly -- the standalone
// swapd binary itself never fed it one.
type stableEnginePublisher struct {
	engine *stable.Engine
}

func (p *stableEnginePublisher) PublishOracleUpdate(_ context.Context, update oracle.Update) error {
	if p == nil || p.engine == nil {
		return nil
	}
	rate, err := strconv.ParseFloat(strings.TrimSpace(update.Median), 64)
	if err != nil {
		return fmt.Errorf("swapd: parse oracle median %q for %s/%s: %w", update.Median, update.Base, update.Quote, err)
	}
	if rate <= 0 {
		return fmt.Errorf("swapd: non-positive oracle median %q for %s/%s", update.Median, update.Base, update.Quote)
	}
	p.engine.RecordPrice(update.Base, update.Quote, rate, update.Time)
	return nil
}

// buildSettlementManager wires up the dual-rail settlement manager from
// config: a NOWPayments payout client is only constructed if the
// nowpayments rail is actually used (by the default or any partner
// override) -- a deployment that only ever uses manual_treasury never needs
// NOWPayments credentials at all.
func buildSettlementManager(cfg config.StableConfig, store *storage.Storage) (*settlement.Manager, error) {
	settlementCfg := settlement.Config{
		DefaultRail:  settlement.Rail(strings.TrimSpace(cfg.Settlement.DefaultRail)),
		PartnerRails: make(map[string]settlement.Rail, len(cfg.Partners)),
	}
	usesNowPayments := settlementCfg.DefaultRail == settlement.RailNowPayments
	for _, partner := range cfg.Partners {
		id := strings.TrimSpace(partner.ID)
		rail := strings.TrimSpace(partner.SettlementRail)
		if rail == "" {
			continue
		}
		settlementCfg.PartnerRails[id] = settlement.Rail(rail)
		if settlement.Rail(rail) == settlement.RailNowPayments {
			usesNowPayments = true
		}
	}

	var payoutClient settlement.PayoutClient
	if usesNowPayments {
		client, err := settlement.NewHTTPPayoutClient(settlement.NowPaymentsConfig{
			Email:    cfg.Settlement.NowPayments.Email,
			Password: cfg.Settlement.NowPayments.Password,
			APIKey:   cfg.Settlement.NowPayments.APIKey,
			BaseURL:  cfg.Settlement.NowPayments.BaseURL,
		})
		if err != nil {
			return nil, fmt.Errorf("configure nowpayments payout client: %w", err)
		}
		payoutClient = client
	}

	mgr, err := settlement.NewManager(store, settlementCfg, payoutClient)
	if err != nil {
		return nil, fmt.Errorf("configure settlement manager: %w", err)
	}
	return mgr, nil
}
