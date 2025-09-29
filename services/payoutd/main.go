package payoutd

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nhbchain/crypto"
	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	consclient "nhbchain/sdk/consensus"
	"nhbchain/services/payoutd/wallet"
)

// Main initialises and runs the payout daemon.
func Main() error {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "services/payoutd/config.yaml", "path to payoutd configuration")
	flag.Parse()

	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("payoutd", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "payoutd",
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

	policyDefs, err := LoadPolicies(cfg.PoliciesPath)
	if err != nil {
		return fmt.Errorf("load policies: %w", err)
	}
	enforcer, err := NewPolicyEnforcer(policyDefs)
	if err != nil {
		return fmt.Errorf("init policies: %w", err)
	}

	for asset, balance := range cfg.Inventory {
		amount, err := parseDecimal(balance)
		if err != nil {
			return fmt.Errorf("parse inventory %s: %w", asset, err)
		}
		enforcer.SetInventory(asset, amount)
	}

	metrics := NewMetrics()
	processor := NewProcessor(enforcer, WithMetrics(metrics), WithPollInterval(cfg.Wallet.PollInterval.Duration))
	if cfg.PauseOnStart {
		processor.Pause()
	}

	// Configure the consensus attestor if credentials are provided.
	var attestor Attestor
	keyBytes, err := hex.DecodeString(strings.TrimSpace(cfg.Consensus.SignerKey))
	if err != nil {
		return fmt.Errorf("decode signer key: %w", err)
	}
	signer, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		return fmt.Errorf("load signer key: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	consensusClient, err := consclient.Dial(ctx, cfg.Consensus.Endpoint)
	cancel()
	if err != nil {
		return fmt.Errorf("dial consensus: %w", err)
	}
	defer func() { _ = consensusClient.Close() }()
	attestor = &ConsensusAttestor{
		Client:    consensusClient,
		Signer:    signer,
		Authority: cfg.Authority,
		ChainID:   cfg.Consensus.ChainID,
		FeeAmount: cfg.Consensus.FeeAmount,
		FeeDenom:  cfg.Consensus.FeeDenom,
		FeePayer:  cfg.Consensus.FeePayer,
		Memo:      cfg.Consensus.Memo,
	}
	processor.attestor = attestor

	// Treasury wallet integration is injected externally; default to returning errors until configured.
	processor.wallet = wallet.FuncWallet{
		TransferFunc: func(context.Context, string, string, *big.Int) (string, error) {
			return "", fmt.Errorf("treasury wallet not configured")
		},
		ConfirmFunc: func(context.Context, string, int, time.Duration) error {
			return fmt.Errorf("treasury wallet not configured")
		},
	}

	adminServer := NewAdminServer(processor)
	httpServer := &http.Server{
		Addr:         cfg.ListenAddress,
		Handler:      adminServer,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errs := make(chan error, 1)
	go func() {
		log.Printf("payoutd listening on %s", cfg.ListenAddress)
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
