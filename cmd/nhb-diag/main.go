// Command nhb-diag is a read-only inspection tool for the ZNHB
// supply-invariant halt investigation (2026-08-21/22). It opens a data
// directory and prints the current ZNHBSalePoolBalance, ZNHBRewardPoolBalance,
// admin wallet BalanceZNHB, and PotsoRewardsLastProcessedEpoch -- nothing is
// ever written. Must be run against a COPY of a live data directory, never
// one a running nhb process has open (LevelDB does not allow concurrent
// writers/readers from separate processes).
package main

import (
	"flag"
	"fmt"
	"os"

	"nhbchain/config"
	"nhbchain/core"
	"nhbchain/core/genesis"
	nhbstate "nhbchain/core/state"
	"nhbchain/native/subscriptions"
	"nhbchain/storage"
	"nhbchain/storage/trie"
)

func main() {
	configFile := flag.String("config", "./config.toml", "Path to the configuration file (only DataDir and GenesisFile are used)")
	flag.Parse()

	cfg, err := config.Load(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to load config: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Opening data directory (read-only intent): %s\n", cfg.DataDir)
	db, err := storage.NewLevelDB(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open database (is another nhb process already using it? this must be a COPY, not the live directory): %v\n", err)
		os.Exit(1)
	}
	defer db.Close()

	chain, err := core.NewBlockchain(db, cfg.GenesisFile, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open blockchain: %v\n", err)
		os.Exit(1)
	}

	header := chain.CurrentHeader()
	if header == nil {
		fmt.Fprintln(os.Stderr, "Error: no committed header found")
		os.Exit(1)
	}
	fmt.Printf("Current committed height: %d\n", header.Height)

	stateTrie, err := trie.NewTrie(db, header.StateRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to open state trie at committed root: %v\n", err)
		os.Exit(1)
	}

	manager := nhbstate.NewManager(stateTrie)

	salePool, err := manager.ZNHBSalePoolBalance()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: ZNHBSalePoolBalance: %v\n", err)
		os.Exit(1)
	}
	rewardPool, err := manager.ZNHBRewardPoolBalance()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: ZNHBRewardPoolBalance: %v\n", err)
		os.Exit(1)
	}
	lastProcessed, ok, err := manager.PotsoRewardsLastProcessedEpoch()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: PotsoRewardsLastProcessedEpoch: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("ZNHBSalePoolBalance:   %s\n", salePool.String())
	fmt.Printf("ZNHBRewardPoolBalance: %s\n", rewardPool.String())
	fmt.Printf("PotsoRewardsLastProcessedEpoch: %d (set=%v)\n", lastProcessed, ok)

	poolsBootstrapped, err := manager.ZNHBPoolsBootstrapped()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: ZNHBPoolsBootstrapped: %v\n", err)
		os.Exit(1)
	}
	driftReconciled, err := manager.ZNHBSupplyDriftReconciled()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: ZNHBSupplyDriftReconciled: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("ZNHBPoolsBootstrapped:     %v\n", poolsBootstrapped)
	fmt.Printf("ZNHBSupplyDriftReconciled: %v\n", driftReconciled)

	var treasuryAddr [20]byte
	var haveTreasury bool
	if masterTreasury := os.Getenv("NHB_MASTER_TREASURY"); masterTreasury != "" {
		if addr, parseErr := genesis.ParseBech32Account(masterTreasury); parseErr == nil {
			treasuryAddr = addr
			haveTreasury = true
			account, acctErr := manager.GetAccount(addr[:])
			if acctErr != nil {
				fmt.Fprintf(os.Stderr, "Error: load admin wallet account: %v\n", acctErr)
				os.Exit(1)
			}
			fmt.Printf("Admin wallet (%s) BalanceZNHB: %s\n", masterTreasury, account.BalanceZNHB.String())
		} else {
			fmt.Printf("Warning: could not parse NHB_MASTER_TREASURY=%q: %v\n", masterTreasury, parseErr)
		}
	}

	lastDay, hasWatermark, err := manager.SubscriptionsLastProcessedDay()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: SubscriptionsLastProcessedDay: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("SubscriptionsLastProcessedDay: %d (set=%v)\n", lastDay, hasWatermark)

	if haveTreasury {
		registry := subscriptions.NewRegistry(manager)
		byPayer, err := registry.ListSubscriptionsByPayer(treasuryAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: ListSubscriptionsByPayer: %v\n", err)
			os.Exit(1)
		}
		byMerchant, err := registry.ListSubscriptionsByMerchant(treasuryAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: ListSubscriptionsByMerchant: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Subscriptions where treasury is PAYER: %d\n", len(byPayer))
		for _, id := range byPayer {
			printSub(registry, id, "payer")
		}
		fmt.Printf("Subscriptions where treasury is MERCHANT: %d\n", len(byMerchant))
		for _, id := range byMerchant {
			printSub(registry, id, "merchant")
		}
	}
}

func printSub(registry *subscriptions.Registry, id subscriptions.SubscriptionID, role string) {
	sub, ok := registry.GetSubscription(id)
	if !ok {
		fmt.Printf("  [%s] subscription %d: not found\n", role, id)
		return
	}
	fmt.Printf(
		"  [%s] subscription %d: asset=%s priceWei=%s status=%v nextChargeAt=%d payer=%x merchant=%x\n",
		role, sub.ID, sub.Asset, sub.PriceWei.String(), sub.Status, sub.NextChargeAt, sub.Payer, sub.Merchant,
	)
}
