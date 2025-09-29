package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"

	"nhbchain/config"
	"nhbchain/consensus/bft"
	"nhbchain/consensus/service"
	"nhbchain/core"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/lending"
	swap "nhbchain/native/swap"
	"nhbchain/network"
	"nhbchain/observability/logging"
	telemetry "nhbchain/observability/otel"
	consensusv1 "nhbchain/proto/consensus/v1"
	"nhbchain/storage"
)

const (
	validatorPassEnv    = "NHB_VALIDATOR_PASS"
	genesisPathEnv      = "NHB_GENESIS"
	allowAutogenesisEnv = "NHB_ALLOW_AUTOGENESIS"
)

func convertQuota(q config.Quota) nativecommon.Quota {
	return nativecommon.Quota{
		MaxRequestsPerMin: q.MaxRequestsPerMin,
		MaxNHBPerEpoch:    q.MaxNHBPerEpoch,
		EpochSeconds:      q.EpochSeconds,
	}
}

func main() {
	configFile := flag.String("config", "./config.toml", "Path to the configuration file")
	genesisFlag := flag.String("genesis", "", "Path to a genesis block JSON file (overrides NHB_GENESIS and config GenesisFile)")
	allowAutogenesisFlag := flag.Bool("allow-autogenesis", false, "DEV ONLY: allow automatic genesis creation when no stored genesis exists")
	grpcAddress := flag.String("grpc", ":9090", "Address for the consensus gRPC server")
	networkAddress := flag.String("p2p", "localhost:9091", "Address of the p2p daemon network service")
	flag.Parse()

	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logging.Setup("consensusd", env)
	otlpEndpoint := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	otlpHeaders := telemetry.ParseHeaders(os.Getenv("OTEL_EXPORTER_OTLP_HEADERS"))
	insecure := true
	if value := strings.TrimSpace(os.Getenv("OTEL_EXPORTER_OTLP_INSECURE")); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			insecure = parsed
		}
	}
	shutdownTelemetry, err := telemetry.Init(context.Background(), telemetry.Config{
		ServiceName: "consensusd",
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
		panic(fmt.Sprintf("Failed to load config: %v", err))
	}

	if err := config.ValidateConfig(cfg.Global); err != nil {
		log.Fatal("invalid configuration", "err", err)
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
		panic(fmt.Sprintf("Failed to open database: %v", err))
	}
	defer db.Close()

	privKey, err := loadValidatorKey(cfg)
	if err != nil {
		panic(fmt.Sprintf("Failed to load validator key: %v", err))
	}

	node, err := core.NewNode(db, privKey, genesisPath, allowAutogenesis)
	if err != nil {
		panic(fmt.Sprintf("Failed to create node: %v", err))
	}

	node.SetGlobalConfig(cfg.Global)
	node.SetMempoolUnlimitedOptIn(cfg.Mempool.AllowUnlimited)
	node.SetMempoolLimit(cfg.Mempool.MaxTransactions)

	govPolicy, err := cfg.Governance.Policy()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse governance policy: %v", err))
	}
	node.SetGovernancePolicy(govPolicy)

	potsoCfg, err := cfg.PotsoRewardConfig()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse POTSO rewards config: %v", err))
	}
	if err := node.SetPotsoRewardConfig(potsoCfg); err != nil {
		panic(fmt.Sprintf("Failed to apply POTSO rewards config: %v", err))
	}
	weightCfg, err := cfg.PotsoWeightConfig()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse POTSO weight config: %v", err))
	}
	if err := node.SetPotsoWeightConfig(weightCfg); err != nil {
		panic(fmt.Sprintf("Failed to apply POTSO weight config: %v", err))
	}

	node.SetLendingRiskParameters(lending.RiskParameters{
		MaxLTV:               cfg.Lending.MaxLTVBps,
		LiquidationThreshold: cfg.Lending.LiquidationThresholdBps,
		DeveloperFeeCapBps:   cfg.Lending.DeveloperFeeBps,
	})

	node.SetLendingAccrualConfig(cfg.Lending.ReserveFactorBps, cfg.Lending.ProtocolFeeBps, lending.DefaultInterestModel)

	devCollectorStr := strings.TrimSpace(cfg.Lending.DeveloperFeeCollector)
	var devCollector crypto.Address
	if devCollectorStr != "" {
		decoded, err := crypto.DecodeAddress(devCollectorStr)
		if err != nil {
			panic(fmt.Sprintf("Failed to decode lending developer fee collector: %v", err))
		}
		devCollector = decoded
	} else if cfg.Lending.DeveloperFeeBps > 0 {
		panic("Lending DeveloperFeeCollector must be configured when DeveloperFeeBps is non-zero")
	}
	node.SetLendingDeveloperFee(cfg.Lending.DeveloperFeeBps, devCollector)

	routingCfg := cfg.Lending.CollateralRouting
	var developerCollateral crypto.Address
	if routingCfg.DeveloperBps > 0 {
		addr := strings.TrimSpace(routingCfg.DeveloperAddress)
		if addr == "" {
			panic("Lending collateral routing requires DeveloperAddress when DeveloperBps is non-zero")
		}
		decoded, err := crypto.DecodeAddress(addr)
		if err != nil {
			panic(fmt.Sprintf("Failed to decode lending developer collateral address: %v", err))
		}
		developerCollateral = decoded
	}
	var protocolCollateral crypto.Address
	if routingCfg.ProtocolBps > 0 {
		addr := strings.TrimSpace(routingCfg.ProtocolAddress)
		if addr == "" {
			panic("Lending collateral routing requires ProtocolAddress when ProtocolBps is non-zero")
		}
		decoded, err := crypto.DecodeAddress(addr)
		if err != nil {
			panic(fmt.Sprintf("Failed to decode lending protocol collateral address: %v", err))
		}
		protocolCollateral = decoded
	}
	totalRoutingBps := routingCfg.LiquidatorBps + routingCfg.DeveloperBps + routingCfg.ProtocolBps
	if totalRoutingBps > 10_000 {
		panic("Lending collateral routing shares must not exceed 10000 basis points")
	}
	node.SetLendingCollateralRouting(lending.CollateralRouting{
		LiquidatorBps:   routingCfg.LiquidatorBps,
		DeveloperBps:    routingCfg.DeveloperBps,
		DeveloperTarget: developerCollateral,
		ProtocolBps:     routingCfg.ProtocolBps,
		ProtocolTarget:  protocolCollateral,
	})

	swapCfg := cfg.SwapSettings()
	node.SetSwapConfig(swapCfg)
	manualOracle := swap.NewManualOracle()
	aggregator := swap.NewOracleAggregator(swapCfg.OraclePriority, swapCfg.MaxQuoteAge())
	aggregator.SetTWAPWindow(swapCfg.TwapWindow())
	aggregator.SetTWAPSampleCap(swapCfg.TwapSampleCap)
	aggregator.SetPriority(swapCfg.OraclePriority)
	aggregator.Register("manual", manualOracle)
	npAPIKey := strings.TrimSpace(os.Getenv("NHB_NOWPAYMENTS_API_KEY"))
	aggregator.Register("nowpayments", swap.NewNowPaymentsOracle(nil, "", npAPIKey))
	aggregator.Register("coingecko", swap.NewCoinGeckoOracle(nil, "", map[string]string{"NHB": "nhb", "ZNHB": "znhb"}))
	node.SetSwapOracle(aggregator)
	node.SetSwapManualOracle(manualOracle)

	node.SetModulePauses(cfg.Global.Pauses)
	node.SetModuleQuotas(map[string]nativecommon.Quota{
		"lending": convertQuota(cfg.Global.Quotas.Lending),
		"swap":    convertQuota(cfg.Global.Quotas.Swap),
		"escrow":  convertQuota(cfg.Global.Quotas.Escrow),
		"trade":   convertQuota(cfg.Global.Quotas.Trade),
		"loyalty": convertQuota(cfg.Global.Quotas.Loyalty),
		"potso":   convertQuota(cfg.Global.Quotas.POTSO),
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	broadcaster := newResilientBroadcaster(ctx)
	baseDir := filepath.Dir(*configFile)
	allowInsecureNetwork, networkDialOpts, err := buildNetworkDialOptions(cfg, baseDir)
	if err != nil {
		panic(fmt.Sprintf("failed to initialise network client security: %v", err))
	}

	go maintainNetworkStream(ctx, *networkAddress, broadcaster, node, allowInsecureNetwork, networkDialOpts, cfg.NetworkSecurity.StreamQueueSize)

	bftEngine := bft.NewEngine(node, privKey, broadcaster)
	node.SetBftEngine(bftEngine)

	grpcListener, err := net.Listen("tcp", *grpcAddress)
	if err != nil {
		panic(fmt.Sprintf("Failed to listen on %s: %v", *grpcAddress, err))
	}
	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(otelgrpc.UnaryServerInterceptor()),
		grpc.ChainStreamInterceptor(otelgrpc.StreamServerInterceptor()),
	)
	srv := service.NewServer(node)
	consensusv1.RegisterConsensusServiceServer(grpcServer, srv)
	consensusv1.RegisterQueryServiceServer(grpcServer, srv)

	go func() {
		if err := grpcServer.Serve(grpcListener); err != nil {
			panic(fmt.Sprintf("gRPC server failed: %v", err))
		}
	}()

	go func() {
		<-ctx.Done()
		grpcServer.GracefulStop()
	}()

	go node.StartConsensus()

	fmt.Println("--- Consensus node initialised and running ---")
	<-ctx.Done()
	fmt.Println("--- Consensus node shutting down ---")
}

