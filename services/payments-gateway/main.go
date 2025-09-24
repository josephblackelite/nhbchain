package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const shutdownTimeout = 10 * time.Second

func main() {
	cfg, err := LoadConfigFromEnv()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	store, err := NewSQLiteStore(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("open sqlite store: %v", err)
	}
	defer store.Close()

	oracle := NewOracle(cfg.OracleTTL, cfg.OracleMaxDeviation, cfg.OracleCircuitBreaker)
	signer, err := NewEnvKMSSigner(cfg.MinterKMSEnv)
	if err != nil {
		log.Fatalf("configure kms signer: %v", err)
	}
	nowClient := NewHTTPNowPaymentsClient(cfg.NowPaymentsBaseURL, cfg.NowPaymentsAPIKey)
	nodeClient := NewRPCNodeClient(cfg.NodeURL, cfg.NodeAuthToken)

	server := NewServer(store, oracle, nowClient, nodeClient, signer, cfg.QuoteTTL, cfg.QuoteCurrency, cfg.NowPaymentsIPNSecret)
	srv := &http.Server{Addr: cfg.ListenAddress, Handler: server}

	go func() {
		log.Printf("payments gateway listening on %s", cfg.ListenAddress)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Printf("shutting down payments gateway")
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown failed: %v", err)
	}
}
