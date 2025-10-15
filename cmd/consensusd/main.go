package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"math/big"
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

	"nhbchain/cmd/internal/passphrase"
	"nhbchain/config"
	"nhbchain/consensus/bft"
	"nhbchain/consensus/service"
	"nhbchain/core"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	nativecommon "nhbchain/native/common"
	"nhbchain/native/lending"
	nativeparams "nhbchain/native/params"
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
	proposalTimeoutEnv  = "NHB_CONSENSUS_TIMEOUT_PROPOSAL"
	prevoteTimeoutEnv   = "NHB_CONSENSUS_TIMEOUT_PREVOTE"
	precommitTimeoutEnv = "NHB_CONSENSUS_TIMEOUT_PRECOMMIT"
	commitTimeoutEnv    = "NHB_CONSENSUS_TIMEOUT_COMMIT"
)

type durationFlag struct {
	value time.Duration
	set   bool
}

func (d *durationFlag) String() string {
	if d == nil {
		return ""
	}
	return d.value.String()
}

func (d *durationFlag) Set(raw string) error {
	parsed, err := time.ParseDuration(strings.TrimSpace(raw))
	if err != nil {
		return err
	}
	d.value = parsed
	d.set = true
	return nil
}

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
	allowMigrateFlag := flag.Bool("allow-migrate", false, "Allow starting with a mismatched state schema (manual migrations only)")
	grpcAddress := flag.String("grpc", "127.0.0.1:9090", "Address for the consensus gRPC server")
	networkAddress := flag.String("p2p", "localhost:9091", "Address of the p2p daemon network service")
	allowInsecureFlag := flag.Bool("allow-insecure", false, "DEV ONLY: permit plaintext loopback connections to p2pd")
	var proposalTimeoutFlag durationFlag
	var prevoteTimeoutFlag durationFlag
	var precommitTimeoutFlag durationFlag
	var commitTimeoutFlag durationFlag
	flag.Var(&proposalTimeoutFlag, "consensus-timeout-proposal", "Time to wait for a proposal before prevoting (e.g. 2s)")
	flag.Var(&prevoteTimeoutFlag, "consensus-timeout-prevote", "Time to wait after prevote before moving to precommit (e.g. 2s)")
	flag.Var(&precommitTimeoutFlag, "consensus-timeout-precommit", "Time to wait after precommit before attempting commit (e.g. 2s)")
	flag.Var(&commitTimeoutFlag, "consensus-timeout-commit", "Total time to wait for commit before starting a new round (e.g. 4s)")
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

	passSource := passphrase.NewSource(validatorPassEnv)

	cfg, err := config.Load(*configFile, config.WithKeystorePassphraseSource(passSource.Get))
	if err != nil {
		panic(fmt.Sprintf("Failed to load config: %v", err))
	}

	if err := config.ValidateConfig(cfg.Global); err != nil {
		log.Fatal("invalid configuration", "err", err)
	}

	consensusTimeouts, err := applyConsensusTimeoutOverrides(cfg.Consensus, proposalTimeoutFlag, prevoteTimeoutFlag, precommitTimeoutFlag, commitTimeoutFlag, os.LookupEnv)
	if err != nil {
		log.Fatal("invalid consensus timeout override", "err", err)
	}
	cfg.Consensus = consensusTimeouts
	if err := config.ValidateConsensus(cfg.Consensus); err != nil {
		log.Fatal("invalid consensus timeouts", "err", err)
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

	privKey, err := loadValidatorKey(cfg, passSource.Get)
	if err != nil {
		panic(fmt.Sprintf("Failed to load validator key: %v", err))
	}

	node, err := core.NewNode(db, privKey, genesisPath, allowAutogenesis, *allowMigrateFlag)
	if err != nil {
		panic(fmt.Sprintf("Failed to create node: %v", err))
	}

	if err := node.SetGlobalConfig(cfg.Global); err != nil {
		log.Fatal("invalid global configuration", "err", err)
	}
	node.SetMempoolUnlimitedOptIn(cfg.Mempool.AllowUnlimited)
	node.SetMempoolLimit(cfg.Mempool.MaxTransactions)

	paymasterLimits, err := cfg.Global.PaymasterLimits()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse paymaster limits: %v", err))
	}
	node.SetPaymasterLimits(core.PaymasterLimits{
		MerchantDailyCapWei: paymasterLimits.MerchantDailyCapWei,
		DeviceDailyTxCap:    paymasterLimits.DeviceDailyTxCap,
		GlobalDailyCapWei:   paymasterLimits.GlobalDailyCapWei,
	})
	autoTopUpCfg, err := cfg.Global.PaymasterAutoTopUpConfig()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse paymaster auto top-up policy: %v", err))
	}
	autoPolicy := core.PaymasterAutoTopUpPolicy{
		Enabled:      autoTopUpCfg.Enabled,
		Token:        autoTopUpCfg.Token,
		Cooldown:     autoTopUpCfg.Cooldown,
		Operator:     autoTopUpCfg.Operator,
		ApproverRole: autoTopUpCfg.ApproverRole,
		MinterRole:   autoTopUpCfg.MinterRole,
	}
	if autoTopUpCfg.MinBalanceWei != nil {
		autoPolicy.MinBalanceWei = new(big.Int).Set(autoTopUpCfg.MinBalanceWei)
	}
	if autoTopUpCfg.TopUpAmountWei != nil {
		autoPolicy.TopUpAmountWei = new(big.Int).Set(autoTopUpCfg.TopUpAmountWei)
	}
	if autoTopUpCfg.DailyCapWei != nil {
		autoPolicy.DailyCapWei = new(big.Int).Set(autoTopUpCfg.DailyCapWei)
	}
	node.SetPaymasterAutoTopUpPolicy(autoPolicy)

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
	if !cfg.Global.Pauses.Staking {
		if err := ensureStakingPauseCleared(node); err != nil {
			panic(fmt.Sprintf("failed to clear staking pause: %v", err))
		}
	}
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
	allowInsecureNetwork, networkDialOpts, err := buildNetworkDialOptions(cfg, baseDir, *networkAddress, *allowInsecureFlag)
	if err != nil {
		panic(fmt.Sprintf("failed to initialise network client security: %v", err))
	}

	go maintainNetworkStream(ctx, *networkAddress, broadcaster, node, allowInsecureNetwork, networkDialOpts, cfg.NetworkSecurity.StreamQueueSize)

	bftEngine := bft.NewEngine(node, privKey, broadcaster, bft.WithTimeouts(bft.TimeoutConfig{
		Proposal:  cfg.Consensus.ProposalTimeout,
		Prevote:   cfg.Consensus.PrevoteTimeout,
		Precommit: cfg.Consensus.PrecommitTimeout,
		Commit:    cfg.Consensus.CommitTimeout,
	}))
	node.SetBftEngine(bftEngine)

	grpcListener, err := net.Listen("tcp", *grpcAddress)
	if err != nil {
		panic(fmt.Sprintf("Failed to listen on %s: %v", *grpcAddress, err))
	}
	serverCreds, serverAuth, err := buildConsensusServerSecurity(cfg, baseDir, *allowInsecureFlag, grpcListener.Addr())
	if err != nil {
		panic(fmt.Sprintf("failed to configure consensus server security: %v", err))
	}

	grpcServer := grpc.NewServer(
		grpc.Creds(serverCreds),
		grpc.ChainUnaryInterceptor(
			service.UnaryAuthInterceptor(serverAuth),
			otelgrpc.UnaryServerInterceptor(),
		),
		grpc.ChainStreamInterceptor(
			service.StreamAuthInterceptor(serverAuth),
			otelgrpc.StreamServerInterceptor(),
		),
	)
	srv := service.NewServer(node, service.WithAuthorizer(serverAuth))
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

