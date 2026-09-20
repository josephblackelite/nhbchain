package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
)

var p2pRPCCall = callP2PRPC

// p2pRetiredMethods maps the p2p subcommands whose node method is retired to
// that method. get is read-only and is still served.
var p2pRetiredMethods = map[string]string{
	"create-trade": "p2p_createTrade",
	"settle":       "p2p_settle",
	"dispute":      "p2p_dispute",
	"resolve":      "p2p_resolve",
}

func runP2PCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, p2pUsage())
		return 1
	}

	if method, retired := p2pRetiredMethods[args[0]]; retired {
		return reportRetired(stderr, "nhb-cli p2p "+args[0], method)
	}
	switch args[0] {
	case "get":
		return runP2PGetTrade(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown p2p subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, p2pUsage())
		return 1
	}
}

func runP2PGetTrade(args []string, stdout, stderr io.Writer) int {
	fs := newP2PFlagSet("p2p get", stderr)
	var id string
	fs.StringVar(&id, "id", "", "trade identifier")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if err := validateEscrowID(id); err != nil {
		return printP2PError(stderr, err.Error())
	}
	params := map[string]interface{}{"tradeId": id}
	result, rpcErr, err := p2pRPCCall("p2p_getTrade", params, false)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func newP2PFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, p2pUsage())
	}
	return fs
}

func printP2PError(w io.Writer, msg string) int {
	fmt.Fprintf(w, "Error: %s\n", msg)
	return 1
}

func callP2PRPC(method string, params interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
	return callEscrowRPC(method, params, requireAuth)
}

func p2pUsage() string {
	return strings.TrimSpace(`Usage:
  nhb-cli p2p <command> [flags]

Commands:
  get           Fetch trade details by id

Retired (the node no longer serves the method; each exits non-zero):
  create-trade, settle, dispute, resolve`)
}
