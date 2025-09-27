package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"gopkg.in/yaml.v3"

	lendingv1 "nhbchain/proto/lending/v1"
	lendingserver "nhbchain/services/lending/server"
)

type Config struct {
	ListenAddress string `yaml:"listen"`
}

func loadConfig(path string) (Config, error) {
	cfg := Config{ListenAddress: ":50053"}
	if path == "" {
		return cfg, fmt.Errorf("config path required")
	}
	file, err := os.Open(path)
	if err != nil {
		return Config{}, fmt.Errorf("open config: %w", err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}
	if cfg.ListenAddress == "" {
		cfg.ListenAddress = ":50053"
	}
	return cfg, nil
}

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "services/lending/config.yaml", "path to lendingd config")
	flag.Parse()

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.ListenAddress, err)
	}
	grpcServer := grpc.NewServer()
	service := lendingserver.New()
	lendingv1.RegisterLendingServiceServer(grpcServer, service)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("lendingd listening on %s", cfg.ListenAddress)
		serverErr <- grpcServer.Serve(listener)
	}()

	select {
	case <-ctx.Done():
		log.Println("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		done := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(done)
		}()
		select {
		case <-done:
		case <-shutdownCtx.Done():
			log.Println("forcing server stop")
			grpcServer.Stop()
		}
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("serve gRPC: %v", err)
		}
	}
}