func ensureStakingPauseCleared(node *core.Node) error {
	if node == nil {
		return fmt.Errorf("node unavailable")
	}
	return node.WithState(func(manager *nhbstate.Manager) error {
		store := nativeparams.NewStore(manager)
		pauses, err := store.Pauses()
		if err != nil {
			return fmt.Errorf("load staking pause state: %w", err)
		}
		if !pauses.Staking {
			return nil
		}
		pauses.Staking = false
		if err := store.SetPauses(pauses); err != nil {
			return fmt.Errorf("persist staking pause state: %w", err)
		}
		return nil
	})
}

func applyConsensusTimeoutOverrides(base config.Consensus, proposal, prevote, precommit, commit durationFlag, lookup envLookupFunc) (config.Consensus, error) {
	result := base
	var err error
	result.ProposalTimeout, err = resolveDurationOverride(base.ProposalTimeout, proposal, "--consensus-timeout-proposal", proposalTimeoutEnv, lookup)
	if err != nil {
		return config.Consensus{}, err
	}
	result.PrevoteTimeout, err = resolveDurationOverride(base.PrevoteTimeout, prevote, "--consensus-timeout-prevote", prevoteTimeoutEnv, lookup)
	if err != nil {
		return config.Consensus{}, err
	}
	result.PrecommitTimeout, err = resolveDurationOverride(base.PrecommitTimeout, precommit, "--consensus-timeout-precommit", precommitTimeoutEnv, lookup)
	if err != nil {
		return config.Consensus{}, err
	}
	result.CommitTimeout, err = resolveDurationOverride(base.CommitTimeout, commit, "--consensus-timeout-commit", commitTimeoutEnv, lookup)
	if err != nil {
		return config.Consensus{}, err
	}
	return result, nil
}

