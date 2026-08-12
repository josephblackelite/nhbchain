// Command buybackd runs the reference-price submission service for the
// on-chain buyback engine: once per epoch it aggregates a market price for
// the configured pair (ZNHB/USD by default), signs it with every locally
// held buyback signer key, and submits the M-of-N bundle to the chain via
// buyback_submitRefPrice.
//
// See services/buybackd/refprice's package doc comment for the honesty and
// custody caveats this service operates under -- neither is solved here,
// both are logged loudly at startup so an operator sees them every time
// this process starts, not just once in a design doc.
package main

import (
	"context"
	"log/slog"
	"math/big"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"nhbchain/native/swap"
	"nhbchain/observability/logging"
	"nhbchain/services/buybackd/config"
	"nhbchain/services/buybackd/refprice"
	"nhbchain/services/buybackd/rpcclient"
	"nhbchain/services/swapd/localsigner"
)

func main() {
	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logger := logging.Setup("buybackd", env)

	cfg := config.LoadFromEnv()
	if err := cfg.Validate(); err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(1)
	}
	sanitized := cfg.Sanitized()
	logger.Info("starting buybackd",
		"rpcBaseURL", sanitized.RPCBaseURL,
		"pair", sanitized.Pair,
		"threshold", sanitized.Threshold,
		"signers", len(sanitized.Signers),
		"pollInterval", sanitized.PollInterval.String(),
		"oraclePriority", sanitized.OraclePriority,
	)
	logger.Warn("neither ZNHB nor NHB has a real external market listing -- see services/buybackd/refprice's package doc comment; whatever this instance's oracle priority actually resolves to is, in practice, a manually configured peg, not genuine market discovery")
	logger.Warn("this process holds every locally configured signer key in memory and can single-handedly reach quorum if it holds enough keys -- see services/buybackd/refprice's package doc comment on the custody assumption this implies")

	source := buildQuoteSource(cfg, logger)

	signers := make([]refprice.Signer, 0, len(cfg.Signers))
	for _, signerCfg := range cfg.Signers {
		client, err := localsigner.NewClient(localsigner.Config{
			KeystorePath:  signerCfg.KeystorePath,
			PassphraseEnv: signerCfg.PassphraseEnv,
		})
		if err != nil {
			logger.Error("failed to load signer keystore", "path", signerCfg.KeystorePath, "error", err)
			os.Exit(1)
		}
		logger.Info("loaded signer keystore", "path", signerCfg.KeystorePath, "address", client.Address())
		signers = append(signers, client)
	}

	chainClient, err := rpcclient.NewClient(rpcclient.Config{
		BaseURL:     cfg.RPCBaseURL,
		BearerToken: cfg.RPCBearerToken,
		Timeout:     cfg.RPCTimeout,
	})
	if err != nil {
		logger.Error("failed to construct RPC client", "error", err)
		os.Exit(1)
	}

	svc, err := refprice.New(source, signers, cfg.Threshold, chainClient, cfg.Pair)
	if err != nil {
		logger.Error("failed to construct reference-price service", "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	runLoop(ctx, logger, svc, cfg.PollInterval)
	logger.Info("buybackd shutting down")
}

// buildQuoteSource wires a *swap.OracleAggregator exactly the way
// cmd/nhb/main.go wires the node's own swap oracle -- manual, NOWPayments,
// then CoinGecko in configured priority order, with the CoinGecko id map
// deliberately left empty because neither NHB nor ZNHB has a real listing
// there (see cmd/nhb/main.go's identical comment). Reusing this wiring
// keeps buybackd's price source honestly consistent with the rest of the
// chain's swap pricing rather than inventing a second, divergent one.
func buildQuoteSource(cfg config.Config, logger *slog.Logger) refprice.QuoteSource {
	manualOracle := swap.NewManualOracle()
	aggregator := swap.NewOracleAggregator(cfg.OraclePriority, cfg.MaxQuoteAge)
	aggregator.Register("manual", manualOracle)
	aggregator.Register("nowpayments", swap.NewNowPaymentsOracle(nil, "", cfg.NOWPaymentsAPIKey))
	aggregator.Register("coingecko", swap.NewCoinGeckoOracle(nil, "", map[string]string{}))
	if cfg.ManualRate != "" {
		base, quote, err := refprice.SplitPair(cfg.Pair)
		if err != nil {
			logger.Error("failed to parse configured pair for manual oracle seed", "pair", cfg.Pair, "error", err)
			os.Exit(1)
		}
		// native/swap's oracle convention takes (fiatCurrency, tokenSymbol)
		// -- the opposite order from refprice's BASE/QUOTE reading -- so
		// this seeds "USD per ZNHB" from a pair read as "ZNHB/USD". See
		// oracleQuoteSource.Quote below for where that same reversal
		// happens on every subsequent read, not just this one-time seed.
		if err := manualOracle.SetDecimal(quote, base, cfg.ManualRate, time.Now().UTC()); err != nil {
			logger.Error("failed to seed manual oracle rate", "pair", cfg.Pair, "error", err)
			os.Exit(1)
		}
	}
	return &oracleQuoteSource{aggregator: aggregator}
}

// oracleQuoteSource adapts *swap.OracleAggregator to refprice.QuoteSource.
//
// The two packages disagree on argument order for a good reason: refprice's
// pair convention (see its SplitPair) follows the conventional FX reading
// "BASE/QUOTE" where BASE is the asset being priced -- "ZNHB/USD" means
// "price of one ZNHB in USD". native/swap's GetRate/CoinGeckoOracle/
// NowPaymentsOracle instead take (fiatCurrency, tokenSymbol), matching
// every existing call site elsewhere in this repo (see cmd/nhb/main.go's
// aggregator.GetRate("USD", symbol) call and native/swap/oracle_test.go).
// This adapter is the one place that reversal happens, so refprice itself
// can stay agnostic to native/swap's argument order entirely.
type oracleQuoteSource struct {
	aggregator *swap.OracleAggregator
}

func (o *oracleQuoteSource) Quote(ctx context.Context, base, quote string) (*big.Rat, []string, time.Time, error) {
	result, err := o.aggregator.GetRate(quote, base)
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	return result.Rate, []string{result.Source}, result.Timestamp, nil
}

// runLoop calls svc.Attempt once immediately, then once per interval, until
// ctx is cancelled (SIGINT/SIGTERM). Every outcome -- submitted, skipped
// because the epoch already has a recorded price, or errored -- is logged,
// since this is the only place an operator running buybackd unattended will
// ever see what it actually did.
func runLoop(ctx context.Context, logger *slog.Logger, svc *refprice.Service, interval time.Duration) {
	attempt := func() {
		submitted, txHash, err := svc.Attempt(ctx)
		switch {
		case err != nil:
			logger.Error("reference-price submission attempt failed", "error", err)
		case submitted:
			logger.Info("submitted reference price", "txHash", txHash)
		default:
			logger.Info("no submission needed this cycle (current epoch already has a recorded reference price)")
		}
	}

	attempt()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			attempt()
		}
	}
}
