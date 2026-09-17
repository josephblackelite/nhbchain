package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
)

type posSweepRequest struct {
	Timestamp int64 `json:"timestamp"`
}

func runPOSCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, posUsage())
		return 1
	}
	switch args[0] {
	case "sweep-voids":
		return runPOSSweepVoids(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown pos subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, posUsage())
		return 1
	}
}

func posUsage() string {
	return "Usage: nhb-cli pos <subcommand>\nSubcommands:\n  sweep-voids   Void expired POS authorizations immediately"
}

func runPOSSweepVoids(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pos sweep-voids", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var timestamp string
	fs.StringVar(&timestamp, "timestamp", "", "optional override timestamp (RFC3339, UNIX seconds, or relative like +1h)")
	if err := fs.Parse(args); err != nil {
		return 1
	}

	var payload interface{}
	if trimmed := strings.TrimSpace(timestamp); trimmed != "" {
		ts, err := parseTimestamp(trimmed)
		if err != nil {
			fmt.Fprintf(stderr, "Error parsing --timestamp: %v\n", err)
			return 1
		}
		payload = posSweepRequest{Timestamp: ts}
	}

	result, rpcErr, err := callEscrowRPC("pos_sweepVoids", payload, true)
	if err != nil {
		fmt.Fprintf(stderr, "RPC error: %v\n", err)
		return 1
	}
	if rpcErr != nil {
		fmt.Fprintf(stderr, "RPC error (%d): %s\n", rpcErr.Code, rpcErr.Message)
		if rpcErr.Data != nil {
			fmt.Fprintf(stderr, "Details: %v\n", rpcErr.Data)
		}
		return 1
	}

	if len(result) == 0 {
		fmt.Fprintln(stdout, "Voided 0 authorizations")
		return 0
	}
	var response struct {
		Voided int `json:"voided"`
	}
	if err := json.Unmarshal(result, &response); err != nil {
		fmt.Fprintf(stderr, "Failed to decode response: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Voided %d authorizations\n", response.Voided)
	return 0
}
