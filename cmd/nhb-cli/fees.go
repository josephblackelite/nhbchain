package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"
)

type feesMonthlyStatusResponse struct {
	Window       string `json:"window_yyyymm"`
	Used         uint64 `json:"used"`
	Remaining    uint64 `json:"remaining"`
	LastRollover string `json:"last_rollover_yyyymm"`
}

func runFeesCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, feesUsage())
		return 1
	}
	switch args[0] {
	case "status":
		return runFeesStatus(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown fees subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, feesUsage())
		return 1
	}
}

func feesUsage() string {
	buf := &bytes.Buffer{}
	fmt.Fprintln(buf, "Usage: nhb-cli fees <subcommand>")
	fmt.Fprintln(buf, "Subcommands:")
	fmt.Fprintln(buf, "  status       Show the aggregated monthly free-tier usage snapshot")
	return buf.String()
}

func runFeesStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("fees status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	payload, err := json.Marshal(map[string]interface{}{
		"id":     1,
		"method": "fees_getMonthlyStatus",
		"params": []interface{}{},
	})
	if err != nil {
		fmt.Fprintf(stderr, "Error building request: %v\n", err)
		return 1
	}
	resp, err := doRPCRequest(payload, false)
	if err != nil {
		fmt.Fprintf(stderr, "RPC error: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	var rpcResp struct {
		Result feesMonthlyStatusResponse `json:"result"`
		Error  *rpcError                 `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		fmt.Fprintf(stderr, "Failed to decode response: %v\n", err)
		return 1
	}
	if rpcResp.Error != nil {
		fmt.Fprintf(stderr, "RPC error: %s\n", rpcResp.Error.Message)
		return 1
	}
	status := rpcResp.Result
	if status.Window == "" {
		fmt.Fprintln(stdout, "No monthly usage has been recorded yet.")
		return 0
	}
	fmt.Fprintf(stdout, "Window:        %s\n", status.Window)
	fmt.Fprintf(stdout, "Used:          %d\n", status.Used)
	fmt.Fprintf(stdout, "Remaining:     %d\n", status.Remaining)
	if status.LastRollover == "" {
		fmt.Fprintf(stdout, "Last rollover: (not yet recorded)\n")
	} else {
		fmt.Fprintf(stdout, "Last rollover: %s\n", status.LastRollover)
	}
	fmt.Fprintf(stdout, "As of:         %s\n", time.Now().UTC().Format(time.RFC3339))
	return 0
}
