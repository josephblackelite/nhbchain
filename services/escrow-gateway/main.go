package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

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

	auth := NewAuthenticator(cfg.APIKeys, cfg.AllowedTimestampSkew, nil)
	node := NewRPCNodeClient(cfg.NodeURL, cfg.NodeAuthToken)
	queue := NewWebhookQueue()
	intents := NewPayIntentBuilder()
	server := NewServer(auth, node, store, queue, intents)

	srv := &http.Server{
		Addr:    cfg.ListenAddress,
		Handler: server,
	}

	go func() {
		log.Printf("escrow gateway listening on %s", cfg.ListenAddress)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Printf("shutting down escrow gateway")
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "graceful shutdown failed: %v\n", err)
	}
}

const shutdownTimeout = 10 * time.Second
