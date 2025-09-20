package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"nhbchain/config"
	"nhbchain/consensus/bft"
	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/p2p"
	"nhbchain/rpc"
	"nhbchain/storage"
)

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

	keyBytes, err := hex.DecodeString(cfg.ValidatorKey)
	if err != nil {
		panic(fmt.Sprintf("Failed to decode validator key: %v", err))
	}
	privKey, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		panic(err)
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
