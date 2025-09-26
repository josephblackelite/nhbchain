package main

import (
	"log"
	"net/http"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"nhbchain/services/otc-gateway/config"
	"nhbchain/services/otc-gateway/models"
	"nhbchain/services/otc-gateway/server"
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

	handler := server.New(db, cfg.DefaultTZ, cfg.ChainID, cfg.S3Bucket, cfg.RPCBase)

	addr := ":" + cfg.Port
	log.Printf("starting otc-gateway on %s", addr)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
