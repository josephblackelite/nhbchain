package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

var govRPCCall = callEscrowRPC

func runGovCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, govUsage())
		return 1
	}
	switch args[0] {
	case "propose":
		return runGovPropose(args[1:], stdout, stderr)
	case "vote":
		return runGovVote(args[1:], stdout, stderr)
	case "finalize":
		return runGovFinalize(args[1:], stdout, stderr)
	case "queue":
		return runGovQueue(args[1:], stdout, stderr)
	case "execute":
		return runGovExecute(args[1:], stdout, stderr)
	case "show":
		return runGovShow(args[1:], stdout, stderr)
	case "list":
		return runGovList(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown gov subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, govUsage())
		return 1
	}
}

func runGovPropose(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gov propose", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		kind    string
		payload string
		from    string
		deposit string
	)
	fs.StringVar(&kind, "kind", "", "proposal kind (e.g. param.update)")
	fs.StringVar(&payload, "payload", "", "proposal payload JSON or @path to file")
	fs.StringVar(&from, "from", "", "proposer bech32 address")
	fs.StringVar(&deposit, "deposit", "0", "deposit amount in wei (supports 1000e18 shorthand)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if strings.TrimSpace(kind) == "" {
		fmt.Fprintln(stderr, "Error: --kind is required")
		return 1
	}
	if strings.TrimSpace(payload) == "" {
		fmt.Fprintln(stderr, "Error: --payload is required")
		return 1
	}
	if strings.TrimSpace(from) == "" {
		fmt.Fprintln(stderr, "Error: --from is required")
		return 1
	}
	payloadBody, err := readGovPayload(payload)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	normalizedDeposit, err := normalizeGovAmount(deposit)
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	params := map[string]interface{}{
		"kind":    kind,
		"payload": payloadBody,
		"from":    from,
		"deposit": normalizedDeposit,
	}
	result, rpcErr, err := govRPCCall("gov_propose", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runGovVote(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gov vote", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		id     uint64
		from   string
		choice string
	)
	fs.Uint64Var(&id, "id", 0, "proposal identifier")
	fs.StringVar(&from, "from", "", "voter bech32 address")
	fs.StringVar(&choice, "choice", "", "vote choice (yes|no|abstain)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if id == 0 {
		fmt.Fprintln(stderr, "Error: --id is required")
		return 1
	}
	if strings.TrimSpace(from) == "" {
		fmt.Fprintln(stderr, "Error: --from is required")
		return 1
	}
	normalizedChoice := strings.ToLower(strings.TrimSpace(choice))
	switch normalizedChoice {
	case "yes", "no", "abstain":
	default:
		fmt.Fprintln(stderr, "Error: --choice must be yes, no, or abstain")
		return 1
	}
	params := map[string]interface{}{
		"id":     id,
		"from":   from,
		"choice": normalizedChoice,
	}
	result, rpcErr, err := govRPCCall("gov_vote", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runGovFinalize(args []string, stdout, stderr io.Writer) int {
	return runGovSimpleIDCommand("gov_finalize", args, stdout, stderr, true)
}

func runGovQueue(args []string, stdout, stderr io.Writer) int {
	return runGovSimpleIDCommand("gov_queue", args, stdout, stderr, true)
}

func runGovExecute(args []string, stdout, stderr io.Writer) int {
	return runGovSimpleIDCommand("gov_execute", args, stdout, stderr, true)
}

func runGovShow(args []string, stdout, stderr io.Writer) int {
	return runGovSimpleIDCommand("gov_proposal", args, stdout, stderr, false)
}

func runGovList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gov list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		cursor uint64
		limit  int
	)
	fs.Uint64Var(&cursor, "cursor", 0, "optional pagination cursor")
	fs.IntVar(&limit, "limit", 0, "max proposals to return (default 20)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	params := map[string]interface{}{}
	if cursor > 0 {
		params["cursor"] = cursor
	}
	if limit > 0 {
		params["limit"] = limit
	}
	var paramPayload interface{}
	if len(params) > 0 {
		paramPayload = params
	}
	result, rpcErr, err := govRPCCall("gov_list", paramPayload, false)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runGovSimpleIDCommand(method string, args []string, stdout, stderr io.Writer, requireAuth bool) int {
	fs := flag.NewFlagSet(method, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var id uint64
	fs.Uint64Var(&id, "id", 0, "proposal identifier")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if id == 0 {
		fmt.Fprintln(stderr, "Error: --id is required")
		return 1
	}
	params := map[string]interface{}{"id": id}
	result, rpcErr, err := govRPCCall(method, params, requireAuth)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func readGovPayload(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if strings.HasPrefix(trimmed, "@") {
		path := strings.TrimPrefix(trimmed, "@")
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("failed to read payload file: %w", err)
		}
		return string(data), nil
	}
	return trimmed, nil
}

func govUsage() string {
	return `Usage: nhb gov <command>

Commands:
  propose   Submit a new governance proposal
  vote      Cast a vote on a proposal
  finalize  Finalize voting and tally a proposal
  queue     Queue a passed proposal for execution
  execute   Execute a queued proposal
  show      Show proposal details
  list      List proposals`
}

func normalizeGovAmount(value string) (string, error) {
	trimmed := strings.ReplaceAll(strings.TrimSpace(value), "_", "")
	if trimmed == "" {
		return "0", nil
	}
	normalized := strings.TrimPrefix(trimmed, "+")
	if strings.HasPrefix(normalized, "-") {
		return "", fmt.Errorf("--deposit must not be negative")
	}
	base := normalized
	var exponent int64
	if idx := strings.IndexAny(base, "eE"); idx != -1 {
		expPart := strings.TrimSpace(base[idx+1:])
		if expPart == "" {
			return "", fmt.Errorf("invalid scientific notation in --deposit")
		}
		expValue, err := strconv.ParseInt(expPart, 10, 32)
		if err != nil {
			return "", fmt.Errorf("invalid scientific notation in --deposit")
		}
		exponent = expValue
		base = strings.TrimSpace(base[:idx])
	}
	parts := strings.Split(base, ".")
	if len(parts) > 2 {
		return "", fmt.Errorf("invalid deposit amount")
	}
	integerPart := parts[0]
	fractionalPart := ""
	if len(parts) == 2 {
		fractionalPart = parts[1]
	}
	digits := integerPart + fractionalPart
	if digits == "" {
		return "0", nil
	}
	if !isDigits(digits) {
		return "", fmt.Errorf("invalid deposit amount")
	}
	fracLen := len(fractionalPart)
	for fracLen > 0 && len(digits) > 0 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		fracLen--
	}
	digits = strings.TrimLeft(digits, "0")
	totalExponent := exponent - int64(fracLen)
	if totalExponent < 0 {
		return "", fmt.Errorf("--deposit must be an integer amount")
	}
	if digits == "" {
		digits = "0"
	}
	if totalExponent > 0 {
		digits += strings.Repeat("0", int(totalExponent))
	}
	return digits, nil
}
