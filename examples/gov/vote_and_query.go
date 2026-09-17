package main

import (
	"context"
	"fmt"
	"log"
	"time"

	govsdk "nhbchain/sdk/gov"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := govsdk.Dial(ctx, "localhost:50061", govsdk.WithInsecure())
	if err != nil {
		log.Fatalf("dial governd: %v", err)
	}
	defer func() { _ = client.Close() }()

	voteMsg, err := govsdk.NewMsgVote("nhb1qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqpkkh94", 42, "yes")
	if err != nil {
		log.Fatalf("build vote: %v", err)
	}
	txHash, err := client.Vote(ctx, voteMsg)
	if err != nil {
		log.Fatalf("submit vote: %v", err)
	}
	fmt.Printf("submitted vote tx %s\n", txHash)

	tallyCtx, cancelTally := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelTally()
	tally, err := client.GetTally(tallyCtx, 42)
	if err != nil {
		log.Fatalf("query tally: %v", err)
	}
	fmt.Printf("proposal %d status %s turnout %d bps yes %d bps no %d bps\n",
		tally.GetProposalId(), tally.GetStatus().String(), tally.GetTurnoutBps(), tally.GetYesPowerBps(), tally.GetNoPowerBps())
}
