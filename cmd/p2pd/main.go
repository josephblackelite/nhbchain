package main

import (
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"

	"nhbchain/config"
	"nhbchain/core"
	"nhbchain/network"
	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	"nhbchain/p2p"
	"nhbchain/p2p/seeds"
	networkv1 "nhbchain/proto/network/v1"
	"nhbchain/storage"
)

const (
	allowAutogenesisEnv = "NHB_ALLOW_AUTOGENESIS"
	genesisPathEnv      = "NHB_GENESIS"
)

func main() {
	configFile := flag.String("config", "./config.toml", "Path to the configuration file")
	genesisFlag := flag.String("genesis", "", "Path to a genesis block JSON file")
	allowAutogenesisFlag := flag.Bool("allow-autogenesis", false, "Allow automatic genesis creation when no stored genesis exists")
	grpcAddress := flag.String("grpc", ":9091", "Address for the internal network gRPC server")
	flag.Parse()

	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("p2pd", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "p2pd",
		Environment: env,
		Endpoint:    otlpEndpoint,
		Insecure:    insecure,
		Headers:     otlpHeaders,
		Metrics:     true,
		Traces:      true,
	})
	if err != nil {
		panic(fmt.Sprintf("failed to initialise telemetry: %v", err))
	}
	defer func() {
		if shutdownTelemetry != nil {
			_ = shutdownTelemetry(context.Background())
		}
	}()

	allowAutogenesisCLISet := flagWasProvided("allow-autogenesis")

	cfg, err := config.Load(*configFile)
	if err != nil {
		panic(fmt.Sprintf("failed to load config: %v", err))
	}

	allowAutogenesis, err := resolveAllowAutogenesis(cfg.AllowAutogenesis, allowAutogenesisCLISet, *allowAutogenesisFlag, os.LookupEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve autogenesis setting: %v\n", err)
		os.Exit(1)
	}

	genesisPath, err := resolveGenesisPath(*genesisFlag, cfg.GenesisFile, allowAutogenesis, os.LookupEnv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to resolve genesis path: %v\n", err)
		os.Exit(1)
	}

	db, err := storage.NewLevelDB(cfg.DataDir)
	if err != nil {
		panic(fmt.Sprintf("failed to open database: %v", err))
	}
	defer db.Close()

	chain, err := core.NewBlockchain(db, genesisPath, allowAutogenesis)
	if err != nil {
		panic(fmt.Sprintf("failed to load blockchain: %v", err))
	}
	chainID := chain.ChainID()
	genesisHash := chain.GenesisHash()

	peerstoreDir := filepath.Join(cfg.DataDir, "p2p")
	if err := os.MkdirAll(peerstoreDir, 0o755); err != nil {
		panic(fmt.Sprintf("failed to prepare p2p directory: %v", err))
	}
	peerstorePath := filepath.Join(peerstoreDir, "peerstore")
	peerstore, err := p2p.NewPeerstore(peerstorePath, 0, 0)
	if err != nil {
		panic(fmt.Sprintf("failed to open peerstore: %v", err))
	}
	defer peerstore.Close()

	identityPath := filepath.Join(peerstoreDir, "node_key.json")
	identity, err := p2p.LoadOrCreateIdentity(identityPath)
	if err != nil {
		panic(fmt.Sprintf("failed to load node identity: %v", err))
	}

	seedStrings := make([]string, 0, len(cfg.P2P.Seeds))
	seedOrigins := make([]p2p.SeedOrigin, 0, len(cfg.P2P.Seeds))
	seenSeeds := make(map[string]struct{})
	for _, raw := range cfg.P2P.Seeds {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}
		nodePart, addrPart, found := strings.Cut(trimmed, "@")
		if !found {
			fmt.Printf("Ignoring seed %q: missing node ID\n", trimmed)
			continue
		}
		nodeID := strings.TrimSpace(nodePart)
		addr := strings.TrimSpace(addrPart)
		if nodeID == "" || addr == "" {
			fmt.Printf("Ignoring seed %q: empty components\n", trimmed)
			continue
		}
		key := strings.ToLower(nodeID) + "@" + strings.ToLower(addr)
		if _, exists := seenSeeds[key]; exists {
			continue
		}
		seenSeeds[key] = struct{}{}
		seedStrings = append(seedStrings, fmt.Sprintf("%s@%s", nodeID, addr))
		seedOrigins = append(seedOrigins, p2p.SeedOrigin{NodeID: nodeID, Address: addr, Source: "config"})
	}
	// TODO: supplement static seeds with dynamic entries from the consensus
	// registry once the discovery flow is plumbed through this daemon.

	pexEnabled := true
	if cfg.P2P.PEX != nil {
		pexEnabled = *cfg.P2P.PEX
	}

	relay := network.NewRelay()

	serverCfg := p2p.ServerConfig{
		ListenAddress:    cfg.ListenAddress,
		ChainID:          chainID,
		GenesisHash:      append([]byte(nil), genesisHash...),
		ClientVersion:    cfg.ClientVersion,
		MaxPeers:         cfg.MaxPeers,
		MaxInbound:       cfg.MaxInbound,
		MaxOutbound:      cfg.MaxOutbound,
		MinPeers:         cfg.MinPeers,
		OutboundPeers:    cfg.OutboundPeers,
		Bootnodes:        append([]string{}, cfg.Bootnodes...),
		PersistentPeers:  append([]string{}, cfg.PersistentPeers...),
		Seeds:            append([]string{}, seedStrings...),
		SeedOrigins:      append([]p2p.SeedOrigin{}, seedOrigins...),
		SeedResolver:     seeds.DefaultResolver(),
		PeerBanDuration:  time.Duration(cfg.P2P.BanDurationSeconds) * time.Second,
		ReadTimeout:      time.Duration(cfg.ReadTimeout) * time.Second,
		WriteTimeout:     time.Duration(cfg.WriteTimeout) * time.Second,
		MaxMessageBytes:  cfg.MaxMsgBytes,
		RateMsgsPerSec:   cfg.P2P.RateMsgsPerSec,
		RateBurst:        cfg.P2P.Burst,
		BanScore:         cfg.P2P.BanScore,
		GreyScore:        cfg.P2P.GreyScore,
		HandshakeTimeout: time.Duration(cfg.P2P.HandshakeTimeoutMs) * time.Millisecond,
		PingInterval:     time.Duration(cfg.P2P.PingIntervalSeconds) * time.Second,
		PingTimeout:      time.Duration(cfg.P2P.PingTimeoutSeconds) * time.Second,
		DialBackoff:      time.Duration(cfg.P2P.DialBackoffSeconds) * time.Second,
		EnablePEX:        pexEnabled,
	}

	p2pServer := p2p.NewServer(relay, identity.PrivateKey, serverCfg)
	p2pServer.SetPeerstore(peerstore)
	relay.SetServer(p2pServer)

	grpcListener, err := net.Listen("tcp", *grpcAddress)
	if err != nil {
		panic(fmt.Sprintf("failed to listen on %s: %v", *grpcAddress, err))
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(otelgrpc.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(otelgrpc.StreamServerInterceptor()),
	)
	networkv1.RegisterNetworkServiceServer(grpcServer, network.NewService(relay))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.StartHeartbeats(ctx, time.Duration(cfg.P2P.PingIntervalSeconds)*time.Second)

	go func() {
		if err := grpcServer.Serve(grpcListener); err != nil {
			panic(fmt.Sprintf("network gRPC server failed: %v", err))
		}
	}()

	go p2pServer.Start()

	fmt.Println("--- p2pd initialised and running ---")
	select {}
}

