package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
)

var identityRPCCall = callIdentityRPC

func runIdentityCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, identityUsage())
		return 1
	}
	switch args[0] {
	case "set-alias":
		return runIdentitySetAlias(args[1:], stdout, stderr)
	case "set-avatar":
		return runIdentitySetAvatar(args[1:], stdout, stderr)
	case "resolve":
		return runIdentityResolve(args[1:], stdout, stderr)
	case "reverse":
		return runIdentityReverse(args[1:], stdout, stderr)
	case "create-claimable":
		return runIdentityCreateClaimable(args[1:], stdout, stderr)
	case "claim":
		return runIdentityClaim(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown id subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, identityUsage())
		return 1
	}
}

func runIdentitySetAlias(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("id set-alias", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var addr, alias string
	fs.StringVar(&addr, "addr", "", "bech32 address owning the alias")
	fs.StringVar(&alias, "alias", "", "alias to register")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	trimmedAddr := strings.TrimSpace(addr)
	if trimmedAddr == "" {
		fmt.Fprintln(stderr, "Error: --addr is required")
		return 1
	}
	trimmedAlias := strings.TrimSpace(alias)
	if trimmedAlias == "" {
		fmt.Fprintln(stderr, "Error: --alias is required")
		return 1
	}
	params := []interface{}{trimmedAddr, trimmedAlias}
	result, rpcErr, err := identityRPCCall("identity_setAlias", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runIdentitySetAvatar(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("id set-avatar", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var addr, avatar string
	fs.StringVar(&addr, "addr", "", "bech32 address owning the alias")
	fs.StringVar(&avatar, "avatar", "", "avatar URL or blob reference")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	trimmedAddr := strings.TrimSpace(addr)
	if trimmedAddr == "" {
		fmt.Fprintln(stderr, "Error: --addr is required")
		return 1
	}
	trimmedAvatar := strings.TrimSpace(avatar)
	if trimmedAvatar == "" {
		fmt.Fprintln(stderr, "Error: --avatar is required")
		return 1
	}
	params := []interface{}{trimmedAddr, trimmedAvatar}
	result, rpcErr, err := identityRPCCall("identity_setAvatar", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
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

func runIdentityCreateClaimable(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("id create-claimable", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var payer, recipient, token, amount string
	var deadline int64
	fs.StringVar(&payer, "payer", "", "bech32 address funding the claimable")
	fs.StringVar(&recipient, "recipient", "", "alias or 32-byte email hash")
	fs.StringVar(&token, "token", "NHB", "token denomination (NHB or ZNHB)")
	fs.StringVar(&amount, "amount", "", "amount to escrow")
	fs.Int64Var(&deadline, "deadline", 0, "unix timestamp when claimable expires")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	trimmedPayer := strings.TrimSpace(payer)
	trimmedRecipient := strings.TrimSpace(recipient)
	trimmedAmount := strings.TrimSpace(amount)
	trimmedToken := strings.TrimSpace(token)
	if trimmedPayer == "" || trimmedRecipient == "" || trimmedAmount == "" || deadline == 0 {
		fmt.Fprintln(stderr, "Error: --payer, --recipient, --amount, and --deadline are required")
		return 1
	}
	payload := map[string]interface{}{
		"payer":     trimmedPayer,
		"recipient": trimmedRecipient,
		"token":     trimmedToken,
		"amount":    trimmedAmount,
		"deadline":  deadline,
	}
	result, rpcErr, err := identityRPCCall("identity_createClaimable", []interface{}{payload}, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runIdentityClaim(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("id claim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var claimID, payee, preimage string
	fs.StringVar(&claimID, "id", "", "claimable identifier")
	fs.StringVar(&payee, "payee", "", "bech32 address receiving funds")
	fs.StringVar(&preimage, "preimage", "", "claim preimage (hex)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	trimmedID := strings.TrimSpace(claimID)
	trimmedPayee := strings.TrimSpace(payee)
	trimmedPreimage := strings.TrimSpace(preimage)
	if trimmedID == "" || trimmedPayee == "" {
		fmt.Fprintln(stderr, "Error: --id and --payee are required")
		return 1
	}
	payload := map[string]interface{}{
		"claimId":  trimmedID,
		"payee":    trimmedPayee,
		"preimage": trimmedPreimage,
	}
	result, rpcErr, err := identityRPCCall("identity_claim", []interface{}{payload}, true)
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
  set-alias          Register or update the alias for an address
  set-avatar         Update the avatar reference for an alias owner
  resolve            Resolve an alias to metadata and addresses
  reverse            Look up the alias associated with an address
  create-claimable   Create a pay-by-email claimable escrow
  claim              Claim a pending identity claimable
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
