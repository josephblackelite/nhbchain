package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"nhbchain/sdk/consensus"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	endpoint := os.Getenv("CONSENSUSD_GRPC_ADDR")
	if endpoint == "" {
		endpoint = "localhost:9090"
	}
	addr := os.Getenv("LENDING_ADDRESS")
	if addr == "" {
		log.Fatal("set LENDING_ADDRESS (bech32 or 0x hex) to query positions")
	}

	client, err := consensus.Dial(ctx, endpoint, consensus.WithInsecure())
	if err != nil {
		log.Fatalf("dial consensus service: %v", err)
	}
	defer client.Close()

	value, _, err := client.QueryState(ctx, "lending", fmt.Sprintf("positions/%s", addr))
	if err != nil {
		log.Fatalf("query positions: %v", err)
	}
	if len(value) == 0 {
		fmt.Println("no active positions for address")
		return
	}
	var decoded []map[string]any
	if err := json.Unmarshal(value, &decoded); err != nil {
		log.Fatalf("decode response: %v", err)
	}
	pretty, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		log.Fatalf("format response: %v", err)
	}
	fmt.Printf("Positions for %s:\n%s\n", addr, string(pretty))
}