type envLookupFunc func(string) (string, bool)

func resolveGenesisPath(cliPath string, cfgPath string, allowAutogenesis bool, lookup envLookupFunc) (string, error) {
	trimmedCLI := strings.TrimSpace(cliPath)
	if trimmedCLI != "" {
		return trimmedCLI, nil
	}
	if lookup != nil {
		if value, ok := lookup(genesisPathEnv); ok {
			trimmedEnv := strings.TrimSpace(value)
			if trimmedEnv != "" {
				return trimmedEnv, nil
			}
		}
	}
	trimmedCfg := strings.TrimSpace(cfgPath)
	if trimmedCfg != "" {
		return trimmedCfg, nil
	}
	if allowAutogenesis {
		return "", nil
	}
	return "", fmt.Errorf("no genesis file provided; supply one via --genesis, %s, or config, or explicitly enable autogenesis (--allow-autogenesis / %s / config)", genesisPathEnv, allowAutogenesisEnv)
}

func resolveAllowAutogenesis(cfgValue bool, cliSet bool, cliValue bool, lookup envLookupFunc) (bool, error) {
	allow := cfgValue
	if lookup != nil {
		if value, ok := lookup(allowAutogenesisEnv); ok {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				parsed, err := strconv.ParseBool(trimmed)
				if err != nil {
					return false, fmt.Errorf("invalid %s value %q: %w", allowAutogenesisEnv, trimmed, err)
				}
				allow = parsed
			}
		}
	}
	if cliSet {
		allow = cliValue
	}
	return allow, nil
}

func flagWasProvided(name string) bool {
	provided := false
	flag.CommandLine.Visit(func(f *flag.Flag) {
		if f.Name == name {
			provided = true
		}
	})
	return provided
}
