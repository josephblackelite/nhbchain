package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"nhbchain/crypto"
	cons "nhbchain/sdk/consensus"
	"nhbchain/sdk/lending"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	endpoint := os.Getenv("CONSENSUSD_GRPC_ADDR")
	if endpoint == "" {
		endpoint = "localhost:9090"
	}

	client, err := cons.Dial(ctx, endpoint, cons.WithInsecure())
	if err != nil {
		log.Fatalf("dial consensus: %v", err)
	}
	defer client.Close()

	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		log.Fatalf("generate key: %v", err)
	}
	sender := key.PubKey().Address().String()

	supplyMsg, err := lending.NewMsgSupply(sender, "usd-pool-1", "1000000")
	if err != nil {
		log.Fatalf("build supply msg: %v", err)
	}

	envelope, err := cons.NewTx(supplyMsg, 1, "localnet", "1000", "znhb", sender, "first transaction demo")
	if err != nil {
		log.Fatalf("build envelope: %v", err)
	}

	signed, err := cons.Sign(envelope, key)
	if err != nil {
		log.Fatalf("sign envelope: %v", err)
	}

	if err := client.SubmitEnvelope(ctx, signed); err != nil {
		log.Fatalf("submit envelope: %v", err)
	}
	fmt.Printf("broadcasted supply from %s to pool %s\n", sender, supplyMsg.GetPoolId())
}
