package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"nhbchain/gateway/compat"
	"nhbchain/gateway/config"
	"nhbchain/gateway/middleware"
	"nhbchain/gateway/routes"
	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
)

func main() {
	var cfgPath string
	var compatModeFlag string
	flag.StringVar(&cfgPath, "config", "", "path to gateway configuration")
	flag.StringVar(&compatModeFlag, "compat-mode", "", "override JSON-RPC compatibility mode (enabled|disabled|auto)")
	flag.Parse()

	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	slogger := logging.Setup("gateway", env)
	logger := log.New(os.Stdout, "gateway ", log.LstdFlags|log.Lmsgprefix)

	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "gateway",
		Environment: env,
		Endpoint:    otlpEndpoint,
		Insecure:    insecure,
		Headers:     otlpHeaders,
		Metrics:     true,
		Traces:      true,
	})
	if err != nil {
		slogger.Error("failed to initialise telemetry", "error", err)
		os.Exit(1)
	}
	defer func() {
		if shutdownTelemetry != nil {
			_ = shutdownTelemetry(context.Background())
		}
	}()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		logger.Fatalf("load config: %v", err)
	}

	configuredMode := compat.ModeAuto
	if compatModeFlag != "" {
		parsed, err := compat.ParseMode(compatModeFlag)
		if err != nil {
			logger.Fatalf("parse compat-mode flag: %v", err)
		}
		configuredMode = parsed
	} else if envMode := strings.TrimSpace(os.Getenv("NHB_COMPAT_MODE")); envMode != "" {
		parsed, err := compat.ParseMode(envMode)
		if err != nil {
			logger.Fatalf("parse NHB_COMPAT_MODE: %v", err)
		}
		configuredMode = parsed
	}
	effectiveMode := compat.DefaultMode()
	if configuredMode != compat.ModeAuto {
		effectiveMode = configuredMode
	}
	enableCompat := compat.ShouldEnable(configuredMode)
	logger.Printf("compatibility mode: requested=%s effective=%s enabled=%t", configuredMode, effectiveMode, enableCompat)
	if _, err := compat.Plan(); err != nil {
		logger.Printf("compat deprecation plan not loaded: %v", err)
	}

	serviceEndpoints := ensureServiceConfig(cfg)
	services := make([]*compat.Service, 0, len(serviceEndpoints))
	for name, endpoint := range serviceEndpoints {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			logger.Fatalf("parse %s endpoint: %v", name, err)
		}
		services = append(services, &compat.Service{Name: name, BaseURL: parsed})
	}
	servicesByName := servicesMap(services)
	required := []string{"lendingd", "swapd", "governd", "consensusd"}
	for _, name := range required {
		if servicesByName[name] == nil {
			logger.Fatalf("missing configuration for service %s", name)
		}
	}

	var compatHandler http.Handler
	if enableCompat {
		dispatcher := compat.NewDispatcher(services, compat.DefaultMappings)
		compatHandler = dispatcher.Handler()
	} else {
		logger.Println("JSON-RPC compatibility dispatcher disabled")
	}

	obs := middleware.NewObservability(middleware.ObservabilityConfig{
		ServiceName:   cfg.Observability.ServiceName,
		MetricsPrefix: cfg.Observability.MetricsPrefix,
		LogRequests:   cfg.Observability.LogRequests,
		Enabled:       cfg.Observability.Metrics || cfg.Observability.Tracing,
	}, logger)

	auth := middleware.NewAuthenticator(middleware.AuthConfig{
		Enabled:        cfg.Auth.Enabled,
		HMACSecret:     cfg.Auth.HMACSecret,
		Issuer:         cfg.Auth.Issuer,
		Audience:       cfg.Auth.Audience,
		ScopeClaim:     cfg.Auth.ScopeClaim,
		OptionalPaths:  cfg.Auth.OptionalPaths,
		AllowAnonymous: cfg.Auth.AllowAnonymous,
		ClockSkew:      cfg.Auth.ClockSkew,
	}, logger)

	rateLimits := make(map[string]middleware.RateLimit)
	for _, entry := range cfg.RateLimits {
		if entry.ID == "" {
			continue
		}
		rate := entry.RatePerSecond
		if rate <= 0 && entry.RequestsPerMinute > 0 {
			rate = entry.RequestsPerMinute / 60.0
		}
		rateLimits[entry.ID] = middleware.RateLimit{
			RatePerSecond: rate,
			Burst:         entry.Burst,
		}
	}
	if len(rateLimits) == 0 {
		rateLimits["lending"] = middleware.RateLimit{RatePerSecond: 2, Burst: 20}
		rateLimits["swap"] = middleware.RateLimit{RatePerSecond: 1, Burst: 10}
		rateLimits["gov"] = middleware.RateLimit{RatePerSecond: 1, Burst: 10}
		rateLimits["consensus"] = middleware.RateLimit{RatePerSecond: 4, Burst: 40}
	}

	router := routes.New(routes.Config{
		Routes: []routes.ServiceRoute{
			{
				Name:           "lending",
				Prefix:         "/v1/lending",
				Target:         servicesByName["lendingd"].BaseURL,
				RequireAuth:    true,
				RequiredScopes: []string{"lending"},
				RateLimitKey:   "lending",
			},
			{
				Name:           "swap",
				Prefix:         "/v1/swap",
				Target:         servicesByName["swapd"].BaseURL,
				RequireAuth:    true,
				RequiredScopes: []string{"swap"},
				RateLimitKey:   "swap",
			},
			{
				Name:           "gov",
				Prefix:         "/v1/gov",
				Target:         servicesByName["governd"].BaseURL,
				RequireAuth:    true,
				RequiredScopes: []string{"gov"},
				RateLimitKey:   "gov",
			},
			{
				Name:         "consensus",
				Prefix:       "/v1/consensus",
				Target:       servicesByName["consensusd"].BaseURL,
				RequireAuth:  false,
				RateLimitKey: "consensus",
			},
		},
		CompatHandler: compatHandler,
		Authenticator: auth,
		RateLimiter:   middleware.NewRateLimiter(rateLimits, logger),
		Observability: obs,
		CORS: middleware.CORSConfig{
			AllowedOrigins:   []string{"*"},
			AllowedMethods:   []string{"GET", "POST", "OPTIONS"},
			AllowedHeaders:   []string{"Content-Type", "Authorization"},
			AllowCredentials: false,
		},
	})

	handler := http.Handler(router)
	if cfg.Observability.Tracing {
		handler = otelhttp.NewHandler(router, "gateway")
	}

	server := &http.Server{
		Addr:         cfg.ListenAddress,
		Handler:      handler,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Printf("listening on %s", cfg.ListenAddress)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatalf("listen and serve: %v", err)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Printf("graceful shutdown failed: %v", err)
	}
}