func resolveDurationOverride(current time.Duration, flag durationFlag, flagName, envKey string, lookup envLookupFunc) (time.Duration, error) {
	if flag.set {
		if flag.value <= 0 {
			return 0, fmt.Errorf("%s must be positive", flagName)
		}
		return flag.value, nil
	}
	if lookup != nil {
		if raw, ok := lookup(envKey); ok {
			trimmed := strings.TrimSpace(raw)
			if trimmed != "" {
				parsed, err := time.ParseDuration(trimmed)
				if err != nil {
					return 0, fmt.Errorf("parse %s: %w", envKey, err)
				}
				if parsed <= 0 {
					return 0, fmt.Errorf("%s must be positive", envKey)
				}
				return parsed, nil
			}
		}
	}
	return current, nil
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

func buildNetworkDialOptions(cfg *config.Config, baseDir string, target string, allowInsecureFlag bool) (bool, []grpc.DialOption, error) {
	if cfg == nil {
		if !allowInsecureFlag {
			return false, nil, fmt.Errorf("plaintext network bridge requires --allow-insecure runtime flag")
		}
		if !isLoopbackTarget(target) {
			return false, nil, fmt.Errorf("plaintext network bridge requires loopback target; refusing %q", target)
		}
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

	allowInsecure := false
	var opts []grpc.DialOption
	if useTLS {
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	} else if sec.AllowInsecure {
		if !allowInsecureFlag {
			return false, nil, fmt.Errorf("plaintext network bridge requires --allow-insecure runtime flag")
		}
		if !isLoopbackTarget(target) {
			return false, nil, fmt.Errorf("plaintext network bridge requires loopback target; refusing %q", target)
		}
		allowInsecure = true
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	} else {
		return false, nil, fmt.Errorf("network security configuration is missing TLS material; set AllowInsecure=false or provide certificates")
	}

	if secret != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(network.NewStaticTokenCredentials(sec.AuthorizationHeaderName(), secret)))
	}

	return allowInsecure, opts, nil
}

