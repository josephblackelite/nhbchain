package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"nhbchain/config"
	"nhbchain/examples/docs/ops/internal/stateutil"
)

func main() {
	dbPath := flag.String("db", "./nhb-data", "path to the consensus data directory")
	consensusEndpoint := flag.String("consensus", "localhost:9090", "consensus gRPC endpoint")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	snapshot, err := stateutil.Load(ctx, *dbPath, *consensusEndpoint)
	if err != nil {
		log.Fatalf("load state snapshot: %v", err)
	}
	defer snapshot.Close()

	fmt.Printf("state height %d (%s)\n", snapshot.Height, snapshot.Timestamp.Format(time.RFC3339))

	raw, ok, err := snapshot.Manager.ParamStoreGet("system/pauses")
	if err != nil {
		log.Fatalf("read pause configuration: %v", err)
	}
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		fmt.Println("no pause overrides set (all modules active)")
		return
	}

	var pauses config.Pauses
	if err := json.Unmarshal(raw, &pauses); err != nil {
		log.Fatalf("decode pause payload: %v", err)
	}

	fmt.Println("module pause status:")
	fmt.Printf("- lending:       %t\n", pauses.Lending)
	fmt.Printf("- swap:          %t\n", pauses.Swap)
	fmt.Printf("- escrow:        %t\n", pauses.Escrow)
	fmt.Printf("- trade:         %t\n", pauses.Trade)
	fmt.Printf("- loyalty:       %t\n", pauses.Loyalty)
	fmt.Printf("- potso:         %t\n", pauses.POTSO)
	fmt.Printf("- transfer_nhb:  %t\n", pauses.TransferNHB)
	fmt.Printf("- transfer_znhb: %t\n", pauses.TransferZNHB)
}