const (
	networkReconnectBaseDelay = 500 * time.Millisecond
	networkReconnectMaxDelay  = 30 * time.Second
)

func maintainNetworkStream(ctx context.Context, target string, broadcaster *resilientBroadcaster, node *core.Node, allowInsecure bool, dialOpts []grpc.DialOption, queueSize int) {
	if broadcaster == nil || node == nil {
		return
	}

	backoff := networkReconnectBaseDelay
	for {
		if ctx.Err() != nil {
			return
		}

		dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		client, err := network.Dial(dialCtx, target, allowInsecure, dialOpts...)
		cancel()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to connect to p2pd at %s: %v\n", target, err)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff *= 2
			if backoff > networkReconnectMaxDelay {
				backoff = networkReconnectMaxDelay
			}
			continue
		}

		client.SetSendQueueSize(queueSize)
		broadcaster.SetClient(client)
		backoff = networkReconnectBaseDelay

		streamErr := client.Run(ctx, node.ProcessNetworkMessage, nil)
		broadcaster.SetClient(nil)
		client.Close()
		if streamErr != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "Network stream terminated: %v\n", streamErr)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > networkReconnectMaxDelay {
			backoff = networkReconnectMaxDelay
		}
	}
}

type envLookupFunc func(string) (string, bool)

