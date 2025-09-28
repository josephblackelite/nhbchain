package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

var (
	p2pNow     = time.Now
	p2pRPCCall = callP2PRPC
)

func runP2PCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, p2pUsage())
		return 1
	}

	switch args[0] {
	case "create-trade":
		return runP2PCreateTrade(args[1:], stdout, stderr)
	case "get":
		return runP2PGetTrade(args[1:], stdout, stderr)
	case "settle":
		return runP2PSettle(args[1:], stdout, stderr)
	case "dispute":
		return runP2PDispute(args[1:], stdout, stderr)
	case "resolve":
		return runP2PResolve(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown p2p subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, p2pUsage())
		return 1
	}
}

func runP2PCreateTrade(args []string, stdout, stderr io.Writer) int {
	fs := newP2PFlagSet("p2p create-trade", stderr)
        var (
                offerID     string
                buyer       string
                seller      string
                baseToken   string
                baseAmount  string
                quoteToken  string
                quoteAmount string
                deadline    string
                slippage    uint
        )
	fs.StringVar(&offerID, "offer", "", "offer identifier")
	fs.StringVar(&buyer, "buyer", "", "buyer bech32 address")
	fs.StringVar(&seller, "seller", "", "seller bech32 address")
	fs.StringVar(&baseToken, "base", "", "base token symbol (NHB or ZNHB)")
	fs.StringVar(&baseAmount, "base-amount", "", "base amount (supports 100e18 shorthand)")
	fs.StringVar(&quoteToken, "quote", "", "quote token symbol (NHB or ZNHB)")
        fs.StringVar(&quoteAmount, "quote-amount", "", "quote amount (supports 100e18 shorthand)")
        fs.StringVar(&deadline, "deadline", "", "deadline as +duration or RFC3339 timestamp")
        fs.UintVar(&slippage, "slippage", 0, "maximum slippage in basis points")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if strings.TrimSpace(offerID) == "" {
		return printP2PError(stderr, "--offer is required")
	}
	if buyer == "" {
		return printP2PError(stderr, "--buyer is required")
	}
	if seller == "" {
		return printP2PError(stderr, "--seller is required")
	}
	if baseToken == "" {
		return printP2PError(stderr, "--base is required")
	}
	normalizedBase := strings.ToUpper(strings.TrimSpace(baseToken))
	if normalizedBase != "NHB" && normalizedBase != "ZNHB" {
		return printP2PError(stderr, "--base must be NHB or ZNHB")
	}
	if baseAmount == "" {
		return printP2PError(stderr, "--base-amount is required")
	}
	normalizedBaseAmount, err := normalizeEscrowAmount(baseAmount)
	if err != nil {
		return printP2PError(stderr, err.Error())
	}
	if quoteToken == "" {
		return printP2PError(stderr, "--quote is required")
	}
	normalizedQuote := strings.ToUpper(strings.TrimSpace(quoteToken))
	if normalizedQuote != "NHB" && normalizedQuote != "ZNHB" {
		return printP2PError(stderr, "--quote must be NHB or ZNHB")
	}
	if quoteAmount == "" {
		return printP2PError(stderr, "--quote-amount is required")
	}
	normalizedQuoteAmount, err := normalizeEscrowAmount(quoteAmount)
	if err != nil {
		return printP2PError(stderr, err.Error())
	}
        if deadline == "" {
                return printP2PError(stderr, "--deadline is required")
        }
        deadlineUnix, err := parseEscrowDeadline(deadline, p2pNow())
        if err != nil {
                return printP2PError(stderr, err.Error())
        }
        if slippage > 10_000 {
                return printP2PError(stderr, "--slippage must be between 0 and 10000")
        }
        params := map[string]interface{}{
                "offerId":     offerID,
                "buyer":       buyer,
                "seller":      seller,
                "baseToken":   normalizedBase,
                "baseAmount":  normalizedBaseAmount,
                "quoteToken":  normalizedQuote,
                "quoteAmount": normalizedQuoteAmount,
                "deadline":    deadlineUnix,
                "slippageBps": slippage,
        }
	result, rpcErr, err := p2pRPCCall("p2p_createTrade", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
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

func runP2PSettle(args []string, stdout, stderr io.Writer) int {
	return runP2PActorCommand("p2p_settle", args, stdout, stderr)
}

func runP2PDispute(args []string, stdout, stderr io.Writer) int {
	fs := newP2PFlagSet("p2p dispute", stderr)
	var (
		id      string
		caller  string
		message string
	)
	fs.StringVar(&id, "id", "", "trade identifier")
	fs.StringVar(&caller, "caller", "", "caller bech32 address")
	fs.StringVar(&message, "message", "", "dispute message")
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
	if caller == "" {
		return printP2PError(stderr, "--caller is required")
	}
	if strings.TrimSpace(message) == "" {
		return printP2PError(stderr, "--message is required")
	}
	params := map[string]interface{}{
		"tradeId": id,
		"caller":  caller,
		"message": message,
	}
	result, rpcErr, err := p2pRPCCall("p2p_dispute", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runP2PResolve(args []string, stdout, stderr io.Writer) int {
	fs := newP2PFlagSet("p2p resolve", stderr)
	var (
		id      string
		caller  string
		outcome string
	)
	fs.StringVar(&id, "id", "", "trade identifier")
	fs.StringVar(&caller, "caller", "", "arbitrator bech32 address")
	fs.StringVar(&outcome, "outcome", "", "resolution outcome")
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
	if caller == "" {
		return printP2PError(stderr, "--caller is required")
	}
	normalizedOutcome := strings.ToLower(strings.TrimSpace(outcome))
	if _, ok := validResolveOutcomes()[normalizedOutcome]; !ok {
		return printP2PError(stderr, "--outcome must be one of release_both, refund_both, release_base_refund_quote, release_quote_refund_base")
	}
	params := map[string]interface{}{
		"tradeId": id,
		"caller":  caller,
		"outcome": normalizedOutcome,
	}
	result, rpcErr, err := p2pRPCCall("p2p_resolve", params, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}
	writeRPCResult(stdout, result)
	return 0
}

func runP2PActorCommand(method string, args []string, stdout, stderr io.Writer) int {
	fs := newP2PFlagSet("p2p "+method, stderr)
	var (
		id     string
		caller string
	)
	fs.StringVar(&id, "id", "", "trade identifier")
	fs.StringVar(&caller, "caller", "", "caller bech32 address")
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
	if caller == "" {
		return printP2PError(stderr, "--caller is required")
	}
	params := map[string]interface{}{
		"tradeId": id,
		"caller":  caller,
	}
	result, rpcErr, err := p2pRPCCall(method, params, true)
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
  create-trade  Create a new dual-leg trade
  get           Fetch trade details by id
  settle        Settle a trade atomically
  dispute       Dispute a trade
  resolve       Resolve a disputed trade`)
}

func validResolveOutcomes() map[string]struct{} {
	return map[string]struct{}{
		"release_both":              {},
		"refund_both":               {},
		"release_base_refund_quote": {},
		"release_quote_refund_base": {},
	}
}
