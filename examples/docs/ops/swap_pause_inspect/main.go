package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"nhbchain/config"
	"nhbchain/examples/docs/ops/internal/stateutil"
)

type stableError struct {
	Error string `json:"error"`
}

type stableStatus struct {
	Quotes       int `json:"quotes"`
	Reservations int `json:"reservations"`
	Assets       int `json:"assets"`
}

func main() {
	dbPath := flag.String("db", "./nhb-data", "path to the consensus data directory")
	consensusEndpoint := flag.String("consensus", "localhost:9090", "consensus gRPC endpoint")
	swapdBase := flag.String("swapd", "http://localhost:7074", "base URL for swapd (scheme://host:port)")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	snapshot, err := stateutil.Load(ctx, *dbPath, *consensusEndpoint)
	if err != nil {
		log.Fatalf("load state snapshot: %v", err)
	}
	defer snapshot.Close()

	swapPaused := false
	raw, ok, err := snapshot.Manager.ParamStoreGet("system/pauses")
	if err != nil {
		log.Fatalf("read pause configuration: %v", err)
	}
	if ok && len(bytes.TrimSpace(raw)) > 0 {
		var pauses config.Pauses
		if err := json.Unmarshal(raw, &pauses); err != nil {
			log.Fatalf("decode pause payload: %v", err)
		}
		swapPaused = pauses.Swap
	}

	fmt.Printf("state height %d (%s)\n", snapshot.Height, snapshot.Timestamp.Format(time.RFC3339))
	fmt.Printf("global.pauses.swap: %t\n", swapPaused)

	client := &http.Client{Timeout: 5 * time.Second}
	stableURL := strings.TrimRight(*swapdBase, "/") + "/v1/stable/status"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, stableURL, nil)
	if err != nil {
		log.Fatalf("build stable status request: %v", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		log.Fatalf("query swapd stable status: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatalf("read swapd response: %v", err)
	}

	switch resp.StatusCode {
	case http.StatusOK:
		var status stableStatus
		if err := json.Unmarshal(body, &status); err != nil {
			log.Fatalf("decode stable status payload: %v", err)
		}
		fmt.Printf("swapd.stable.paused: false (quotes=%d reservations=%d assets=%d)\n", status.Quotes, status.Reservations, status.Assets)
	case http.StatusNotImplemented:
		var payload stableError
		if err := json.Unmarshal(body, &payload); err != nil {
			log.Fatalf("decode stable error payload: %v", err)
		}
		if payload.Error == "" {
			payload.Error = string(body)
		}
		fmt.Printf("swapd.stable.paused: true (%s)\n", payload.Error)
	default:
		log.Fatalf("unexpected status from swapd: %d %s", resp.StatusCode, string(body))
	}
}
