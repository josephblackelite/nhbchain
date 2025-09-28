package oracleattesterd

import (
	"context"
	"encoding/hex"
	"flag"
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

	"nhbchain/crypto"
	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	cons "nhbchain/sdk/consensus"
)

// Main runs the oracle attester daemon using the provided command line flags.
func Main() error {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "services/oracle-attesterd/config.yaml", "path to oracle attesterd config")
	flag.Parse()

	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("oracle-attesterd", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "oracle-attesterd",
		Environment: env,
		Endpoint:    otlpEndpoint,
		Insecure:    insecure,
		Headers:     otlpHeaders,
		Metrics:     true,
		Traces:      true,
	})
	if err != nil {
		return fmt.Errorf("init telemetry: %w", err)
	}
	defer func() {
		if shutdownTelemetry != nil {
			_ = shutdownTelemetry(context.Background())
		}
	}()

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	keyBytes, err := hex.DecodeString(strings.TrimSpace(cfg.SignerKey))
	if err != nil {
		return fmt.Errorf("decode signer key: %w", err)
	}
	signer, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		return fmt.Errorf("load signer key: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	consensusClient, err := cons.Dial(ctx, cfg.ConsensusEndpoint)
	cancel()
	if err != nil {
		return fmt.Errorf("dial consensus: %w", err)
	}
	defer func() { _ = consensusClient.Close() }()

	evmClient, err := DialEVMClient(cfg.EVM.RPCURL)
	if err != nil {
		return fmt.Errorf("dial evm: %w", err)
	}
	defer evmClient.Close()

	store, err := NewInvoiceStore(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open invoice store: %w", err)
	}
	defer func() { _ = store.Close() }()

	submitter := &ConsensusSubmitter{
		Client:  consensusClient,
		Signer:  signer,
		ChainID: cfg.ChainID,
		Fee:     cfg.Fee,
	}

	verifier := NewEVMVerifier(evmClient)

	server, err := NewServer(cfg, store, verifier, submitter)
	if err != nil {
		return fmt.Errorf("build server: %w", err)
	}

	httpServer := &http.Server{
		Addr:         cfg.ListenAddress,
		Handler:      otelhttp.NewHandler(server, "oracle-attesterd"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 1)
	go func() {
		log.Printf("oracle-attesterd listening on %s", cfg.ListenAddress)
		errs <- httpServer.ListenAndServe()
	}()

	select {
	case <-stopCtx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			return err
		}
		return nil
	case err := <-errs:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}
