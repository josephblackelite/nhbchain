package main

import (
	"context"
	"encoding/hex"
	"flag"
	"log"
	"net"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"google.golang.org/grpc"

	"nhbchain/crypto"
	govv1 "nhbchain/proto/gov/v1"
	cons "nhbchain/sdk/consensus"
	"nhbchain/services/governd/config"
	"nhbchain/services/governd/server"
)

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "services/governd/config.yaml", "path to governd config")
	flag.Parse()

	cfg, err := config.Load(cfgPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}
	keyBytes, err := hex.DecodeString(strings.TrimSpace(cfg.SignerKey))
	if err != nil {
		log.Fatalf("decode signer key: %v", err)
	}
	signer, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		log.Fatalf("load signer key: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	client, err := cons.Dial(ctx, cfg.ConsensusEndpoint)
	cancel()
	if err != nil {
		log.Fatalf("dial consensus: %v", err)
	}
	defer func() { _ = client.Close() }()

	service := server.New(client, signer, cfg)

	listener, err := net.Listen("tcp", cfg.ListenAddress)
	if err != nil {
		log.Fatalf("listen on %s: %v", cfg.ListenAddress, err)
	}
	grpcServer := grpc.NewServer()
	govv1.RegisterQueryServer(grpcServer, service)
	govv1.RegisterMsgServer(grpcServer, service)

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	serverErr := make(chan error, 1)
	go func() {
		log.Printf("governd listening on %s", cfg.ListenAddress)
		serverErr <- grpcServer.Serve(listener)
	}()

	select {
	case <-rootCtx.Done():
		log.Println("shutdown signal received")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		stopped := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
		case <-shutdownCtx.Done():
			log.Println("forcing shutdown")
			grpcServer.Stop()
		}
	case err := <-serverErr:
		if err != nil {
			log.Fatalf("serve gRPC: %v", err)
		}
	}
}
