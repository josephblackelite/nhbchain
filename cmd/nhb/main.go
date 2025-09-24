package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"nhbchain/config"
	"nhbchain/consensus/bft"
	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/p2p"
	"nhbchain/rpc"
	"nhbchain/storage"
)

const (
	validatorPassEnv = "NHB_VALIDATOR_PASS"
	genesisPathEnv   = "NHB_GENESIS"
)

func main() {
	configFile := flag.String("config", "./config.toml", "Path to the configuration file")
	genesisFlag := flag.String("genesis", "", "Path to a genesis block JSON file (overrides NHB_GENESIS and config GenesisFile)")
	allowAutogenesis := flag.Bool("allow-autogenesis", true, "Allow automatic genesis creation when no stored genesis exists")
	flag.Parse()

	cfg, err := config.Load(*configFile)
	if err != nil {
		panic(fmt.Sprintf("Failed to load config: %v", err))
	}

	genesisPath, err := resolveGenesisPath(*genesisFlag, cfg.GenesisFile, *allowAutogenesis, os.LookupEnv)
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

	// 1. Create the core node.
	node, err := core.NewNode(db, privKey, genesisPath, *allowAutogenesis)
	if err != nil {
		panic(fmt.Sprintf("Failed to create node: %v", err))
	}

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

	// 2. Create the P2P server, passing the node as the MessageHandler.
	p2pCfg := p2p.ServerConfig{
		ListenAddress:    cfg.ListenAddress,
		ChainID:          node.ChainID(),
		GenesisHash:      node.GenesisHash(),
		ClientVersion:    cfg.ClientVersion,
		MaxPeers:         cfg.MaxPeers,
		MaxInbound:       cfg.MaxInbound,
		MaxOutbound:      cfg.MaxOutbound,
		Bootnodes:        append([]string{}, cfg.Bootnodes...),
		PersistentPeers:  append([]string{}, cfg.PersistentPeers...),
		PeerBanDuration:  time.Duration(cfg.PeerBanSeconds) * time.Second,
		ReadTimeout:      time.Duration(cfg.ReadTimeout) * time.Second,
		WriteTimeout:     time.Duration(cfg.WriteTimeout) * time.Second,
		MaxMessageBytes:  cfg.MaxMsgBytes,
		RateMsgsPerSec:   cfg.P2P.RateMsgsPerSec,
		RateBurst:        cfg.P2P.Burst,
		BanScore:         cfg.P2P.BanScore,
		GreyScore:        cfg.P2P.GreyScore,
		HandshakeTimeout: time.Duration(cfg.P2P.HandshakeTimeoutMs) * time.Millisecond,
	}
	p2pServer := p2p.NewServer(node, privKey, p2pCfg)
	node.SetP2PServer(p2pServer)

	// 3. Create the BFT engine, passing the node (as NodeInterface) and P2P server (as Broadcaster).
	bftEngine := bft.NewEngine(node, privKey, p2pServer)

	// 4. Set the fully configured BFT engine on the node.
	node.SetBftEngine(bftEngine)

	// --- Server Startup ---
	rpcServer := rpc.NewServer(node)
	go rpcServer.Start(cfg.RPCAddress)
	go p2pServer.Start()

	fmt.Println("--- NHBCoin Node Initialized and Running ---")
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

	return "", fmt.Errorf("no genesis file provided; supply one via --genesis, %s, or config, or enable --allow-autogenesis", genesisPathEnv)
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
