package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	identitygateway "nhbchain/services/identity-gateway"
)

func main() {
	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("identity-gateway", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "identity-gateway",
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

	if err := run(); err != nil {
		log.Fatalf("identity gateway failed: %v", err)
	}
}

func run() error {
	listenAddr := strings.TrimSpace(os.Getenv("IDENTITY_GATEWAY_LISTEN"))
	if listenAddr == "" {
		listenAddr = ":8095"
	}
	if port := strings.TrimSpace(os.Getenv("IDENTITY_GATEWAY_PORT")); port != "" {
		host, _, err := net.SplitHostPort(listenAddr)
		if err != nil {
			listenAddr = ":" + port
		} else {
			listenAddr = net.JoinHostPort(host, port)
		}
	}
	dbPath := strings.TrimSpace(os.Getenv("IDENTITY_GATEWAY_DB"))
	if dbPath == "" {
		dbPath = "identity-gateway.db"
	}
	emailSalt := strings.TrimSpace(os.Getenv("IDENTITY_EMAIL_SALT"))
	if emailSalt == "" {
		return errors.New("IDENTITY_EMAIL_SALT is required")
	}
	apiKeysRaw := strings.TrimSpace(os.Getenv("IDENTITY_GATEWAY_API_KEYS"))
	if apiKeysRaw == "" {
		return errors.New("IDENTITY_GATEWAY_API_KEYS is required")
	}
	apiKeys, err := parseAPIKeys(apiKeysRaw)
	if err != nil {
		return err
	}
	codeTTL := parseDurationDefault(os.Getenv("IDENTITY_GATEWAY_CODE_TTL"), 10*time.Minute)
	registerWindow := parseDurationDefault(os.Getenv("IDENTITY_GATEWAY_REGISTER_WINDOW"), time.Hour)
	timestampSkew := parseDurationDefault(os.Getenv("IDENTITY_GATEWAY_TIMESTAMP_SKEW"), 5*time.Minute)
	idempotencyTTL := parseDurationDefault(os.Getenv("IDENTITY_GATEWAY_IDEMPOTENCY_TTL"), 24*time.Hour)
	registerAttempts := parseIntDefault(os.Getenv("IDENTITY_GATEWAY_REGISTER_ATTEMPTS"), 5)

	store, err := identitygateway.NewStore(dbPath, nil)
	if err != nil {
		return err
	}
	defer store.Close()

	emailer := &identitygateway.LogEmailer{}
	server, err := identitygateway.NewServer(store, emailer, identitygateway.Config{
		APIKeys:          apiKeys,
		EmailSalt:        []byte(emailSalt),
		CodeTTL:          codeTTL,
		RegisterWindow:   registerWindow,
		RegisterAttempts: registerAttempts,
		TimestampSkew:    timestampSkew,
		IdempotencyTTL:   idempotencyTTL,
	})
	if err != nil {
		return err
	}

	handler := otelhttp.NewHandler(server, "identity-gateway")
	srv := &http.Server{
		Addr:         listenAddr,
		Handler:      handler,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	log.Printf("identity-gateway listening on %s", listenAddr)
	return srv.ListenAndServe()
}

func parseAPIKeys(raw string) (map[string]string, error) {
	entries := strings.Split(raw, ",")
	keys := make(map[string]string, len(entries))
	for _, entry := range entries {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			continue
		}
		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid API key entry %q", entry)
		}
		key := strings.TrimSpace(parts[0])
		secret := strings.TrimSpace(parts[1])
		if key == "" || secret == "" {
			return nil, fmt.Errorf("invalid API key entry %q", entry)
		}
		keys[key] = secret
	}
	if len(keys) == 0 {
		return nil, errors.New("no valid API keys configured")
	}
	return keys, nil
}

func parseDurationDefault(raw string, def time.Duration) time.Duration {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return def
	}
	value, err := time.ParseDuration(trimmed)
	if err != nil {
		log.Printf("invalid duration %q: %v", raw, err)
		return def
	}
	return value
}

func parseIntDefault(raw string, def int) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return def
	}
	value, err := strconv.Atoi(trimmed)
	if err != nil {
		log.Printf("invalid integer %q: %v", raw, err)
		return def
	}
	return value
}
