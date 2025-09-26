package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

type realmResult struct {
	ID              string            `json:"id"`
	Version         uint64            `json:"version"`
	NextPolicyNonce uint64            `json:"nextPolicyNonce"`
	CreatedAt       int64             `json:"createdAt"`
	UpdatedAt       int64             `json:"updatedAt"`
	Arbitrators     *arbitratorResult `json:"arbitrators,omitempty"`
}

type arbitratorResult struct {
	Scheme    string   `json:"scheme"`
	Threshold uint32   `json:"threshold"`
	Members   []string `json:"members,omitempty"`
}

type snapshotResult struct {
	ID             string        `json:"id"`
	Payer          string        `json:"payer"`
	Payee          string        `json:"payee"`
	Mediator       *string       `json:"mediator,omitempty"`
	Token          string        `json:"token"`
	Amount         string        `json:"amount"`
	FeeBps         uint32        `json:"feeBps"`
	Deadline       int64         `json:"deadline"`
	CreatedAt      int64         `json:"createdAt"`
	Status         string        `json:"status"`
	Meta           string        `json:"meta"`
	Realm          *string       `json:"realm,omitempty"`
	FrozenPolicy   *frozenPolicy `json:"frozenPolicy,omitempty"`
	ResolutionHash *string       `json:"resolutionHash,omitempty"`
}

type frozenPolicy struct {
	RealmID      string   `json:"realmId"`
	RealmVersion uint64   `json:"realmVersion"`
	PolicyNonce  uint64   `json:"policyNonce"`
	Scheme       string   `json:"scheme"`
	Threshold    uint32   `json:"threshold"`
	Members      []string `json:"members,omitempty"`
	FrozenAt     int64    `json:"frozenAt,omitempty"`
}

type eventResult struct {
	Sequence   int64             `json:"sequence"`
	Type       string            `json:"type"`
	Attributes map[string]string `json:"attributes"`
}

type escrowCreateResult struct {
	ID string `json:"id"`
}

func main() {
	defaultRPC := strings.TrimSpace(os.Getenv("NHB_RPC_URL"))
	if defaultRPC == "" {
		defaultRPC = "http://127.0.0.1:8545"
	}
	defaultAuth := strings.TrimSpace(os.Getenv("NHB_RPC_TOKEN"))

	root := flag.NewFlagSet("nhb-escrow", flag.ExitOnError)
	rpcURL := root.String("rpc", defaultRPC, "JSON-RPC endpoint")
	authToken := root.String("auth", defaultAuth, "Bearer token for authenticated RPC calls")
	root.Parse(os.Args[1:])

	args := root.Args()
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage())
		os.Exit(1)
	}

	code := 0
	switch args[0] {
	case "realm":
		code = runRealmCommand(*rpcURL, *authToken, args[1:])
	case "snapshot":
		code = runSnapshotCommand(*rpcURL, *authToken, args[1:])
	case "events":
		code = runEventsCommand(*rpcURL, *authToken, args[1:])
	case "open":
		code = runOpenCommand(*rpcURL, *authToken, args[1:])
	case "resolve":
		code = runResolveCommand(*rpcURL, *authToken, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		fmt.Fprintln(os.Stderr, usage())
		code = 1
	}
	if code != 0 {
		os.Exit(code)
	}
}

func runRealmCommand(rpcURL, auth string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: realm get --id <realm-id>")
		return 1
	}
	switch args[0] {
	case "get":
		fs := flag.NewFlagSet("realm get", flag.ExitOnError)
		id := fs.String("id", "", "realm identifier")
		fs.Parse(args[1:])
		if strings.TrimSpace(*id) == "" {
			fmt.Fprintln(os.Stderr, "--id is required")
			return 1
		}
		params := []interface{}{map[string]string{"id": strings.TrimSpace(*id)}}
		result, rpcErr, err := callRPC(rpcURL, auth, "escrow_getRealm", params)
		if err != nil {
			fmt.Fprintf(os.Stderr, "RPC call failed: %v\n", err)
			return 1
		}
		if rpcErr != nil {
			printRPCError(rpcErr)
			return 1
		}
		var realm realmResult
		if err := json.Unmarshal(result, &realm); err != nil {
			fmt.Fprintf(os.Stderr, "decode realm response: %v\n", err)
			return 1
		}
		if err := printJSON(realm); err != nil {
			fmt.Fprintf(os.Stderr, "print response: %v\n", err)
			return 1
		}
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown realm subcommand: %s\n", args[0])
		return 1
	}
}

