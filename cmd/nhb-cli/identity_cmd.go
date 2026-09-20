package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
)

var identityRPCCall = callIdentityRPC

// identityRetiredMethods maps the id subcommands whose node method is retired to
// that method. The read-only ones (resolve, reverse) are still served.
var identityRetiredMethods = map[string]string{
	"set-alias":        "identity_setAlias",
	"set-avatar":       "identity_setAvatar",
	"add-address":      "identity_addAddress",
	"remove-address":   "identity_removeAddress",
	"set-primary":      "identity_setPrimary",
	"rename":           "identity_rename",
	"create-claimable": "identity_createClaimable",
	"claim":            "identity_claim",
}

func runIdentityCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, identityUsage())
		return 1
	}
	if method, retired := identityRetiredMethods[args[0]]; retired {
		return reportRetired(stderr, "nhb-cli id "+args[0], method)
	}
	switch args[0] {
	case "resolve":
		return runIdentityResolve(args[1:], stdout, stderr)
	case "reverse":
		return runIdentityReverse(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown id subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, identityUsage())
		return 1
	}
}

func runIdentityResolve(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("id resolve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var alias string
	fs.StringVar(&alias, "alias", "", "alias to resolve")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	trimmed := strings.TrimSpace(alias)
	if trimmed == "" {
		fmt.Fprintln(stderr, "Error: --alias is required")
		return 1
	}
	params := []interface{}{trimmed}
	result, rpcErr, err := identityRPCCall("identity_resolve", params, false)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runIdentityReverse(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("id reverse", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var addr string
	fs.StringVar(&addr, "addr", "", "bech32 address to look up")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		fmt.Fprintln(stderr, "Error: --addr is required")
		return 1
	}
	params := []interface{}{trimmed}
	result, rpcErr, err := identityRPCCall("identity_reverse", params, false)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func identityUsage() string {
	return strings.TrimSpace(`Usage:
  nhb-cli id <command> [flags]

Commands:
  resolve            Resolve an alias to metadata and addresses
  reverse            Look up the alias associated with an address

Retired (the node no longer serves the method; each exits non-zero):
  set-alias, set-avatar, add-address, remove-address, set-primary, rename,
  create-claimable, claim
`)
}

func callIdentityRPC(method string, params []interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
	payload := map[string]interface{}{"id": 1, "method": method, "params": params}
	body, _ := json.Marshal(payload)
	resp, err := doRPCRequest(body, requireAuth)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, nil, fmt.Errorf("failed to decode response from node")
	}
	return rpcResp.Result, rpcResp.Error, nil
}
