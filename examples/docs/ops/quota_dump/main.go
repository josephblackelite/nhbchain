package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"nhbchain/crypto"
	"nhbchain/examples/docs/ops/internal/stateutil"
	nativecommon "nhbchain/native/common"
	systemquotas "nhbchain/native/system/quotas"
)

func main() {
	dbPath := flag.String("db", "./nhb-data", "path to the consensus data directory")
	consensusEndpoint := flag.String("consensus", "localhost:9090", "consensus gRPC endpoint")
	module := flag.String("module", "", "module name (lending|swap|escrow|trade|loyalty|potso)")
	address := flag.String("address", "", "bech32 account address to inspect")
	epoch := flag.Uint64("epoch", 0, "explicit quota epoch (defaults to current epoch)")
	epochSeconds := flag.Uint64("epoch-seconds", 60, "epoch window size in seconds for the module")
	flag.Parse()

	if strings.TrimSpace(*module) == "" {
		log.Fatal("--module is required")
	}
	if strings.TrimSpace(*address) == "" {
		log.Fatal("--address is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	snapshot, err := stateutil.Load(ctx, *dbPath, *consensusEndpoint)
	if err != nil {
		log.Fatalf("load state snapshot: %v", err)
	}
	defer snapshot.Close()

	seconds := *epochSeconds
	if seconds == 0 {
		seconds = 60
	}

	effectiveEpoch := *epoch
	if effectiveEpoch == 0 {
		unix := snapshot.Timestamp.Unix()
		if unix < 0 {
			unix = 0
		}
		effectiveEpoch = uint64(unix) / uint64(seconds)
	}

	acct, err := crypto.DecodeAddress(*address)
	if err != nil {
		log.Fatalf("decode address: %v", err)
	}

	store := systemquotas.NewStore(snapshot.Manager)
	counters, ok, err := store.Load(*module, effectiveEpoch, acct.Bytes())
	if err != nil {
		log.Fatalf("load quota counters: %v", err)
	}

	normalized := strings.ToLower(strings.TrimSpace(*module))
	fmt.Printf("quota snapshot at height %d (%s)\n", snapshot.Height, snapshot.Timestamp.Format(time.RFC3339))
	fmt.Printf("module: %s\n", normalized)
	fmt.Printf("epoch:  %d (window %d seconds)\n", effectiveEpoch, seconds)
	fmt.Printf("address: %s\n", acct.String())

	if !ok {
		fmt.Println("no counters recorded for this address in the selected epoch")
		return
	}

	fmt.Printf("requests used: %d\n", counters.ReqCount)
	fmt.Printf("nhb used:      %d\n", counters.NHBUsed)

	if counters.ReqCount == math.MaxUint32 {
		fmt.Println("warning: request counter saturated (uint32 max)")
	}
	if counters.NHBUsed == math.MaxUint64 {
		fmt.Println("warning: nhb counter saturated (uint64 max)")
	}

	fmt.Printf("raw counters: %+v\n", nativecommon.QuotaNow{ReqCount: counters.ReqCount, NHBUsed: counters.NHBUsed, EpochID: counters.EpochID})
}
