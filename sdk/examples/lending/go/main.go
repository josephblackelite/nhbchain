package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"nhbchain/sdk/lending"
)

func main() {
	endpoint := flag.String("endpoint", "localhost:9090", "lending gRPC endpoint (host:port)")
	account := flag.String("account", "nhb1exampleaccount", "account address to act on")
	market := flag.String("market", "nhb", "market symbol (e.g. nhb)")
	supplyAmt := flag.String("supply", "500", "amount to supply in base units")
	borrowAmt := flag.String("borrow", "100", "amount to borrow in base units")
	repayAmt := flag.String("repay", "50", "amount to repay in base units")
	insecure := flag.Bool("insecure", true, "dial without TLS (development only)")
	timeout := flag.Duration("timeout", 5*time.Second, "per-RPC timeout")
	flag.Parse()

	ctx := context.Background()

	dialOpts := []lending.DialOption{}
	if *insecure {
		dialOpts = append(dialOpts, lending.WithInsecure())
	}

	connCtx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()

	client, err := lending.Dial(connCtx, *endpoint, dialOpts...)
	if err != nil {
		log.Fatalf("dial lending service: %v", err)
	}
	defer client.Close()

	fmt.Printf("Connected to %s\n", *endpoint)

	listCtx, cancelList := context.WithTimeout(ctx, *timeout)
	markets, err := client.ListMarkets(listCtx)
	cancelList()
	if err != nil {
		log.Fatalf("list markets: %v", err)
	}
	fmt.Printf("Available markets:\n")
	for _, m := range markets {
		fmt.Printf("- %s (%s)\n", m.GetKey().GetSymbol(), m.GetBaseAsset())
	}
	if len(markets) == 0 {
		fmt.Println("(none returned – check node configuration)")
	}

	supplyCtx, cancelSupply := context.WithTimeout(ctx, *timeout)
	supplied, err := client.SupplyAsset(supplyCtx, *account, *market, *supplyAmt)
	cancelSupply()
	if err != nil {
		log.Fatalf("supply asset: %v", err)
	}
	fmt.Printf(
		"Supplied %s %s. Total supplied: %s (health factor: %s)\n",
		*supplyAmt,
		*market,
		supplied.GetSupplied(),
		supplied.GetHealthFactor(),
	)

	borrowCtx, cancelBorrow := context.WithTimeout(ctx, *timeout)
	borrowed, err := client.BorrowAsset(borrowCtx, *account, *market, *borrowAmt)
	cancelBorrow()
	if err != nil {
		log.Fatalf("borrow asset: %v", err)
	}
	fmt.Printf("Borrowed %s. Total borrowed: %s\n", *borrowAmt, borrowed.GetBorrowed())

	repayCtx, cancelRepay := context.WithTimeout(ctx, *timeout)
	repaid, err := client.RepayAsset(repayCtx, *account, *market, *repayAmt)
	cancelRepay()
	if err != nil {
		log.Fatalf("repay asset: %v", err)
	}
	fmt.Printf("Repaid %s. Total borrowed: %s\n", *repayAmt, repaid.GetBorrowed())

	posCtx, cancelPos := context.WithTimeout(ctx, *timeout)
	position, err := client.GetPosition(posCtx, *account)
	cancelPos()
	if err != nil {
		log.Fatalf("get position: %v", err)
	}

	fmt.Println("Final account position:")
	fmt.Printf("  Supplied: %s\n", position.GetSupplied())
	fmt.Printf("  Borrowed: %s\n", position.GetBorrowed())
	fmt.Printf("  Collateral: %s\n", position.GetCollateral())
	fmt.Printf("  Health factor: %s\n", position.GetHealthFactor())
}
