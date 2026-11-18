package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"nhbchain/config"
	"nhbchain/examples/docs/ops/internal/stateutil"
	govv1 "nhbchain/proto/gov/v1"
	govsdk "nhbchain/sdk/gov"
)

func main() {
	dbPath := flag.String("db", "./nhb-data", "path to the consensus data directory")
	consensusEndpoint := flag.String("consensus", "localhost:9090", "consensus gRPC endpoint")
	govEndpoint := flag.String("governance", "localhost:50061", "governd gRPC endpoint")
	authority := flag.String("authority", "", "governance authority address")
	module := flag.String("module", "", "module to toggle (lending|swap|escrow|trade|loyalty|potso); the SetPauses message carries these six only")
	state := flag.String("state", "pause", "target state: pause or resume")
	flag.Parse()

	if strings.TrimSpace(*authority) == "" {
		log.Fatal("--authority is required")
	}
	if strings.TrimSpace(*module) == "" {
		log.Fatal("--module is required")
	}

	desiredPause, err := parseState(*state)
	if err != nil {
		log.Fatalf("invalid state: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	snapshot, err := stateutil.Load(ctx, *dbPath, *consensusEndpoint)
	cancel()
	if err != nil {
		log.Fatalf("load state snapshot: %v", err)
	}
	defer snapshot.Close()

	pauses, err := currentPauses(snapshot.Manager)
	if err != nil {
		log.Fatalf("load pause configuration: %v", err)
	}

	if err := setModulePause(&pauses, *module, desiredPause); err != nil {
		log.Fatal(err)
	}

	payload, err := buildPausesPayload(pauses)
	if err != nil {
		log.Fatal(err)
	}

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 5*time.Second)
	client, err := govsdk.Dial(dialCtx, *govEndpoint, govsdk.WithInsecure())
	dialCancel()
	if err != nil {
		log.Fatalf("dial governance service: %v", err)
	}
	defer client.Close()

	sendCtx, sendCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer sendCancel()

	msg, err := govsdk.NewMsgSetPauses(strings.TrimSpace(*authority), payload)
	if err != nil {
		log.Fatalf("build pause message: %v", err)
	}

	txHash, err := client.SetPauses(sendCtx, msg)
	if err != nil {
		log.Fatalf("broadcast pause transaction: %v", err)
	}

	fmt.Printf("updated %s pause=%t via tx %s\n", strings.ToLower(strings.TrimSpace(*module)), desiredPause, txHash)
}

func parseState(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "pause", "paused", "on":
		return true, nil
	case "resume", "unpause", "off":
		return false, nil
	default:
		return false, fmt.Errorf("unknown state %q", value)
	}
}

func currentPauses(manager interface {
	ParamStoreGet(name string) ([]byte, bool, error)
}) (config.Pauses, error) {
	var pauses config.Pauses
	raw, ok, err := manager.ParamStoreGet("system/pauses")
	if err != nil {
		return pauses, err
	}
	if !ok || len(bytes.TrimSpace(raw)) == 0 {
		return pauses, nil
	}
	if err := json.Unmarshal(raw, &pauses); err != nil {
		return config.Pauses{}, fmt.Errorf("decode pause payload: %w", err)
	}
	return pauses, nil
}

func setModulePause(pauses *config.Pauses, module string, paused bool) error {
	switch strings.ToLower(strings.TrimSpace(module)) {
	case "lending":
		pauses.Lending = paused
	case "swap":
		pauses.Swap = paused
	case "escrow":
		pauses.Escrow = paused
	case "trade":
		pauses.Trade = paused
	case "loyalty":
		pauses.Loyalty = paused
	case "potso":
		pauses.POTSO = paused
	case "transfer_nhb", "transfer_znhb", "staking":
		return fmt.Errorf("module %q cannot be toggled with this sample: the SetPauses message carries lending, swap, escrow, trade, loyalty and potso only", module)
	default:
		return fmt.Errorf("unknown module %q", module)
	}
	return nil
}

// buildPausesPayload turns the stored configuration into the six-module message.
// The message replaces the whole configuration, so a stored transfer or staking
// pause would be silently cleared by it; the sample stops instead.
func buildPausesPayload(pauses config.Pauses) (*govv1.Pauses, error) {
	for name, set := range map[string]bool{"transfer_nhb": pauses.TransferNHB, "transfer_znhb": pauses.TransferZNHB, "staking": pauses.Staking} {
		if set {
			return nil, fmt.Errorf("the stored configuration pauses %s, which the six-module SetPauses message cannot carry: sending it would clear that pause, so this sample will not send it", name)
		}
	}
	return &govv1.Pauses{
		Lending: pauses.Lending,
		Swap:    pauses.Swap,
		Escrow:  pauses.Escrow,
		Trade:   pauses.Trade,
		Loyalty: pauses.Loyalty,
		Potso:   pauses.POTSO,
	}, nil
}
