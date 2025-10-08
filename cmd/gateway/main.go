package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
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
	var allowInsecureFlag bool
	flag.StringVar(&cfgPath, "config", "", "path to gateway configuration")
	flag.StringVar(&compatModeFlag, "compat-mode", "", "override JSON-RPC compatibility mode (enabled|disabled|auto)")
	flag.BoolVar(&allowInsecureFlag, "allow-insecure", false, "DEV ONLY: permit plaintext listeners on loopback interfaces")
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

	configDir := ""
	if strings.TrimSpace(cfgPath) != "" {
		configDir = filepath.Dir(cfgPath)
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
	autoUpgrade := cfg.Security.AutoUpgradeHTTP
	if override := strings.TrimSpace(os.Getenv("NHB_GATEWAY_AUTO_HTTPS")); override != "" {
		parsed, err := strconv.ParseBool(override)
		if err != nil {
			logger.Fatalf("parse NHB_GATEWAY_AUTO_HTTPS: %v", err)
		}
		autoUpgrade = parsed
	}
	services := make([]*compat.Service, 0, len(serviceEndpoints))
	for name, endpoint := range serviceEndpoints {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			logger.Fatalf("parse %s endpoint: %v", name, err)
		}
		secured, upgraded, err := config.EnforceSecureScheme(env, parsed, autoUpgrade)
		if err != nil {
			logger.Fatalf("enforce HTTPS for %s endpoint: %v", name, err)
		}
		if upgraded {
			logger.Printf("auto-upgraded %s endpoint to HTTPS", name)
		}
		services = append(services, &compat.Service{Name: name, BaseURL: secured})
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

	router, err := routes.New(routes.Config{
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
				Name:         "transactions",
				Prefix:       "/v1/transactions",
				Target:       servicesByName["consensusd"].BaseURL,
				RequireAuth:  true,
				RateLimitKey: "consensus",
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

	if err != nil {
		logger.Fatalf("configure routes: %v", err)
	}

	handler := http.Handler(router)
	if cfg.Observability.Tracing {
		handler = otelhttp.NewHandler(router, "gateway")
	}

	tlsConfig, err := buildTLSConfig(configDir, cfg.Security)
	if err != nil {
		logger.Fatalf("configure TLS: %v", err)
	}

	allowInsecure := cfg.Security.AllowInsecure || allowInsecureFlag
	if tlsConfig == nil {
		if !allowInsecure {
			logger.Fatal("gateway TLS certificate and key are required; provide security.tlsCertFile/tlsKeyFile or start with --allow-insecure in dev")
		}
		if !strings.EqualFold(env, "dev") && !isLoopbackAddress(cfg.ListenAddress) {
			logger.Fatal("plaintext gateway mode is restricted to loopback listeners or dev environment")
		}
	}

	server := &http.Server{
		Addr:         cfg.ListenAddress,
		Handler:      handler,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}
	if tlsConfig != nil {
		server.TLSConfig = tlsConfig
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		logger.Fatalf("listen: %v", err)
	}
	go func() {
		scheme := "http"
		if tlsConfig != nil {
			scheme = "https"
		}
		logger.Printf("listening on %s://%s", scheme, listener.Addr())
		var serveErr error
		if tlsConfig != nil {
			serveErr = server.Serve(tls.NewListener(listener, tlsConfig))
		} else {
			serveErr = server.Serve(listener)
		}
		if serveErr != nil && serveErr != http.ErrServerClosed {
			logger.Fatalf("listen and serve: %v", serveErr)
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

func buildTLSConfig(baseDir string, sec config.SecurityConfig) (*tls.Config, error) {
	certPath := resolveTLSPath(baseDir, sec.TLSCertFile)
	keyPath := resolveTLSPath(baseDir, sec.TLSKeyFile)
	caPath := resolveTLSPath(baseDir, sec.TLSClientCAFile)
	if strings.TrimSpace(certPath) == "" && strings.TrimSpace(keyPath) == "" && strings.TrimSpace(caPath) == "" {
		return nil, nil
	}
	if strings.TrimSpace(certPath) == "" || strings.TrimSpace(keyPath) == "" {
		return nil, fmt.Errorf("security.tlsCertFile and security.tlsKeyFile must both be provided when enabling TLS")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load TLS key pair: %w", err)
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	if strings.TrimSpace(caPath) != "" {
		data, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("parse client CA file %s", caPath)
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return tlsCfg, nil
}

func resolveTLSPath(baseDir, path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if baseDir == "" || filepath.IsAbs(trimmed) {
		return trimmed
	}
	return filepath.Join(baseDir, trimmed)
}

func isLoopbackAddress(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
