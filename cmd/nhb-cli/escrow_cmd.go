package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

var (
	escrowNow     = time.Now
	escrowRPCCall = callEscrowRPC
)

func runEscrowCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, escrowUsage())
		return 1
	}

	switch args[0] {
	case "create":
		return runEscrowCreate(args[1:], stdout, stderr)
	case "get":
		return runEscrowGet(args[1:], stdout, stderr)
	case "fund":
		return runEscrowFund(args[1:], stdout, stderr)
	case "release":
		return runEscrowRelease(args[1:], stdout, stderr)
	case "refund":
		return runEscrowRefund(args[1:], stdout, stderr)
	case "expire":
		return runEscrowExpire(args[1:], stdout, stderr)
	case "dispute":
		return runEscrowDispute(args[1:], stdout, stderr)
	case "resolve":
		return runEscrowResolve(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown escrow subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, escrowUsage())
		return 1
	}
}

func runEscrowCreate(args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet("escrow create", stderr)
	var (
		payer     string
		payee     string
		token     string
		amountStr string
		feeBpsStr string
		deadline  string
		mediator  string
		meta      string
		realm     string
		nonceStr  string
	)
	fs.StringVar(&payer, "payer", "", "payer bech32 address")
	fs.StringVar(&payee, "payee", "", "payee bech32 address")
	fs.StringVar(&token, "token", "", "token symbol (NHB or ZNHB)")
	fs.StringVar(&amountStr, "amount", "", "escrow amount (supports 100e18 shorthand)")
	fs.StringVar(&feeBpsStr, "fee-bps", "", "fee in basis points")
	fs.StringVar(&deadline, "deadline", "", "deadline as +duration or RFC3339 timestamp")
	fs.StringVar(&mediator, "mediator", "", "optional mediator bech32 address")
	fs.StringVar(&meta, "meta", "", "optional 0x-prefixed metadata hash")
	fs.StringVar(&realm, "realm", "", "optional realm identifier")
	fs.StringVar(&nonceStr, "nonce", "", "unique nonce for this escrow")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if payer == "" {
		return printEscrowError(stderr, "--payer is required")
	}
	if payee == "" {
		return printEscrowError(stderr, "--payee is required")
	}
	if token == "" {
		return printEscrowError(stderr, "--token is required")
	}
	normalizedToken := strings.ToUpper(strings.TrimSpace(token))
	if normalizedToken != "NHB" && normalizedToken != "ZNHB" {
		return printEscrowError(stderr, "--token must be NHB or ZNHB")
	}
	if amountStr == "" {
		return printEscrowError(stderr, "--amount is required")
	}
	normalizedAmount, err := normalizeEscrowAmount(amountStr)
	if err != nil {
		return printEscrowError(stderr, err.Error())
	}
	if feeBpsStr == "" {
		return printEscrowError(stderr, "--fee-bps is required")
	}
	feeBpsValue, err := strconv.ParseUint(feeBpsStr, 10, 32)
	if err != nil {
		return printEscrowError(stderr, "--fee-bps must be a positive integer")
	}
	if feeBpsValue > 10_000 {
		return printEscrowError(stderr, "--fee-bps must be <= 10000")
	}
	if deadline == "" {
		return printEscrowError(stderr, "--deadline is required")
	}
	deadlineUnix, err := parseEscrowDeadline(deadline, escrowNow())
	if err != nil {
		return printEscrowError(stderr, err.Error())
	}
	if nonceStr == "" {
		return printEscrowError(stderr, "--nonce is required")
	}
	nonceValue, err := strconv.ParseUint(nonceStr, 10, 64)
	if err != nil || nonceValue == 0 {
		return printEscrowError(stderr, "--nonce must be a positive integer")
	}

	params := map[string]interface{}{
		"payer":    payer,
		"payee":    payee,
		"token":    normalizedToken,
		"amount":   normalizedAmount,
		"feeBps":   feeBpsValue,
		"deadline": deadlineUnix,
		"nonce":    nonceValue,
	}
	if strings.TrimSpace(mediator) != "" {
		params["mediator"] = mediator
	}
	if strings.TrimSpace(meta) != "" {
		params["meta"] = meta
	}
	if strings.TrimSpace(realm) != "" {
		params["realm"] = realm
	}

	result, rpcErr, err := escrowRPCCall("escrow_create", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runEscrowGet(args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet("escrow get", stderr)
	var id string
	fs.StringVar(&id, "id", "", "escrow identifier")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if err := validateEscrowID(id); err != nil {
		return printEscrowError(stderr, err.Error())
	}
	params := map[string]interface{}{"id": id}
	result, rpcErr, err := escrowRPCCall("escrow_get", params, false)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runEscrowFund(args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet("escrow fund", stderr)
	var (
		id   string
		from string
	)
	fs.StringVar(&id, "id", "", "escrow identifier")
	fs.StringVar(&from, "from", "", "payer address funding the escrow")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if err := validateEscrowID(id); err != nil {
		return printEscrowError(stderr, err.Error())
	}
	if from == "" {
		return printEscrowError(stderr, "--from is required")
	}
	params := map[string]interface{}{"id": id, "from": from}
	result, rpcErr, err := escrowRPCCall("escrow_fund", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runEscrowRelease(args []string, stdout, stderr io.Writer) int {
	return runEscrowTransition("escrow_release", "--caller", args, stdout, stderr)
}

func runEscrowRefund(args []string, stdout, stderr io.Writer) int {
	return runEscrowTransition("escrow_refund", "--caller", args, stdout, stderr)
}

func runEscrowDispute(args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet("escrow dispute", stderr)
	var (
		id      string
		caller  string
		message string
	)
	fs.StringVar(&id, "id", "", "escrow identifier")
	fs.StringVar(&caller, "caller", "", "payer or payee address")
	fs.StringVar(&message, "message", "", "dispute message")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if err := validateEscrowID(id); err != nil {
		return printEscrowError(stderr, err.Error())
	}
	if caller == "" {
		return printEscrowError(stderr, "--caller is required")
	}
	params := map[string]interface{}{"id": id, "caller": caller}
	if trimmed := strings.TrimSpace(message); trimmed != "" {
		params["reason"] = trimmed
	}
	result, rpcErr, err := escrowRPCCall("escrow_dispute", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runEscrowResolve(args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet("escrow resolve", stderr)
	var (
		id      string
		caller  string
		outcome string
	)
	fs.StringVar(&id, "id", "", "escrow identifier")
	fs.StringVar(&caller, "caller", "", "mediator address")
	fs.StringVar(&outcome, "outcome", "", "resolution outcome (release or refund)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if err := validateEscrowID(id); err != nil {
		return printEscrowError(stderr, err.Error())
	}
	if caller == "" {
		return printEscrowError(stderr, "--caller is required")
	}
	normalizedOutcome := strings.ToLower(strings.TrimSpace(outcome))
	if normalizedOutcome != "release" && normalizedOutcome != "refund" {
		return printEscrowError(stderr, "--outcome must be release or refund")
	}
	params := map[string]interface{}{"id": id, "caller": caller, "outcome": normalizedOutcome}
	result, rpcErr, err := escrowRPCCall("escrow_resolve", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runEscrowExpire(args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet("escrow expire", stderr)
	var id string
	fs.StringVar(&id, "id", "", "escrow identifier")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if err := validateEscrowID(id); err != nil {
		return printEscrowError(stderr, err.Error())
	}
	params := map[string]interface{}{"id": id}
	result, rpcErr, err := escrowRPCCall("escrow_expire", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runEscrowTransition(method string, callerFlag string, args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet(method, stderr)
	var (
		id     string
		caller string
	)
	fs.StringVar(&id, "id", "", "escrow identifier")
	fs.StringVar(&caller, "caller", "", "actor address")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if err := validateEscrowID(id); err != nil {
		return printEscrowError(stderr, err.Error())
	}
	if caller == "" {
		return printEscrowError(stderr, fmt.Sprintf("%s is required", callerFlag))
	}
	params := map[string]interface{}{"id": id, "caller": caller}
	result, rpcErr, err := escrowRPCCall(method, params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func newEscrowFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintln(stderr, escrowUsage())
	}
	return fs
}

func printEscrowError(w io.Writer, msg string) int {
	fmt.Fprintf(w, "Error: %s\n", msg)
	return 1
}

func handleRPCError(w io.Writer, err *rpcError) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(w, "RPC error %d: %s\n", err.Code, err.Message)
	return 1
}

func handleRPCCallError(w io.Writer, err error) int {
	if err == nil {
		return 0
	}
	fmt.Fprintf(w, "RPC call failed: %v\n", err)
	return 1
}

func writeRPCResult(w io.Writer, result json.RawMessage) {
	if len(result) == 0 {
		fmt.Fprintln(w, "null")
		return
	}
	if _, err := w.Write(result); err == nil {
		if len(result) == 0 || result[len(result)-1] != '\n' {
			fmt.Fprintln(w)
		}
	}
}

func escrowUsage() string {
	return strings.TrimSpace(`Usage:
  nhb-cli escrow <command> [flags]

Commands:
  create  Create a new escrow definition
  get     Fetch escrow details by id
  fund    Fund an escrow from the payer account
  release Release funds to the payee
  refund  Refund funds to the payer
  expire  Expire an escrow after the deadline
  dispute Flag an escrow for mediation
  resolve Resolve a disputed escrow
`)
}

func normalizeEscrowAmount(value string) (string, error) {
	trimmed := strings.ReplaceAll(strings.TrimSpace(value), "_", "")
	if trimmed == "" {
		return "", fmt.Errorf("--amount is required")
	}
	var exponent int
	base := trimmed
	if idx := strings.IndexAny(trimmed, "eE"); idx != -1 {
		base = trimmed[:idx]
		expPart := strings.TrimSpace(trimmed[idx+1:])
		if expPart == "" {
			return "", fmt.Errorf("invalid scientific notation in --amount")
		}
		expValue, err := strconv.ParseInt(expPart, 10, 32)
		if err != nil {
			return "", fmt.Errorf("invalid scientific notation in --amount")
		}
		exponent = int(expValue)
	}
	base = strings.TrimSpace(strings.TrimPrefix(base, "+"))
	if strings.HasPrefix(base, "-") {
		return "", fmt.Errorf("--amount must be positive")
	}
	parts := strings.Split(base, ".")
	if len(parts) > 2 {
		return "", fmt.Errorf("invalid amount format")
	}
	integerPart := parts[0]
	fractionalPart := ""
	if len(parts) == 2 {
		fractionalPart = parts[1]
	}
	digits := integerPart + fractionalPart
	if digits == "" {
		return "", fmt.Errorf("invalid amount format")
	}
	if !isDigits(digits) {
		return "", fmt.Errorf("invalid amount format")
	}
	digits = strings.TrimLeft(digits, "0")
	fracLen := len(fractionalPart)
	if fracLen > 0 {
		for fracLen > 0 && len(digits) > 0 && digits[len(digits)-1] == '0' {
			digits = digits[:len(digits)-1]
			fracLen--
		}
	}
	totalExponent := exponent - fracLen
	if totalExponent < 0 {
		return "", fmt.Errorf("--amount must be an integer")
	}
	if digits == "" {
		return "", fmt.Errorf("--amount must be positive")
	}
	if totalExponent > 0 {
		digits += strings.Repeat("0", totalExponent)
	}
	return digits, nil
}

func isDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isHex(value string) bool {
	for _, r := range value {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func parseEscrowDeadline(value string, now time.Time) (int64, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0, fmt.Errorf("--deadline is required")
	}
	if strings.HasPrefix(trimmed, "+") {
		durationStr := strings.TrimSpace(trimmed[1:])
		if durationStr == "" {
			return 0, fmt.Errorf("invalid deadline duration")
		}
		dur, err := parseDeadlineDuration(durationStr)
		if err != nil {
			return 0, err
		}
		if dur <= 0 {
			return 0, fmt.Errorf("deadline duration must be positive")
		}
		return now.Add(dur).Unix(), nil
	}
	ts, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return 0, fmt.Errorf("invalid RFC3339 deadline")
	}
	return ts.Unix(), nil
}

func parseDeadlineDuration(value string) (time.Duration, error) {
	if strings.HasSuffix(value, "d") || strings.HasSuffix(value, "D") {
		daysStr := strings.TrimSuffix(strings.TrimSuffix(value, "d"), "D")
		if daysStr == "" {
			return 0, fmt.Errorf("invalid deadline duration")
		}
		days, err := strconv.ParseFloat(daysStr, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid deadline duration")
		}
		return time.Duration(days * 24 * float64(time.Hour)), nil
	}
	dur, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid deadline duration")
	}
	return dur, nil
}

func callEscrowRPC(method string, params interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
	payload := map[string]interface{}{
		"id":     1,
		"method": method,
	}
	if params != nil {
		payload["params"] = []interface{}{params}
	} else {
		payload["params"] = []interface{}{}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, nil, err
	}
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
		return nil, nil, fmt.Errorf("failed to decode RPC response: %w", err)
	}
	return rpcResp.Result, rpcResp.Error, nil
}

func validateEscrowID(value string) error {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return fmt.Errorf("--id is required")
	}
	cleaned := trimmed
	if strings.HasPrefix(trimmed, "0x") || strings.HasPrefix(trimmed, "0X") {
		cleaned = trimmed[2:]
	} else {
		return fmt.Errorf("--id must be a 0x-prefixed 32-byte hex string")
	}
	if len(cleaned) != 64 {
		return fmt.Errorf("--id must be a 0x-prefixed 32-byte hex string")
	}
	if !isHex(cleaned) {
		return fmt.Errorf("--id must contain only hexadecimal characters")
	}
	return nil
}
