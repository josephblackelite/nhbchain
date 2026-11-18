package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"nhbchain/crypto"
	"nhbchain/rpc/txchainid"
	cons "nhbchain/sdk/consensus"
	lendtx "nhbchain/sdk/lending"
)

// envelopeChainID returns the decimal chain id for the envelope: the one the node reports as txChainId in the public
// nhb_getNetworkStats call (NHB_CHAIN_ID, when set, is an explicit choice). If the node cannot say, nothing is signed.
func envelopeChainID(ctx context.Context, explicit, rpcURL string) (string, error) {
	if v := strings.TrimSpace(explicit); v != "" {
		return v, nil
	}
	id, err := txchainid.NewReader(txchainid.HTTPFetcher(nil, rpcURL, ""), 0).ChainID(ctx)
	if err != nil {
		return "", fmt.Errorf("read the chain id from %s: %w", rpcURL, err)
	}
	return id.String(), nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := cons.Dial(ctx, "localhost:9090", cons.WithInsecure())
	if err != nil {
		log.Fatalf("dial consensus: %v", err)
	}
	defer func() { _ = client.Close() }()

	// Load or generate the account key used to authorise the transaction.
	key, err := crypto.GeneratePrivateKey()
	if err != nil {
		log.Fatalf("generate key: %v", err)
	}

	// Construct the module payload using the lending helpers. All numeric amounts
	// are represented as strings to avoid floating point ambiguity.
	borrowMsg, err := lendtx.NewMsgBorrow(
		key.PubKey().Address().String(),
		"usd-pool-1",
		"1000000", // 1.0 units at 6 decimals
		key.PubKey().Address().String(),
	)
	if err != nil {
		log.Fatalf("build borrow msg: %v", err)
	}

	rpcURL := os.Getenv("NHB_RPC_URL")
	if rpcURL == "" {
		rpcURL = "http://localhost:8545"
	}
	chainID, err := envelopeChainID(ctx, os.Getenv("NHB_CHAIN_ID"), rpcURL)
	if err != nil {
		log.Fatal(err)
	}

	// Wrap the payload in an envelope carrying replay protection metadata.
	envelope, err := cons.NewTx(borrowMsg, 1, chainID, "", "", "", "sdk borrow example")
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

	raw, err := proto.Marshal(signed)
	if err != nil {
		log.Fatalf("marshal signed tx: %v", err)
	}
	hash := sha256.Sum256(raw)
	fmt.Printf("broadcast borrowing tx %x\n", hash[:])
}
