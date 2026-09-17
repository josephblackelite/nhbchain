package main

import (
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"strings"
)

var claimableRPCCall = callEscrowRPC

func runClaimableCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, claimableUsage())
		return 1
	}
	switch args[0] {
	case "create":
		return runClaimableCreate(args[1:], stdout, stderr)
	case "claim":
		return runClaimableClaim(args[1:], stdout, stderr)
	case "cancel":
		return runClaimableCancel(args[1:], stdout, stderr)
	case "get":
		return runClaimableGet(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown claimable subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, claimableUsage())
		return 1
	}
}

func runClaimableCreate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("claimable create", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		payer     string
		token     string
		amountStr string
		deadline  string
		hashLock  string
	)
	fs.StringVar(&payer, "payer", "", "payer bech32 address")
	fs.StringVar(&token, "token", "", "token symbol (NHB or ZNHB)")
	fs.StringVar(&amountStr, "amount", "", "claimable amount (supports 100e18 shorthand)")
	fs.StringVar(&deadline, "deadline", "", "deadline as +duration or RFC3339 timestamp")
	fs.StringVar(&hashLock, "hash-lock", "", "hash lock as 0x-prefixed 32-byte hex string")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if payer == "" {
		fmt.Fprintln(stderr, "Error: --payer is required")
		return 1
	}
	normalizedToken := strings.ToUpper(strings.TrimSpace(token))
	if normalizedToken != "NHB" && normalizedToken != "ZNHB" {
		fmt.Fprintln(stderr, "Error: --token must be NHB or ZNHB")
		return 1
	}
	if strings.TrimSpace(amountStr) == "" {
		fmt.Fprintln(stderr, "Error: --amount is required")
		return 1
	}
	normalizedAmount, err := normalizeEscrowAmount(amountStr)
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	if strings.TrimSpace(deadline) == "" {
		fmt.Fprintln(stderr, "Error: --deadline is required")
		return 1
	}
	deadlineUnix, err := parseEscrowDeadline(deadline, escrowNow())
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	if err := validateHashLock(hashLock); err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	payload := map[string]interface{}{
		"payer":    payer,
		"token":    normalizedToken,
		"amount":   normalizedAmount,
		"deadline": deadlineUnix,
		"hashLock": strings.TrimPrefix(strings.TrimPrefix(hashLock, "0x"), "0X"),
	}
	result, rpcErr, err := claimableRPCCall("claimable_create", payload, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runClaimableClaim(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("claimable claim", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		id       string
		preimage string
		payee    string
	)
	fs.StringVar(&id, "id", "", "claimable identifier")
	fs.StringVar(&preimage, "preimage", "", "preimage as 0x-prefixed hex string")
	fs.StringVar(&payee, "payee", "", "payee bech32 address")
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
	if payee == "" {
		fmt.Fprintln(stderr, "Error: --payee is required")
		return 1
	}
	if _, err := parseHex(preimage); err != nil {
		fmt.Fprintln(stderr, "Error: --preimage must be hex-encoded")
		return 1
	}
	payload := map[string]interface{}{
		"id":       id,
		"preimage": strings.TrimPrefix(strings.TrimPrefix(preimage, "0x"), "0X"),
		"payee":    payee,
	}
	result, rpcErr, err := claimableRPCCall("claimable_claim", payload, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runClaimableCancel(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("claimable cancel", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var (
		id     string
		caller string
	)
	fs.StringVar(&id, "id", "", "claimable identifier")
	fs.StringVar(&caller, "caller", "", "caller bech32 address")
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
	if caller == "" {
		fmt.Fprintln(stderr, "Error: --caller is required")
		return 1
	}
	payload := map[string]interface{}{
		"id":     id,
		"caller": caller,
	}
	result, rpcErr, err := claimableRPCCall("claimable_cancel", payload, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
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
	return "Usage: nhb-cli claimable <create|claim|cancel|get> [--flags]"
}

func validateHashLock(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("--hash-lock is required")
	}
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if len(cleaned) != 64 {
		return fmt.Errorf("--hash-lock must be a 0x-prefixed 32-byte hex string")
	}
	if _, err := hex.DecodeString(cleaned); err != nil {
		return fmt.Errorf("--hash-lock must contain only hexadecimal characters")
	}
	return nil
}

func parseHex(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	cleaned := strings.TrimPrefix(strings.TrimPrefix(trimmed, "0x"), "0X")
	if cleaned == "" {
		return []byte{}, nil
	}
	return hex.DecodeString(cleaned)
}
