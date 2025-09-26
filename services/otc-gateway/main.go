package main

import (
	"context"
	"log"
	"net/http"
	"strconv"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"nhbchain/services/otc-gateway/config"
	"nhbchain/services/otc-gateway/hsm"
	"nhbchain/services/otc-gateway/models"
	"nhbchain/services/otc-gateway/recon"
	"nhbchain/services/otc-gateway/server"
	"nhbchain/services/otc-gateway/swaprpc"
)

func main() {
	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}

	db, err := gorm.Open(postgres.Open(cfg.DatabaseURL), &gorm.Config{})
	if err != nil {
		log.Fatalf("database connection error: %v", err)
	}

	if err := models.AutoMigrate(db); err != nil {
		log.Fatalf("auto migrate error: %v", err)
	}

	chainID, err := strconv.ParseUint(cfg.ChainID, 10, 64)
	if err != nil {
		log.Fatalf("invalid chain id %s: %v", cfg.ChainID, err)
	}

	signer, err := hsm.NewClient(hsm.Config{
		BaseURL:    cfg.HSMBaseURL,
		CACertPath: cfg.HSMCACert,
		ClientCert: cfg.HSMClientCert,
		ClientKey:  cfg.HSMClientKey,
		KeyLabel:   cfg.HSMKeyLabel,
		OverrideDN: cfg.HSMOverrideDN,
	})
	if err != nil {
		log.Fatalf("hsm client error: %v", err)
	}

	swapClient := swaprpc.NewClient(swaprpc.Config{URL: cfg.SwapRPCBase, Provider: cfg.SwapProvider})

	srv := server.New(server.Config{
		DB:           db,
		TZ:           cfg.DefaultTZ,
		ChainID:      chainID,
		S3Bucket:     cfg.S3Bucket,
		SwapClient:   swapClient,
		Signer:       signer,
		VoucherTTL:   cfg.VoucherTTL,
		Provider:     cfg.SwapProvider,
		PollInterval: cfg.MintPollInterval,
	})

	reconciler, err := recon.NewReconciler(recon.Config{
		DB:        db,
		TZ:        cfg.DefaultTZ,
		Exporter:  swapClient,
		OutputDir: cfg.ReconOutputDir,
		DryRun:    cfg.ReconDryRun,
	})
	if err != nil {
		log.Fatalf("reconciler init error: %v", err)
	}
	scheduler := recon.NewScheduler(recon.SchedulerConfig{
		Reconciler: reconciler,
		Window:     cfg.ReconWindow,
		RunHour:    cfg.ReconRunHour,
		RunMinute:  cfg.ReconRunMinute,
		Location:   cfg.DefaultTZ,
	})
	go scheduler.Start(context.Background())

	handler := srv.Handler()

	addr := ":" + cfg.Port
	log.Printf("starting otc-gateway on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
