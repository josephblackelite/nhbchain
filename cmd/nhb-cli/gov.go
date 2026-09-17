package main

import (
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
)

var govRPCCall = callEscrowRPC

// govProposePayload/govVoteChoicePayload/govProposalIDPayload mirror
// core/governance_tx.go's applyGovProposeTransaction/applyGovVoteTransaction/
// applyGovFinalizeTransaction (etc.)'s unexported decode-side payload
// shapes -- RLP encodes/decodes structs positionally, so these only need to
// match structurally.
type govProposePayload struct {
	Kind    string
	Payload string
	Deposit *big.Int
}

type govVoteChoicePayload struct {
	ProposalID uint64
	Choice     string
}

type govProposalIDPayload struct {
	ProposalID uint64
}

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

// sendGovTx signs payload as the given governance TxType with key and
// broadcasts it via the standard nhb_sendTransaction path -- governance
// writes are real signed native transactions now (core/governance_tx.go),
// the same as any other transaction type in this CLI, not a bespoke
// unsigned RPC call. The proposer/voter/finalizer/queuer/executor is
// whichever key signs the transaction; there is no separate --from address
// to spoof.
func sendGovTx(keyFile string, txType types.TxType, payload interface{}) (string, error) {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		return "", fmt.Errorf("loading private key: %w", err)
	}
	account, err := fetchAccount(privKey.PubKey().Address().String())
	if err != nil {
		return "", fmt.Errorf("fetching account details: %w", err)
	}
	data, err := rlp.EncodeToBytes(payload)
	if err != nil {
		return "", fmt.Errorf("encoding payload: %w", err)
	}
	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     txType,
		Nonce:    account.Nonce,
		Data:     data,
		GasLimit: 100_000,
		GasPrice: big.NewInt(1),
	}
	if err := tx.Sign(privKey.PrivateKey); err != nil {
		return "", fmt.Errorf("signing transaction: %w", err)
	}
	hash, err := sendTransaction(&tx)
	if err != nil {
		return "", fmt.Errorf("sending transaction: %w", err)
	}
	return hash, nil
}

func runGovPropose(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gov propose", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		kind    string
		payload string
		keyFile string
		deposit string
	)
	fs.StringVar(&kind, "kind", "", "proposal kind (e.g. param.update)")
	fs.StringVar(&payload, "payload", "", "proposal payload JSON or @path to file")
	fs.StringVar(&keyFile, "key", "", "path to the proposer's local private key file")
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
	if strings.TrimSpace(keyFile) == "" {
		fmt.Fprintln(stderr, "Error: --key is required")
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
	depositWei, ok := new(big.Int).SetString(normalizedDeposit, 10)
	if !ok {
		fmt.Fprintln(stderr, "Error: invalid --deposit amount")
		return 1
	}
	hash, err := sendGovTx(keyFile, types.TxTypeGovPropose, govProposePayload{Kind: kind, Payload: payloadBody, Deposit: depositWei})
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Broadcasted governance proposal: %s\n", hash)
	return 0
}

func runGovVote(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gov vote", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		id      uint64
		keyFile string
		choice  string
	)
	fs.Uint64Var(&id, "id", 0, "proposal identifier")
	fs.StringVar(&keyFile, "key", "", "path to the voter's local private key file")
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
	if strings.TrimSpace(keyFile) == "" {
		fmt.Fprintln(stderr, "Error: --key is required")
		return 1
	}
	normalizedChoice := strings.ToLower(strings.TrimSpace(choice))
	switch normalizedChoice {
	case "yes", "no", "abstain":
	default:
		fmt.Fprintln(stderr, "Error: --choice must be yes, no, or abstain")
		return 1
	}
	hash, err := sendGovTx(keyFile, types.TxTypeGovVote, govVoteChoicePayload{ProposalID: id, Choice: normalizedChoice})
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Broadcasted governance vote: %s\n", hash)
	return 0
}

func runGovFinalize(args []string, stdout, stderr io.Writer) int {
	return runGovSimpleIDTxCommand("finalize", types.TxTypeGovFinalize, args, stdout, stderr)
}

func runGovQueue(args []string, stdout, stderr io.Writer) int {
	return runGovSimpleIDTxCommand("queue", types.TxTypeGovQueue, args, stdout, stderr)
}

func runGovExecute(args []string, stdout, stderr io.Writer) int {
	return runGovSimpleIDTxCommand("execute", types.TxTypeGovExecute, args, stdout, stderr)
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

// runGovSimpleIDTxCommand handles finalize/queue/execute -- each is a real
// signed TxTypeGovFinalize/Queue/Execute transaction now (see
// core/governance_tx.go's doc comments on why these have no
// caller-identity requirement at the engine level: they're pure
// "has the voting/timelock period elapsed" triggers), rather than the old
// bearer-token-gated, unsigned RPC call.
func runGovSimpleIDTxCommand(label string, txType types.TxType, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("gov "+label, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		id      uint64
		keyFile string
	)
	fs.Uint64Var(&id, "id", 0, "proposal identifier")
	fs.StringVar(&keyFile, "key", "", "path to a local private key file to sign this transaction with")
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
	if strings.TrimSpace(keyFile) == "" {
		fmt.Fprintln(stderr, "Error: --key is required")
		return 1
	}
	hash, err := sendGovTx(keyFile, txType, govProposalIDPayload{ProposalID: id})
	if err != nil {
		fmt.Fprintf(stderr, "Error: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Broadcasted governance %s: %s\n", label, hash)
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
  propose   Submit a new governance proposal (--kind --payload --key --deposit)
  vote      Cast a vote on a proposal (--id --key --choice)
  finalize  Finalize voting and tally a proposal (--id --key)
  queue     Queue a passed proposal for execution (--id --key)
  execute   Execute a queued proposal (--id --key)
  show      Show proposal details (--id)
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