func buildNetworkDialOptions(cfg *config.Config, baseDir string) (bool, []grpc.DialOption, error) {
	if cfg == nil {
		return true, []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}, nil
	}
	sec := cfg.NetworkSecurity

	secret, err := sec.ResolveSharedSecret(baseDir, os.LookupEnv)
	if err != nil {
		return false, nil, fmt.Errorf("resolve shared secret: %w", err)
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	useTLS := false

	if caPath := resolvePath(baseDir, sec.ServerCAFile); caPath != "" {
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return false, nil, fmt.Errorf("read server CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return false, nil, fmt.Errorf("failed to parse server CA certificates from %s", caPath)
		}
		tlsConfig.RootCAs = pool
		useTLS = true
	}

	certPath := resolvePath(baseDir, sec.ClientTLSCertFile)
	keyPath := resolvePath(baseDir, sec.ClientTLSKeyFile)
	if certPath != "" || keyPath != "" {
		if certPath == "" || keyPath == "" {
			return false, nil, fmt.Errorf("ClientTLSCertFile and ClientTLSKeyFile must both be configured when using client certificates")
		}
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return false, nil, fmt.Errorf("load client TLS keypair: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
		useTLS = true
	}

	if serverName := strings.TrimSpace(sec.ServerName); serverName != "" {
		tlsConfig.ServerName = serverName
		useTLS = true
	}

	if !useTLS && strings.TrimSpace(sec.ServerTLSCertFile) != "" {
		useTLS = true
	}

	allowInsecure := sec.AllowInsecure
	var opts []grpc.DialOption
	if useTLS {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	} else if allowInsecure {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		return false, nil, fmt.Errorf("network security configuration is missing TLS material; set AllowInsecure=true only for development")
	}

	if secret != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(network.NewStaticTokenCredentials(sec.AuthorizationHeaderName(), secret)))
	}

	return allowInsecure, opts, nil
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

func loadValidatorKey(cfg *config.Config) (*crypto.PrivateKey, error) {
	if cfg.ValidatorKMSURI != "" || cfg.ValidatorKMSEnv != "" {
		return loadFromKMS(cfg)
	}

	if cfg.ValidatorKeystorePath == "" {
		return nil, fmt.Errorf("validator keystore path not configured")
	}

	passphrase, ok := os.LookupEnv(validatorPassEnv)
	if !ok {
		return nil, fmt.Errorf("%s environment variable not set", validatorPassEnv)
	}

	key, err := crypto.LoadFromKeystore(cfg.ValidatorKeystorePath, passphrase)
	if err != nil {
		return nil, fmt.Errorf("unable to decrypt keystore %s: %w", cfg.ValidatorKeystorePath, err)
	}
	return key, nil
}

func loadFromKMS(cfg *config.Config) (*crypto.PrivateKey, error) {
	if envName := cfg.ValidatorKMSEnv; envName != "" {
		return keyFromEnv(envName)
	}

	if cfg.ValidatorKMSURI == "" {
		return nil, fmt.Errorf("validator KMS URI not configured")
	}

	parsed, err := url.Parse(cfg.ValidatorKMSURI)
	if err != nil {
		return nil, fmt.Errorf("invalid validator KMS URI: %w", err)
	}

	switch parsed.Scheme {
	case "env":
		return keyFromEnv(parsed.Opaque)
	default:
		return nil, fmt.Errorf("unsupported validator KMS scheme %q", parsed.Scheme)
	}
}

func keyFromEnv(name string) (*crypto.PrivateKey, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("validator KMS environment variable not provided")
	}
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil, fmt.Errorf("validator KMS environment variable %s not set", name)
	}
	return parsePrivateKeyMaterial(value)
}

func parsePrivateKeyMaterial(material string) (*crypto.PrivateKey, error) {
	trimmed := strings.TrimSpace(material)
	trimmed = strings.TrimPrefix(trimmed, "0x")
	if trimmed == "" {
		return nil, fmt.Errorf("empty private key material")
	}
	bytes, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("failed to decode hex private key: %w", err)
	}
	return crypto.PrivateKeyFromBytes(bytes)
}
