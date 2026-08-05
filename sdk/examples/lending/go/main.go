// Command main demonstrates the signed-tx passthrough trust model lendingd
// requires: the caller builds and signs a real on-chain lending transaction
// locally (never handing a private key to lendingd), then hands the signed
// JSON blob to the gRPC service, which relays it to the node's
// nhb_sendTransaction RPC exactly as-is. This mirrors nhbportal's
// walletManager.ts signAndAssembleL1Tx -> sendTransaction pattern.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"nhbchain/core/types"
	"nhbchain/crypto"
	goclient "nhbchain/sdk/go/client"
	"nhbchain/sdk/lending"
)

func main() {
	lendingEndpoint := flag.String("lending-endpoint", "localhost:9090", "lendingd gRPC endpoint (host:port)")
	nodeEndpoint := flag.String("node-endpoint", "http://localhost:8080", "node JSON-RPC endpoint, used only to look up the account nonce")
	keyHex := flag.String("key", "", "hex-encoded secp256k1 private key to sign with (required)")
	market := flag.String("market", "default", "lending pool id")
	supplyAmt := flag.String("supply", "500", "amount to supply in base units")
	borrowAmt := flag.String("borrow", "100", "amount to borrow in base units")
	repayAmt := flag.String("repay", "50", "amount to repay in base units")
	insecure := flag.Bool("insecure", true, "dial the lendingd gRPC endpoint without TLS (development only)")
	timeout := flag.Duration("timeout", 5*time.Second, "per-RPC timeout")
	flag.Parse()

	if *keyHex == "" {
		log.Fatalf("a -key is required: lendingd never signs on the caller's behalf, so the caller must supply their own key to build each transaction")
	}
	keyBytes, err := hex.DecodeString(strings.TrimPrefix(*keyHex, "0x"))
	if err != nil {
		log.Fatalf("decode private key hex: %v", err)
	}
	key, err := crypto.PrivateKeyFromBytes(keyBytes)
	if err != nil {
		log.Fatalf("parse private key: %v", err)
	}
	account, err := lending.SenderAddress(key)
	if err != nil {
		log.Fatalf("derive sender address: %v", err)
	}

	ctx := context.Background()

	// Only used to fetch the account's current nonce before signing each
	// transaction -- it never sees the private key.
	node, err := goclient.New(*nodeEndpoint)
	if err != nil {
		log.Fatalf("dial node rpc: %v", err)
	}

	dialOpts := []lending.DialOption{}
	if *insecure {
		dialOpts = append(dialOpts, lending.WithInsecure())
	}
	connCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	client, err := lending.Dial(connCtx, *lendingEndpoint, dialOpts...)
	if err != nil {
		log.Fatalf("dial lending service: %v", err)
	}
	defer client.Close()

	fmt.Printf("Connected to lendingd at %s (account %s)\n", *lendingEndpoint, account)

	listCtx, cancelList := context.WithTimeout(ctx, *timeout)
	markets, err := client.ListMarkets(listCtx)
	cancelList()
	if err != nil {
		log.Fatalf("list markets: %v", err)
	}
	fmt.Println("Available markets:")
	for _, m := range markets {
		fmt.Printf("- %s (%s)\n", m.GetKey().GetSymbol(), m.GetBaseAsset())
	}

	supplyHash, err := signAndSupply(ctx, node, client, key, account, *market, *supplyAmt, *timeout)
	if err != nil {
		log.Fatalf("supply asset: %v", err)
	}
	fmt.Printf("Supplied %s %s (tx %s)\n", *supplyAmt, *market, supplyHash)

	borrowHash, err := signAndBorrow(ctx, node, client, key, account, *market, *borrowAmt, *timeout)
	if err != nil {
		log.Fatalf("borrow asset: %v", err)
	}
	fmt.Printf("Borrowed %s %s (tx %s)\n", *borrowAmt, *market, borrowHash)

	repayHash, err := signAndRepay(ctx, node, client, key, account, *market, *repayAmt, *timeout)
	if err != nil {
		log.Fatalf("repay asset: %v", err)
	}
	fmt.Printf("Repaid %s %s (tx %s)\n", *repayAmt, *market, repayHash)

	// Mutations are asynchronous (mempool-accepted, not yet confirmed), so
	// GetPosition here may still reflect pre-transaction state -- in a real
	// integration, wait for confirmation (or poll) before reading it back.
	posCtx, cancelPos := context.WithTimeout(ctx, *timeout)
	position, err := client.GetPosition(posCtx, account)
	cancelPos()
	if err != nil {
		log.Fatalf("get position: %v", err)
	}
	fmt.Println("Account position (may still be pending confirmation):")
	fmt.Printf("  Supplied: %s\n", position.GetSupplied())
	fmt.Printf("  Borrowed: %s\n", position.GetBorrowed())
	fmt.Printf("  Collateral: %s\n", position.GetCollateral())
	fmt.Printf("  Health factor: %s\n", position.GetHealthFactor())
}

func signAndSupply(ctx context.Context, node *goclient.Client, client *lending.Client, key *crypto.PrivateKey, account, market, amount string, timeout time.Duration) (string, error) {
	nonceCtx, cancel := context.WithTimeout(ctx, timeout)
	nonce, err := node.AccountNonce(nonceCtx, account)
	cancel()
	if err != nil {
		return "", fmt.Errorf("fetch nonce: %w", err)
	}
	tx, err := lending.NewSupplyTx(types.NHBChainID(), nonce, market, amount)
	if err != nil {
		return "", err
	}
	signed, err := lending.SignAndEncode(tx, key.PrivateKey)
	if err != nil {
		return "", err
	}
	callCtx, cancelCall := context.WithTimeout(ctx, timeout)
	defer cancelCall()
	return client.SupplyAsset(callCtx, account, market, amount, signed)
}

func signAndBorrow(ctx context.Context, node *goclient.Client, client *lending.Client, key *crypto.PrivateKey, account, market, amount string, timeout time.Duration) (string, error) {
	nonceCtx, cancel := context.WithTimeout(ctx, timeout)
	nonce, err := node.AccountNonce(nonceCtx, account)
	cancel()
	if err != nil {
		return "", fmt.Errorf("fetch nonce: %w", err)
	}
	tx, err := lending.NewBorrowTx(types.NHBChainID(), nonce, market, amount)
	if err != nil {
		return "", err
	}
	signed, err := lending.SignAndEncode(tx, key.PrivateKey)
	if err != nil {
		return "", err
	}
	callCtx, cancelCall := context.WithTimeout(ctx, timeout)
	defer cancelCall()
	return client.BorrowAsset(callCtx, account, market, amount, signed)
}

func signAndRepay(ctx context.Context, node *goclient.Client, client *lending.Client, key *crypto.PrivateKey, account, market, amount string, timeout time.Duration) (string, error) {
	nonceCtx, cancel := context.WithTimeout(ctx, timeout)
	nonce, err := node.AccountNonce(nonceCtx, account)
	cancel()
	if err != nil {
		return "", fmt.Errorf("fetch nonce: %w", err)
	}
	tx, err := lending.NewRepayTx(types.NHBChainID(), nonce, market, amount)
	if err != nil {
		return "", err
	}
	signed, err := lending.SignAndEncode(tx, key.PrivateKey)
	if err != nil {
		return "", err
	}
	callCtx, cancelCall := context.WithTimeout(ctx, timeout)
	defer cancelCall()
	return client.RepayAsset(callCtx, account, market, amount, signed)
}
