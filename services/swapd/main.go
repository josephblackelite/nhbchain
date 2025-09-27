package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nhbchain/services/swapd/adapters"
	"nhbchain/services/swapd/config"
	"nhbchain/services/swapd/oracle"
	"nhbchain/services/swapd/server"
	"nhbchain/services/swapd/storage"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "services/swapd/config.yaml", "path to swapd configuration file")
	flag.Parse()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("swapd: load config: %v", err)
	}

	store, err := storage.Open(cfg.DatabasePath)
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

	mgr, err := oracle.New(store, sources, pairs, cfg.Oracle.Interval.Duration, cfg.Oracle.MaxAge.Duration, cfg.Oracle.MinFeeds)
	if err != nil {
		log.Fatalf("swapd: oracle manager: %v", err)
	}

	srv, err := server.New(server.Config{ListenAddress: cfg.ListenAddress, PolicyID: policy.ID}, store, log.Default())
	if err != nil {
		log.Fatalf("swapd: server: %v", err)
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