func ensureServiceConfig(cfg config.Config) map[string]string {
	endpoints := map[string]string{
		"lendingd":   "http://127.0.0.1:7101",
		"swapd":      "http://127.0.0.1:7102",
		"governd":    "http://127.0.0.1:7103",
		"consensusd": "http://127.0.0.1:7104",
	}
	envOverrides := map[string]string{
		"lendingd":   os.Getenv("NHB_GATEWAY_LENDING_URL"),
		"swapd":      os.Getenv("NHB_GATEWAY_SWAP_URL"),
		"governd":    os.Getenv("NHB_GATEWAY_GOV_URL"),
		"consensusd": os.Getenv("NHB_GATEWAY_CONSENSUS_URL"),
	}
	for name, value := range envOverrides {
		if strings.TrimSpace(value) != "" {
			endpoints[name] = value
		}
	}
	for _, svc := range cfg.Services {
		if strings.TrimSpace(svc.Name) == "" || strings.TrimSpace(svc.Endpoint) == "" {
			continue
		}
		endpoints[svc.Name] = svc.Endpoint
	}
	return endpoints
}

func servicesMap(services []*compat.Service) map[string]*compat.Service {
	out := make(map[string]*compat.Service, len(services))
	for _, svc := range services {
		out[svc.Name] = svc
	}
	return out
}