func buildConsensusServerSecurity(cfg *config.Config, baseDir string, allowInsecureFlag bool, listener net.Addr) (credentials.TransportCredentials, service.Authorizer, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("missing configuration for consensus security")
	}
	sec := cfg.NetworkSecurity

	secret, err := sec.ResolveSharedSecret(baseDir, os.LookupEnv)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve shared secret: %w", err)
	}

	var auths []network.Authenticator
	if secret != "" {
		auths = append(auths, network.NewTokenAuthenticator(sec.AuthorizationHeaderName(), secret))
	}

	certPath := resolvePath(baseDir, sec.ServerTLSCertFile)
	keyPath := resolvePath(baseDir, sec.ServerTLSKeyFile)

	var tlsConfig *tls.Config
	if certPath != "" || keyPath != "" {
		if certPath == "" || keyPath == "" {
			return nil, nil, fmt.Errorf("consensus security requires both ServerTLSCertFile and ServerTLSKeyFile when enabling TLS")
		}
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err != nil {
			return nil, nil, fmt.Errorf("load consensus TLS keypair: %w", err)
		}
		tlsConfig = &tls.Config{
			MinVersion:   tls.VersionTLS12,
			Certificates: []tls.Certificate{cert},
		}
	}

	if caPath := resolvePath(baseDir, sec.ClientCAFile); caPath != "" {
		if tlsConfig == nil {
			return nil, nil, fmt.Errorf("ClientCAFile requires ServerTLSCertFile and ServerTLSKeyFile to be configured")
		}
		pem, err := os.ReadFile(caPath)
		if err != nil {
			return nil, nil, fmt.Errorf("read client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, nil, fmt.Errorf("failed to parse client CA certificates from %s", caPath)
		}
		tlsConfig.ClientCAs = pool
		tlsConfig.ClientAuth = tls.RequireAndVerifyClientCert
		auths = append(auths, network.NewTLSAuthorizer(sec.AllowedClientCommonNames))
	}

	if len(auths) == 0 {
		return nil, nil, fmt.Errorf("consensus security requires a shared secret or client certificate authentication")
	}

	var creds credentials.TransportCredentials
	switch {
	case tlsConfig != nil:
		creds = credentials.NewTLS(tlsConfig)
	case sec.AllowInsecure:
		if !allowInsecureFlag {
			return nil, nil, fmt.Errorf("plaintext consensus server requires --allow-insecure runtime flag")
		}
		if listener == nil || !isLoopbackListener(listener) {
			return nil, nil, fmt.Errorf("plaintext consensus server is restricted to loopback listeners; refusing %v", listener)
		}
		creds = insecure.NewCredentials()
	default:
		return nil, nil, fmt.Errorf("consensus security requires TLS material; set AllowInsecure=false or provide certificates")
	}

	return creds, network.ChainAuthenticators(auths...), nil
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

func isLoopbackListener(addr net.Addr) bool {
	tcpAddr, ok := addr.(*net.TCPAddr)
	if !ok {
		return false
	}
	ip := tcpAddr.IP
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}

func isLoopbackTarget(target string) bool {
	host := strings.TrimSpace(target)
	if host == "" {
		return false
	}
	// Strip scheme if provided.
	if idx := strings.Index(host, "://"); idx != -1 {
		host = host[idx+3:]
	}
	// Separate host from port for IPv4/hostname style targets.
	if strings.HasPrefix(host, "[") {
		if end := strings.Index(host, "]"); end != -1 {
			host = host[1:end]
		}
	} else if colon := strings.LastIndex(host, ":"); colon != -1 {
		host = host[:colon]
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
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

func loadValidatorKey(cfg *config.Config, resolvePassphrase func() (string, error)) (*crypto.PrivateKey, error) {
	if cfg.ValidatorKMSURI != "" || cfg.ValidatorKMSEnv != "" {
		return loadFromKMS(cfg)
	}

	if cfg.ValidatorKeystorePath == "" {
		return nil, fmt.Errorf("validator keystore path not configured")
	}

	if resolvePassphrase == nil {
		return nil, fmt.Errorf("validator keystore passphrase required; set %s or run interactively", validatorPassEnv)
	}

	passphrase, err := resolvePassphrase()
	if err != nil {
		return nil, fmt.Errorf("failed to obtain validator keystore passphrase: %w", err)
	}
	if strings.TrimSpace(passphrase) == "" {
		return nil, fmt.Errorf("validator keystore passphrase cannot be empty")
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
