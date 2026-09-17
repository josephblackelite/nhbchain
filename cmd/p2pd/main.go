package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"nhbchain/cmd/internal/passphrase"
	"nhbchain/config"
	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/network"
	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	"nhbchain/p2p"
	"nhbchain/p2p/seeds"
	networkv1 "nhbchain/proto/network/v1"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

const (
	validatorPassEnv    = "NHB_VALIDATOR_PASS"
	allowAutogenesisEnv = "NHB_ALLOW_AUTOGENESIS"
	genesisPathEnv      = "NHB_GENESIS"
)

func main() {
	configFile := flag.String("config", "./config.toml", "Path to the configuration file")
	genesisFlag := flag.String("genesis", "", "Path to a genesis block JSON file")
	allowAutogenesisFlag := flag.Bool("allow-autogenesis", false, "Allow automatic genesis creation when no stored genesis exists")
	grpcAddress := flag.String("grpc", "127.0.0.1:9091", "Address for the internal network gRPC server")
	allowInsecureFlag := flag.Bool("allow-insecure", false, "DEV ONLY: permit plaintext listeners on loopback interfaces")
	flag.Parse()

	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logger := logging.Setup("p2pd", env)
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

	passSource := passphrase.NewSource(validatorPassEnv)

	cfg, err := config.Load(*configFile, config.WithKeystorePassphraseSource(passSource.Get))
	if err != nil {
		panic(fmt.Sprintf("failed to load config: %v", err))
	}

	allowAutogenesis, err := resolveAllowAutogenesis(cfg.AllowAutogenesis, allowAutogenesisCLISet, *allowAutogenesisFlag, os.LookupEnv)
	if err != nil {
		logger.Error("Failed to resolve autogenesis setting", slog.Any("error", err))
		os.Exit(1)
	}

	genesisPath, err := resolveGenesisPath(*genesisFlag, cfg.GenesisFile, allowAutogenesis, os.LookupEnv)
	if err != nil {
		logger.Error("Failed to resolve genesis path", slog.Any("error", err))
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
			logger.Warn("Ignoring seed due to missing node ID",
				logging.MaskField("seed", trimmed),
				slog.String("reason", "missing node id"))
			continue
		}
		nodeID := strings.TrimSpace(nodePart)
		addr := strings.TrimSpace(addrPart)
		if nodeID == "" || addr == "" {
			logger.Warn("Ignoring seed due to empty components",
				logging.MaskField("seed", trimmed),
				slog.String("reason", "empty components"))
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
	var seedRegistry *seeds.Registry
	header := chain.CurrentHeader()
	if header != nil {
		stateTrie, err := trie.NewTrie(db, header.StateRoot)
		if err != nil {
			logger.Error("Failed to open state trie", slog.Any("error", err))
		} else {
			manager := nhbstate.NewManager(stateTrie)
			if rawRegistry, ok, err := manager.ParamStoreGet("network.seeds"); err != nil {
				logger.Error("Failed to load network seeds", slog.Any("error", err))
			} else if ok {
				registry, parseErr := seeds.Parse(rawRegistry)
				if parseErr != nil {
					logger.Error("Failed to parse network seeds", slog.Any("error", parseErr))
				} else {
					seedRegistry = registry
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					resolved, resolveErr := registry.Resolve(ctx, time.Now(), seeds.DefaultResolver())
					cancel()
					if resolveErr != nil {
						logger.Error("DNS seed resolution failed", slog.Any("error", resolveErr))
					}
					for _, entry := range resolved {
						addr := strings.TrimSpace(entry.Address)
						key := strings.ToLower(entry.NodeID) + "@" + strings.ToLower(addr)
						if _, exists := seenSeeds[key]; exists {
							continue
						}
						seenSeeds[key] = struct{}{}
						seedStrings = append(seedStrings, fmt.Sprintf("%s@%s", entry.NodeID, addr))
						seedOrigins = append(seedOrigins, p2p.SeedOrigin{
							NodeID:    entry.NodeID,
							Address:   addr,
							Source:    entry.Source,
							NotBefore: entry.NotBefore,
							NotAfter:  entry.NotAfter,
						})
					}
				}
			}
		}
	}

	pexEnabled := true
	if cfg.P2P.PEX != nil {
		pexEnabled = *cfg.P2P.PEX
	}

	relay := network.NewRelay(
		network.WithRelayQueueSize(cfg.NetworkSecurity.StreamQueueSize),
		network.WithRelayDropAlertRatio(cfg.NetworkSecurity.RelayDropLogRatio),
		network.WithRelayLogger(logger.With(slog.String("component", "network_relay"))),
	)

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
		SeedRegistry:     seedRegistry,
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
	baseDir := filepath.Dir(*configFile)
	serverCreds, auth, readAuth, err := buildNetworkServerSecurity(cfg, baseDir, *allowInsecureFlag, grpcListener.Addr())
	if err != nil {
		panic(fmt.Sprintf("failed to initialise network security: %v", err))
	}

	grpcServer := grpc.NewServer(
		grpc.Creds(serverCreds),
		grpc.ChainUnaryInterceptor(otelgrpc.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(otelgrpc.StreamServerInterceptor()),
	)
	svc, err := network.NewService(relay, auth,
		network.WithReadAuthenticator(readAuth),
		network.WithAllowUnauthenticatedReads(cfg.NetworkSecurity.AllowUnauthenticatedReads),
	)
	if err != nil {
		panic(fmt.Sprintf("failed to initialise network service: %v", err))
	}
	networkv1.RegisterNetworkServiceServer(grpcServer, svc)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go relay.StartHeartbeats(ctx, time.Duration(cfg.P2P.PingIntervalSeconds)*time.Second)

	go func() {
		if err := grpcServer.Serve(grpcListener); err != nil {
			panic(fmt.Sprintf("network gRPC server failed: %v", err))
		}
	}()

	go p2pServer.Start()

	logger.Info("p2pd initialised and running")
	select {}
}

type envLookupFunc func(string) (string, bool)

func buildNetworkServerSecurity(cfg *config.Config, baseDir string, allowInsecureFlag bool, listener net.Addr) (credentials.TransportCredentials, network.Authenticator, network.Authenticator, error) {
	if cfg == nil {
		auth := network.ChainAuthenticators()
		if !allowInsecureFlag {
			return nil, nil, nil, fmt.Errorf("plaintext network bridge requires --allow-insecure runtime flag")
		}
		tcpAddr, ok := listener.(*net.TCPAddr)
		if !ok || tcpAddr.IP == nil || !tcpAddr.IP.IsLoopback() {
			return nil, nil, nil, fmt.Errorf("plaintext network bridge is restricted to loopback listeners; refusing %v", listener)
		}
		return insecure.NewCredentials(), auth, auth, nil
	}
	return network.BuildServerSecurity(&cfg.NetworkSecurity, baseDir, os.LookupEnv, allowInsecureFlag, listener)
}

func resolvePath(baseDir, path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	if baseDir != "" && !filepath.IsAbs(trimmed) {
		return filepath.Join(baseDir, trimmed)
	}
	return trimmed
}

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
