package main

import (
	"context"
	"fmt"
	"log"
	"time"

	consensusv1 "nhbchain/proto/consensus/v1"
	"nhbchain/sdk/consensus"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := consensus.Dial(ctx, "localhost:50051")
	if err != nil {
		log.Fatalf("dial consensus: %v", err)
	}
	defer client.Close()

	height, err := client.GetHeight(ctx)
	if err != nil {
		log.Fatalf("fetch height: %v", err)
	}
	fmt.Printf("current height: %d\n", height)

	txs, err := client.GetMempool(ctx)
	if err != nil {
		log.Fatalf("fetch mempool: %v", err)
	}
	fmt.Printf("mempool txs: %d\n", len(txs))

	// Construct a placeholder transaction to demonstrate request wiring.
	demoTx := &consensusv1.Transaction{Nonce: 1}
	if err := client.SubmitTransaction(ctx, demoTx); err != nil {
		fmt.Printf("submit transaction failed (expected against mock server): %v\n", err)
	}
}
