package main

import (
	"flag"
	"fmt"
	"io"
)

var claimableRPCCall = callEscrowRPC

// claimableRetiredMethods maps the claimable subcommands whose node method is
// retired to that method. get is read-only and is still served.
var claimableRetiredMethods = map[string]string{
	"create": "claimable_create",
	"claim":  "claimable_claim",
	"cancel": "claimable_cancel",
}

func runClaimableCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, claimableUsage())
		return 1
	}
	if method, retired := claimableRetiredMethods[args[0]]; retired {
		return reportRetired(stderr, "nhb-cli claimable "+args[0], method)
	}
	switch args[0] {
	case "get":
		return runClaimableGet(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown claimable subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, claimableUsage())
		return 1
	}
}

func runClaimableGet(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("claimable get", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var id string
	fs.StringVar(&id, "id", "", "claimable identifier")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if err := validateEscrowID(id); err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	payload := map[string]interface{}{"id": id}
	result, rpcErr, err := claimableRPCCall("claimable_get", payload, false)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func claimableUsage() string {
	return "Usage: nhb-cli claimable get --id <0x...>\n(create, claim and cancel are retired: the node no longer serves them, and each exits non-zero)"
}
