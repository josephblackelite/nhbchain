package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"nhbchain/observability/logging"
)

const shutdownTimeout = 10 * time.Second

func main() {
	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("ops-reporting", env)

	cfg, err := LoadConfigFromEnv()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	mintReader, err := NewMintReader(cfg.PaymentsDBPath)
	if err != nil {
		log.Fatalf("open mint reader: %v", err)
	}
	defer mintReader.Close()
	merchantReader, err := NewMerchantReader(cfg.EscrowDBPath)
	if err != nil {
		log.Fatalf("open merchant reader: %v", err)
	}
	defer merchantReader.Close()
	treasuryReader, err := NewTreasuryReader(cfg.TreasuryDBPath)
	if err != nil {
		log.Fatalf("open treasury reader: %v", err)
	}
	defer treasuryReader.Close()
	payoutReader, err := NewPayoutExecutionReader(cfg.PayoutDBPath)
	if err != nil {
		log.Fatalf("open payout reader: %v", err)
	}
	defer payoutReader.Close()

	server := NewServer(mintReader, merchantReader, treasuryReader, payoutReader, cfg.BearerToken)
	httpServer := &http.Server{Addr: cfg.ListenAddress, Handler: server}

	go func() {
		log.Printf("ops-reporting listening on %s", cfg.ListenAddress)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := httpServer.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
