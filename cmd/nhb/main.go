package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"nhbchain/config"
	"nhbchain/consensus/bft"
	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/p2p"
	"nhbchain/rpc"
	"nhbchain/storage"
)

const validatorPassEnv = "NHB_VALIDATOR_PASS"

func main() {
	configFile := flag.String("config", "./config.toml", "Path to the configuration file")
	flag.Parse()

	cfg, err := config.Load(*configFile)
	if err != nil {
		panic(fmt.Sprintf("Failed to load config: %v", err))
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
	node, err := core.NewNode(db, privKey)
	if err != nil {
		panic(fmt.Sprintf("Failed to create node: %v", err))
	}

	// 2. Create the P2P server, passing the node as the MessageHandler.
	p2pServer := p2p.NewServer(cfg.ListenAddress, node)

	// 3. Create the BFT engine, passing the node (as NodeInterface) and P2P server (as Broadcaster).
	bftEngine := bft.NewEngine(node, privKey, p2pServer)

	// 4. Set the fully configured BFT engine on the node.
	node.SetBftEngine(bftEngine)

	// --- Server Startup ---
	rpcServer := rpc.NewServer(node)
	go rpcServer.Start(cfg.RPCAddress)
	go p2pServer.Start()

	for _, peerAddr := range cfg.BootstrapPeers {
		go func(addr string) {
			if err := p2pServer.Connect(addr); err != nil {
				fmt.Printf("Warning: Failed to connect to bootstrap peer %s: %v\n", addr, err)
			}
		}(peerAddr)
	}

	fmt.Println("--- NHBCoin Node Initialized and Running ---")
	go node.StartConsensus()
	select {}
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