func runSnapshotCommand(rpcURL, auth string, args []string) int {
	fs := flag.NewFlagSet("snapshot", flag.ExitOnError)
	id := fs.String("id", "", "escrow identifier")
	fs.Parse(args)
	if strings.TrimSpace(*id) == "" {
		fmt.Fprintln(os.Stderr, "--id is required")
		return 1
	}
	params := []interface{}{map[string]string{"id": strings.TrimSpace(*id)}}
	result, rpcErr, err := callRPC(rpcURL, auth, "escrow_getSnapshot", params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RPC call failed: %v\n", err)
		return 1
	}
	if rpcErr != nil {
		printRPCError(rpcErr)
		return 1
	}
	var snapshot snapshotResult
	if err := json.Unmarshal(result, &snapshot); err != nil {
		fmt.Fprintf(os.Stderr, "decode snapshot response: %v\n", err)
		return 1
	}
	if err := printJSON(snapshot); err != nil {
		fmt.Fprintf(os.Stderr, "print response: %v\n", err)
		return 1
	}
	return 0
}

func runEventsCommand(rpcURL, auth string, args []string) int {
	fs := flag.NewFlagSet("events", flag.ExitOnError)
	prefix := fs.String("prefix", "", "optional event type prefix (default escrow.)")
	limitFlag := fs.Int("limit", 0, "maximum number of events to return")
	fs.Parse(args)
	var params []interface{}
	payload := make(map[string]interface{})
	if strings.TrimSpace(*prefix) != "" {
		payload["prefix"] = strings.TrimSpace(*prefix)
	}
	if *limitFlag > 0 {
		payload["limit"] = *limitFlag
	}
	if len(payload) > 0 {
		params = []interface{}{payload}
	}
	result, rpcErr, err := callRPC(rpcURL, auth, "escrow_listEvents", params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RPC call failed: %v\n", err)
		return 1
	}
	if rpcErr != nil {
		printRPCError(rpcErr)
		return 1
	}
	var events []eventResult
	if err := json.Unmarshal(result, &events); err != nil {
		fmt.Fprintf(os.Stderr, "decode events response: %v\n", err)
		return 1
	}
	if err := printJSON(events); err != nil {
		fmt.Fprintf(os.Stderr, "print response: %v\n", err)
		return 1
	}
	return 0
}

func runOpenCommand(rpcURL, auth string, args []string) int {
	fs := flag.NewFlagSet("open", flag.ExitOnError)
	payer := fs.String("payer", "", "payer Bech32 address")
	payee := fs.String("payee", "", "payee Bech32 address")
	token := fs.String("token", "NHB", "token symbol (NHB or ZNHB)")
	amount := fs.String("amount", "", "escrow amount in wei")
	feeBps := fs.Uint("fee-bps", 0, "escrow fee in basis points")
	deadline := fs.Int64("deadline", 0, "escrow deadline as unix timestamp")
	mediator := fs.String("mediator", "", "optional mediator address")
	meta := fs.String("meta", "", "optional 0x-prefixed metadata hash")
	realm := fs.String("realm", "", "optional realm identifier")
	fs.Parse(args)

	if strings.TrimSpace(*payer) == "" {
		fmt.Fprintln(os.Stderr, "--payer is required")
		return 1
	}
	if strings.TrimSpace(*payee) == "" {
		fmt.Fprintln(os.Stderr, "--payee is required")
		return 1
	}
	if strings.TrimSpace(*amount) == "" {
		fmt.Fprintln(os.Stderr, "--amount is required")
		return 1
	}
	if *deadline <= 0 {
		fmt.Fprintln(os.Stderr, "--deadline must be a unix timestamp in the future")
		return 1
	}
	params := map[string]interface{}{
		"payer":    strings.TrimSpace(*payer),
		"payee":    strings.TrimSpace(*payee),
		"token":    strings.ToUpper(strings.TrimSpace(*token)),
		"amount":   strings.TrimSpace(*amount),
		"feeBps":   *feeBps,
		"deadline": *deadline,
	}
	if trimmed := strings.TrimSpace(*mediator); trimmed != "" {
		params["mediator"] = trimmed
	}
	if trimmed := strings.TrimSpace(*meta); trimmed != "" {
		params["meta"] = trimmed
	}
	if trimmed := strings.TrimSpace(*realm); trimmed != "" {
		params["realm"] = trimmed
	}
	payload := []interface{}{params}
	result, rpcErr, err := callRPC(rpcURL, auth, "escrow_create", payload)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RPC call failed: %v\n", err)
		return 1
	}
	if rpcErr != nil {
		printRPCError(rpcErr)
		return 1
	}
	var created escrowCreateResult
	if err := json.Unmarshal(result, &created); err != nil {
		fmt.Fprintf(os.Stderr, "decode escrow response: %v\n", err)
		return 1
	}
	fmt.Printf("Escrow opened with id %s\n", created.ID)
	return 0
}

