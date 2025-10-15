package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nhbchain/cmd/internal/passphrase"
	"nhbchain/config"
	"nhbchain/consensus/bft"
	"nhbchain/core"
	"nhbchain/core/genesis"
	nhbstate "nhbchain/core/state"
	"nhbchain/crypto"
	"nhbchain/native/lending"
	nativeparams "nhbchain/native/params"
	swap "nhbchain/native/swap"
	"nhbchain/observability/logging"
	"nhbchain/p2p"
	"nhbchain/p2p/seeds"
	"nhbchain/rpc"
	"nhbchain/storage"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

const (
	validatorPassEnv    = "NHB_VALIDATOR_PASS"
	genesisPathEnv      = "NHB_GENESIS"
	allowAutogenesisEnv = "NHB_ALLOW_AUTOGENESIS"
)

func main() {
	configFile := flag.String("config", "./config.toml", "Path to the configuration file")
	genesisFlag := flag.String("genesis", "", "Path to a genesis block JSON file (overrides NHB_GENESIS and config GenesisFile)")
	allowAutogenesisFlag := flag.Bool("allow-autogenesis", false, "DEV ONLY: allow automatic genesis creation when no stored genesis exists")
	allowMigrateFlag := flag.Bool("allow-migrate", false, "Allow starting with a mismatched state schema (manual migrations only)")
	flag.Parse()

	allowAutogenesisCLISet := flagWasProvided("allow-autogenesis")

	env := strings.TrimSpace(os.Getenv("NHB_ENV"))
	logger := logging.Setup("nhb", env)

	passSource := passphrase.NewSource(validatorPassEnv)

	cfg, err := config.Load(*configFile, config.WithKeystorePassphraseSource(passSource.Get))
	if err != nil {
		panic(fmt.Sprintf("Failed to load config: %v", err))
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
		panic(fmt.Sprintf("Failed to open database: %v", err))
	}
	defer db.Close()

	privKey, err := loadValidatorKey(cfg, passSource.Get)
	if err != nil {
		panic(fmt.Sprintf("Failed to load validator key: %v", err))
	}

	validatorAddr := privKey.PubKey().Address()
	pubKeyBytes := ethcrypto.FromECDSAPub(privKey.PubKey().PublicKey)
	pubKeyHex := hex.EncodeToString(pubKeyBytes)

	trimmedGenesis := strings.TrimSpace(genesisPath)
	if trimmedGenesis != "" {
		spec, err := genesis.LoadGenesisSpec(trimmedGenesis)
		if err != nil {
			panic(fmt.Sprintf("Failed to load genesis spec: %v", err))
		}
		info := genesis.ValidatorAutoPopulateInfo{
			Address: validatorAddr.String(),
			PubKey:  pubKeyHex,
		}
		if _, err := spec.ResolveValidatorAutoPopulate(&info); err != nil {
			panic(fmt.Sprintf("Failed to resolve genesis validator entry: %v", err))
		}
		if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
			panic(fmt.Sprintf("Failed to prepare data directory for genesis spec: %v", err))
		}
		resolvedPath := filepath.Join(cfg.DataDir, "genesis.resolved.json")
		data, err := json.MarshalIndent(spec, "", "  ")
		if err != nil {
			panic(fmt.Sprintf("Failed to encode resolved genesis spec: %v", err))
		}
		if err := os.WriteFile(resolvedPath, data, 0o644); err != nil {
			panic(fmt.Sprintf("Failed to write resolved genesis spec: %v", err))
		}
		genesisPath = resolvedPath
	} else {
		genesisPath = ""
	}

	peerstoreDir := filepath.Join(cfg.DataDir, "p2p")
	if err := os.MkdirAll(peerstoreDir, 0o755); err != nil {
		panic(fmt.Sprintf("Failed to prepare p2p directory: %v", err))
	}
	peerstorePath := filepath.Join(peerstoreDir, "peerstore")
	peerstore, err := p2p.NewPeerstore(peerstorePath, 0, 0)
	if err != nil {
		panic(fmt.Sprintf("Failed to open peerstore: %v", err))
	}
	defer peerstore.Close()

	identityPath := filepath.Join(peerstoreDir, "node_key.json")
	identity, err := p2p.LoadOrCreateIdentity(identityPath)
	if err != nil {
		panic(fmt.Sprintf("Failed to load node identity: %v", err))
	}

	// 1. Create the core node.
	node, err := core.NewNode(db, privKey, genesisPath, allowAutogenesis, *allowMigrateFlag)
	if err != nil {
		panic(fmt.Sprintf("Failed to create node: %v", err))
	}

	if err := node.SetGlobalConfig(cfg.Global); err != nil {
		panic(fmt.Sprintf("Failed to apply global config: %v", err))
	}
	node.SetMempoolUnlimitedOptIn(cfg.Mempool.AllowUnlimited)
	node.SetMempoolLimit(cfg.Mempool.MaxTransactions)
	node.SetModulePauses(cfg.Global.Pauses)
	if !cfg.Global.Pauses.Staking {
		if err := ensureStakingPauseCleared(node); err != nil {
			panic(fmt.Sprintf("Failed to clear staking pause: %v", err))
		}
	}

	if err := node.SyncStakingParams(); err != nil {
		panic(fmt.Sprintf("Failed to apply staking params: %v", err))
	}

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
	sanctionsParams, err := swapCfg.Sanctions.Parameters()
	if err != nil {
		panic(fmt.Sprintf("Failed to parse swap sanctions config: %v", err))
	}
	node.SetSwapSanctionsChecker(sanctionsParams.Checker())

	// 2. Create the P2P server, passing the node as the MessageHandler.
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
		node := strings.TrimSpace(nodePart)
		addr := strings.TrimSpace(addrPart)
		if node == "" || addr == "" {
			logger.Warn("Ignoring seed due to empty components",
				logging.MaskField("seed", trimmed),
				slog.String("reason", "empty components"))
			continue
		}
		key := strings.ToLower(node) + "@" + strings.ToLower(addr)
		if _, ok := seenSeeds[key]; ok {
			continue
		}
		seenSeeds[key] = struct{}{}
		seedStrings = append(seedStrings, fmt.Sprintf("%s@%s", node, addr))
		seedOrigins = append(seedOrigins, p2p.SeedOrigin{NodeID: node, Address: addr, Source: "config"})
	}

	var seedRegistry *seeds.Registry
	if rawRegistry, ok, err := node.NetworkSeedsParam(); err != nil {
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

	pexEnabled := true
	if cfg.P2P.PEX != nil {
		pexEnabled = *cfg.P2P.PEX
	}
	p2pCfg := p2p.ServerConfig{
		ListenAddress:    cfg.ListenAddress,
		ChainID:          node.ChainID(),
		GenesisHash:      node.GenesisHash(),
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
	p2pServer := p2p.NewServer(node, identity.PrivateKey, p2pCfg)
	p2pServer.SetPeerstore(peerstore)

	// 3. Create the BFT engine, passing the node (as NodeInterface) and P2P server (as Broadcaster).
	bftEngine := bft.NewEngine(node, privKey, p2pServer, bft.WithTimeouts(bft.TimeoutConfig{
		Proposal:  cfg.Consensus.ProposalTimeout,
		Prevote:   cfg.Consensus.PrevoteTimeout,
		Precommit: cfg.Consensus.PrecommitTimeout,
		Commit:    cfg.Consensus.CommitTimeout,
	}))

	// 4. Set the fully configured BFT engine on the node.
	node.SetBftEngine(bftEngine)

	// --- Server Startup ---
	networkAdapter := &p2pNetworkAdapter{server: p2pServer}
	rpcServer, err := rpc.NewServer(node, networkAdapter, rpc.ServerConfig{
		TrustProxyHeaders: cfg.RPCTrustProxyHeaders,
		TrustedProxies:    append([]string{}, cfg.RPCTrustedProxies...),
		AllowlistCIDRs:    append([]string{}, cfg.RPCAllowlistCIDRs...),
		ProxyHeaders: rpc.ProxyHeadersConfig{
			XForwardedFor: rpc.ProxyHeaderMode(strings.TrimSpace(cfg.RPCProxyHeaders.XForwardedFor)),
			XRealIP:       rpc.ProxyHeaderMode(strings.TrimSpace(cfg.RPCProxyHeaders.XRealIP)),
		},
		JWT: rpc.JWTConfig{
			Enable:           cfg.RPCJWT.Enable,
			Alg:              cfg.RPCJWT.Alg,
			HSSecretEnv:      cfg.RPCJWT.HSSecretEnv,
			RSAPublicKeyFile: cfg.RPCJWT.RSAPublicKeyFile,
			Issuer:           cfg.RPCJWT.Issuer,
			Audience:         append([]string{}, cfg.RPCJWT.Audience...),
			MaxSkewSeconds:   cfg.RPCJWT.MaxSkewSeconds,
		},
		ReadHeaderTimeout:        time.Duration(cfg.RPCReadHeaderTimeout) * time.Second,
		ReadTimeout:              time.Duration(cfg.RPCReadTimeout) * time.Second,
		WriteTimeout:             time.Duration(cfg.RPCWriteTimeout) * time.Second,
		IdleTimeout:              time.Duration(cfg.RPCIdleTimeout) * time.Second,
		MaxTxPerWindow:           cfg.RPCMaxTxPerWindow,
		RateLimitWindow:          time.Duration(cfg.RPCRateLimitWindow) * time.Second,
		TLSCertFile:              cfg.RPCTLSCertFile,
		TLSKeyFile:               cfg.RPCTLSKeyFile,
		TLSClientCAFile:          cfg.RPCTLSClientCAFile,
		AllowInsecure:            cfg.RPCAllowInsecure,
		AllowInsecureUnspecified: cfg.RPCAllowInsecureUnspecified,
	})
	if err != nil {
		logger.Error("failed to initialise RPC server", slog.Any("error", err))
		os.Exit(1)
	}
	rpcErrCh := make(chan error, 1)
	go func() {
		err := rpcServer.Start(cfg.RPCAddress)
		rpcErrCh <- err
		close(rpcErrCh)
	}()

	if err := waitForRPCStartup(cfg.RPCAddress, rpcErrCh, 5*time.Second); err != nil {
		logger.Error("RPC server failed to start", slog.Any("error", err))
		os.Exit(1)
	}

	go func() {
		if err, ok := <-rpcErrCh; ok && err != nil {
			logger.Error("RPC server terminated", slog.Any("error", err))
		}
	}()

	go p2pServer.Start()

	logger.Info("NHBCoin node initialised and running")
	go node.StartConsensus()
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

func waitForRPCStartup(addr string, errCh <-chan error, timeout time.Duration) error {
	dialAddr := dialAddressFor(addr)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case err, ok := <-errCh:
			if !ok {
				return fmt.Errorf("RPC server terminated before startup confirmation")
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("RPC server exited before startup confirmation")
		default:
		}

		conn, err := net.DialTimeout("tcp", dialAddr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}

		select {
		case err, ok := <-errCh:
			if !ok {
				return fmt.Errorf("RPC server terminated before startup confirmation")
			}
			if err != nil {
				return err
			}
			return fmt.Errorf("RPC server exited before startup confirmation")
		case <-ticker.C:
		case <-deadline.C:
			return fmt.Errorf("timed out waiting for RPC server to start on %s", addr)
		}
	}
}

func dialAddressFor(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
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

type p2pNetworkAdapter struct {
	server *p2p.Server
}

func (a *p2pNetworkAdapter) NetworkView(ctx context.Context) (p2p.NetworkView, []string, error) {
	if a == nil || a.server == nil {
		return p2p.NetworkView{}, nil, fmt.Errorf("p2p server unavailable")
	}
	return a.server.SnapshotNetwork(), a.server.ListenAddresses(), nil
}

func (a *p2pNetworkAdapter) NetworkPeers(ctx context.Context) ([]p2p.PeerNetInfo, error) {
	if a == nil || a.server == nil {
		return nil, fmt.Errorf("p2p server unavailable")
	}
	return a.server.NetPeers(), nil
}

func (a *p2pNetworkAdapter) Dial(ctx context.Context, target string) error {
	if a == nil || a.server == nil {
		return fmt.Errorf("p2p server unavailable")
	}
	return a.server.DialPeer(target)
}

func (a *p2pNetworkAdapter) Ban(ctx context.Context, nodeID string, duration time.Duration) error {
	if a == nil || a.server == nil {
		return fmt.Errorf("p2p server unavailable")
	}
	return a.server.BanPeer(nodeID, duration)
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

func loadFromKMS(cfg *config.Config) (*crypto.PrivateKey, error) {
	if envName := cfg.ValidatorKMSEnv; envName != "" {
		return keyFromEnv(envName)
	}

	if uri := cfg.ValidatorKMSURI; uri != "" {
		parsed, err := url.Parse(uri)
		if err != nil {
			return nil, fmt.Errorf("invalid KMS URI %q: %w", uri, err)
		}

		switch parsed.Scheme {
		case "env":
			target := parsed.Host
			if target == "" {
				target = strings.TrimPrefix(parsed.Path, "/")
			}
			if target == "" {
				return nil, fmt.Errorf("invalid env URI %q", uri)
			}
			return keyFromEnv(target)
		default:
			return nil, fmt.Errorf("unsupported KMS URI scheme %q", parsed.Scheme)
		}
	}

	return nil, fmt.Errorf("no KMS configuration provided")
}

func keyFromEnv(name string) (*crypto.PrivateKey, error) {
	value, ok := os.LookupEnv(name)
	if !ok {
		return nil, fmt.Errorf("environment variable %q not set", name)
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