func runResolveCommand(rpcURL, auth string, args []string) int {
	fs := flag.NewFlagSet("resolve", flag.ExitOnError)
	id := fs.String("id", "", "escrow identifier")
	caller := fs.String("caller", "", "arbitrator Bech32 address")
	outcome := fs.String("outcome", "", "resolution outcome (release or refund)")
	fs.Parse(args)
	if strings.TrimSpace(*id) == "" {
		fmt.Fprintln(os.Stderr, "--id is required")
		return 1
	}
	if strings.TrimSpace(*caller) == "" {
		fmt.Fprintln(os.Stderr, "--caller is required")
		return 1
	}
	normalizedOutcome := strings.ToLower(strings.TrimSpace(*outcome))
	if normalizedOutcome != "release" && normalizedOutcome != "refund" {
		fmt.Fprintln(os.Stderr, "--outcome must be release or refund")
		return 1
	}
	params := []interface{}{map[string]string{
		"id":      strings.TrimSpace(*id),
		"caller":  strings.TrimSpace(*caller),
		"outcome": normalizedOutcome,
	}}
	_, rpcErr, err := callRPC(rpcURL, auth, "escrow_resolve", params)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RPC call failed: %v\n", err)
		return 1
	}
	if rpcErr != nil {
		printRPCError(rpcErr)
		return 1
	}
	fmt.Println("Arbitration payload submitted successfully.")
	return 0
}

func callRPC(rpcURL, authToken, method string, params []interface{}) (json.RawMessage, *rpcError, error) {
	if params == nil {
		params = []interface{}{}
	}
	reqBody := rpcRequest{JSONRPC: "2.0", Method: method, Params: params, ID: int(time.Now().UnixNano())}
	payload, err := json.Marshal(reqBody)
	if err != nil {
		return nil, nil, err
	}
	httpReq, err := http.NewRequest(http.MethodPost, rpcURL, bytes.NewReader(payload))
	if err != nil {
		return nil, nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(authToken) != "" {
		httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(authToken))
	}
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, nil, fmt.Errorf("rpc status %d: %s", resp.StatusCode, string(body))
	}
	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, nil, err
	}
	if rpcResp.Error != nil {
		return nil, rpcResp.Error, nil
	}
	return rpcResp.Result, nil, nil
}

func printRPCError(err *rpcError) {
	if err == nil {
		return
	}
	fmt.Fprintf(os.Stderr, "RPC error (%d): %s\n", err.Code, err.Message)
	if len(err.Data) > 0 && string(err.Data) != "null" {
		fmt.Fprintf(os.Stderr, "Details: %s\n", strings.TrimSpace(string(err.Data)))
	}
}

func printJSON(v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(data))
	return nil
}

func usage() string {
	return "nhb-escrow usage:\n  nhb-escrow [--rpc URL] [--auth TOKEN] <command> [options]\n\nCommands:\n  realm get --id <realm>       Display arbitration realm metadata\n  snapshot --id <escrow>        Fetch escrow snapshot including frozen policy\n  events [--prefix P] [--limit N]  List recent escrow.* events\n  open --payer A --payee B --token T --amount X --fee-bps N --deadline TS [--realm R] [--mediator M] [--meta 0x..]\n  resolve --id E --caller A --outcome release|refund\n"
}
