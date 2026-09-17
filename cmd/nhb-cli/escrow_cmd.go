package main

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/types"
	"nhbchain/crypto"
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

// createEscrowCLIPayload mirrors core/state_transition.go's applyCreateEscrow
// unexported createEscrowPayload struct field-for-field -- Payee/Mediator/Meta
// are plain []byte fields, which encoding/json base64-encodes automatically,
// exactly what the chain-side decoder expects.
type createEscrowCLIPayload struct {
	Payee    []byte   `json:"payee"`
	Token    string   `json:"token"`
	Amount   *big.Int `json:"amount"`
	FeeBps   uint32   `json:"feeBps"`
	Deadline int64    `json:"deadline"`
	Nonce    uint64   `json:"nonce"`
	Mediator []byte   `json:"mediator,omitempty"`
	Meta     []byte   `json:"meta,omitempty"`
	Realm    string   `json:"realm,omitempty"`
}

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
	case "create-realm":
		return runEscrowCreateRealm(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown escrow subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, escrowUsage())
		return 1
	}
}

// runEscrowCreate builds, signs, and broadcasts a real TxTypeCreateEscrow
// transaction. escrow_create (the RPC method this used to call) is
// permanently disabled -- see rpc/escrow_handlers.go's
// escrowRPCDisabledMessage -- because it mutated validator state directly
// outside the block pipeline, guaranteeing a consensus fork on this
// 2-validator chain. There is no --payer flag: the signing key's own
// address becomes the payer, on-chain, exactly like every other rewritten
// command in this file -- a plaintext caller string is not proof of
// anything, a transaction signature is.
func runEscrowCreate(args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet("escrow create", stderr)
	var (
		payee     string
		token     string
		amountStr string
		feeBpsStr string
		deadline  string
		mediator  string
		meta      string
		realm     string
		nonceStr  string
		keyFile   string
	)
	fs.StringVar(&payee, "payee", "", "payee bech32 address")
	fs.StringVar(&token, "token", "", "token symbol (NHB or ZNHB)")
	fs.StringVar(&amountStr, "amount", "", "escrow amount (supports 100e18 shorthand)")
	fs.StringVar(&feeBpsStr, "fee-bps", "", "fee in basis points")
	fs.StringVar(&deadline, "deadline", "", "deadline as +duration or RFC3339 timestamp")
	fs.StringVar(&mediator, "mediator", "", "optional mediator bech32 address")
	fs.StringVar(&meta, "meta", "", "optional 0x-prefixed metadata hash (<=32 bytes)")
	fs.StringVar(&realm, "realm", "", "optional realm identifier")
	fs.StringVar(&nonceStr, "nonce", "", "unique escrow nonce (distinct from the account/tx nonce -- part of the escrow's own ID derivation)")
	fs.StringVar(&keyFile, "key", "", "path to the payer's private key file")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
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
	amount, ok := new(big.Int).SetString(normalizedAmount, 10)
	if !ok {
		return printEscrowError(stderr, "--amount could not be parsed")
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
	if keyFile == "" {
		return printEscrowError(stderr, "--key is required (path to the payer's private key file)")
	}

	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		return printEscrowError(stderr, fmt.Sprintf("loading private key: %v", err))
	}
	payerAddr := privKey.PubKey().Address()

	payeeAddr, err := crypto.DecodeAddress(payee)
	if err != nil {
		return printEscrowError(stderr, fmt.Sprintf("invalid --payee: %v", err))
	}

	payload := createEscrowCLIPayload{
		Payee:    payeeAddr.Bytes(),
		Token:    normalizedToken,
		Amount:   amount,
		FeeBps:   uint32(feeBpsValue),
		Deadline: deadlineUnix,
		Nonce:    nonceValue,
		Realm:    strings.TrimSpace(realm),
	}
	var metaHash [32]byte
	if trimmed := strings.TrimSpace(meta); trimmed != "" {
		metaBytes, err := decodeEscrowMetaHex(trimmed)
		if err != nil {
			return printEscrowError(stderr, err.Error())
		}
		payload.Meta = metaBytes[:]
		metaHash = metaBytes
	}
	if trimmed := strings.TrimSpace(mediator); trimmed != "" {
		mediatorAddr, err := crypto.DecodeAddress(trimmed)
		if err != nil {
			return printEscrowError(stderr, fmt.Sprintf("invalid --mediator: %v", err))
		}
		payload.Mediator = mediatorAddr.Bytes()
	}

	// The escrow ID is deterministic (keccak256(payer, payee, metaHash,
	// nonce) -- see native/escrow/engine.go's Create), so it can be computed
	// and shown up front, before the transaction is even sent, exactly like
	// the escrow-gateway's POST /escrow/create returns it synchronously.
	var payerBytes, payeeBytes [20]byte
	copy(payerBytes[:], payerAddr.Bytes())
	copy(payeeBytes[:], payeeAddr.Bytes())
	var nonceBuf [8]byte
	binary.BigEndian.PutUint64(nonceBuf[:], nonceValue)
	escrowID := ethcrypto.Keccak256Hash(payerBytes[:], payeeBytes[:], metaHash[:], nonceBuf[:])

	data, err := json.Marshal(payload)
	if err != nil {
		return printEscrowError(stderr, fmt.Sprintf("encoding payload: %v", err))
	}
	if err := signAndSendTx(types.TxTypeCreateEscrow, data, keyFile); err != nil {
		fmt.Fprintf(stderr, "Error creating escrow: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Escrow creation transaction sent. Escrow ID: 0x%s\n", hex.EncodeToString(escrowID[:]))
	fmt.Fprintln(stdout, "Check the node logs for confirmation, then 'escrow get --id <id>' to verify it landed.")
	return 0
}

func decodeEscrowMetaHex(value string) ([32]byte, error) {
	var out [32]byte
	trimmed := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(value), "0x"), "0X")
	raw, err := hex.DecodeString(trimmed)
	if err != nil {
		return out, fmt.Errorf("--meta must be a hex string: %w", err)
	}
	if len(raw) > len(out) {
		return out, fmt.Errorf("--meta must be <= %d bytes", len(out))
	}
	copy(out[:], raw)
	return out, nil
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

// runEscrowFund builds, signs, and broadcasts TxTypeLockEscrow (tx.Data is
// the bare 32-byte escrow id, per core/state_transition.go's
// applyLockEscrow) -- the on-chain equivalent of the disabled escrow_fund
// RPC. There is no --from flag: the signing key's own address is the
// funder, cryptographically.
func runEscrowFund(args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet("escrow fund", stderr)
	var (
		id      string
		keyFile string
	)
	fs.StringVar(&id, "id", "", "escrow identifier")
	fs.StringVar(&keyFile, "key", "", "path to the funder's (payer's) private key file")
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
	if keyFile == "" {
		return printEscrowError(stderr, "--key is required")
	}
	idBytes, err := decodeEscrowIDHex(id)
	if err != nil {
		return printEscrowError(stderr, err.Error())
	}
	if err := signAndSendTx(types.TxTypeLockEscrow, idBytes, keyFile); err != nil {
		fmt.Fprintf(stderr, "Error funding escrow: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Escrow fund transaction sent. Check the node logs for confirmation.")
	return 0
}

func runEscrowRelease(args []string, stdout, stderr io.Writer) int {
	return runEscrowTransition(types.TxTypeReleaseEscrow, "release funds to the payee", args, stdout, stderr)
}

func runEscrowRefund(args []string, stdout, stderr io.Writer) int {
	return runEscrowTransition(types.TxTypeRefundEscrow, "refund funds to the payer", args, stdout, stderr)
}

// runEscrowDispute builds, signs, and broadcasts TxTypeDisputeEscrow. Unlike
// the old escrow_dispute RPC, the direct on-chain dispute path (applyDisputeEscrow,
// core/state_transition.go) carries no reason/message field at all -- tx.Data
// is just the bare escrow id, and Engine.Dispute is always called with an
// empty reason string on this path. A dispute reason is only supported via
// the delegated meta-transaction path (TxTypeDelegatedDisputeEscrow) that the
// escrow-gateway service uses, which requires signing a JSON envelope rather
// than a bare transaction -- not something this simple CLI command builds.
func runEscrowDispute(args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet("escrow dispute", stderr)
	var (
		id      string
		keyFile string
	)
	fs.StringVar(&id, "id", "", "escrow identifier")
	fs.StringVar(&keyFile, "key", "", "path to the payer's or payee's private key file")
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
	if keyFile == "" {
		return printEscrowError(stderr, "--key is required")
	}
	idBytes, err := decodeEscrowIDHex(id)
	if err != nil {
		return printEscrowError(stderr, err.Error())
	}
	if err := signAndSendTx(types.TxTypeDisputeEscrow, idBytes, keyFile); err != nil {
		fmt.Fprintf(stderr, "Error disputing escrow: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Escrow dispute transaction sent (no reason attached -- see this command's source comment). Check the node logs for confirmation.")
	return 0
}

// runEscrowResolve is deliberately NOT wired to a working transaction.
// escrow_resolve (the old RPC) assumed a single designated mediator could
// resolve with a plain outcome string -- that model no longer exists.
// Real on-chain resolution now goes through TxTypeArbitrateRelease/
// ArbitrateRefund (core/state_transition.go's applyArbitrate), which calls
// Engine.ResolveWithSignatures: it requires the escrow to have been created
// against a registered arbitration realm (a committee of arbitrators plus a
// signing threshold) and a bundle of raw signatures from that committee
// meeting the threshold -- a fundamentally different, multi-party flow this
// single-command CLI subcommand cannot honestly reduce to a --caller/--outcome
// pair. Matches the escrow-gateway's own POST /escrow/resolve, which returns
// a plain 503 for the identical reason (see docs/escrow/nhbchain-escrow-gateway.md).
func runEscrowResolve(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "Error: escrow resolve is not available via this command.")
	fmt.Fprintln(stderr, "Resolution now requires a registered arbitration realm and a threshold of arbitrator")
	fmt.Fprintln(stderr, "signatures (TxTypeArbitrateRelease/ArbitrateRefund, core/state_transition.go's applyArbitrate)")
	fmt.Fprintln(stderr, "-- a multi-party flow, not a single caller+outcome RPC call. The escrow-gateway's own")
	fmt.Fprintln(stderr, "POST /escrow/resolve returns 503 for the same reason until a realm is provisioned.")
	return 1
}

// createRealmCLIPayload mirrors core/state_transition.go's decodeEscrowRealmPayload
// field-for-field. Only the fields a client can actually set -- Version,
// NextPolicyNonce, CreatedAt/UpdatedAt are server-assigned (native/escrow/engine.go's
// CreateRealm) and have no corresponding JSON field here at all.
type createRealmCLIPayload struct {
	ID                 string   `json:"id"`
	Threshold          uint32   `json:"threshold"`
	Scheme             uint8    `json:"scheme"`
	Members            []string `json:"members"`
	FeeBps             uint32   `json:"feeBps,omitempty"`
	FeeRecipient       string   `json:"feeRecipient,omitempty"`
	Scope              uint8    `json:"scope"`
	ProviderProfile    string   `json:"providerProfile"`
	ArbitrationFeeBps  uint32   `json:"arbitrationFeeBps,omitempty"`
	FeeRecipientBech32 string   `json:"feeRecipientBech32,omitempty"`
}

// runEscrowCreateRealm builds, signs, and broadcasts a TxTypeEscrowCreateRealm
// transaction (native/escrow/engine.go's CreateRealm) -- previously built but
// entirely unreachable from this CLI; escrow resolve/arbitration has nothing
// to operate against without at least one realm existing. The signing key
// must already hold the on-chain role ROLE_ESCROW_REALM_ADMIN (granted via a
// governance role.allowlist proposal, see docs/governance/*) -- this command
// does not grant that role itself, it only submits the realm-creation
// transaction, which core/state_transition.go's applyEscrowCreateRealm
// rejects outright if the signer lacks the role.
func runEscrowCreateRealm(args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet("escrow create-realm", stderr)
	var (
		id                 string
		threshold          uint
		scheme             string
		members            string
		scope              string
		providerProfile    string
		feeBps             uint
		feeRecipient       string
		arbitrationFeeBps  uint
		feeRecipientBech32 string
		keyFile            string
	)
	fs.StringVar(&id, "id", "", "realm identifier")
	fs.UintVar(&threshold, "threshold", 1, "number of arbitrator signatures required to resolve a dispute")
	fs.StringVar(&scheme, "scheme", "single", "arbitration scheme: single or committee")
	fs.StringVar(&members, "members", "", "comma-separated arbitrator bech32 addresses")
	fs.StringVar(&scope, "scope", "platform", "realm scope: platform or marketplace")
	fs.StringVar(&providerProfile, "provider-profile", "", "free-text description of who operates this realm")
	fs.UintVar(&feeBps, "fee-bps", 0, "optional escrow settlement fee in basis points (requires --fee-recipient)")
	fs.StringVar(&feeRecipient, "fee-recipient", "", "bech32 address to receive --fee-bps (required if --fee-bps > 0)")
	fs.UintVar(&arbitrationFeeBps, "arbitration-fee-bps", 0, "optional arbitration fee in basis points (requires --fee-recipient-bech32)")
	fs.StringVar(&feeRecipientBech32, "fee-recipient-bech32", "", "bech32 address to receive --arbitration-fee-bps")
	fs.StringVar(&keyFile, "key", "", "path to a private key file holding ROLE_ESCROW_REALM_ADMIN")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintln(stderr, "Error: unexpected positional arguments")
		return 1
	}
	if strings.TrimSpace(id) == "" {
		return printEscrowError(stderr, "--id is required")
	}
	memberList := make([]string, 0)
	for _, m := range strings.Split(members, ",") {
		if trimmed := strings.TrimSpace(m); trimmed != "" {
			memberList = append(memberList, trimmed)
		}
	}
	if len(memberList) == 0 {
		return printEscrowError(stderr, "--members is required (comma-separated bech32 addresses)")
	}
	if threshold == 0 || threshold > uint(len(memberList)) {
		return printEscrowError(stderr, "--threshold must be between 1 and the number of --members")
	}
	var schemeValue uint8
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "single":
		schemeValue = 1
	case "committee":
		schemeValue = 2
	default:
		return printEscrowError(stderr, "--scheme must be single or committee")
	}
	var scopeValue uint8
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "platform":
		scopeValue = 1
	case "marketplace":
		scopeValue = 2
	default:
		return printEscrowError(stderr, "--scope must be platform or marketplace")
	}
	if strings.TrimSpace(providerProfile) == "" {
		return printEscrowError(stderr, "--provider-profile is required")
	}
	if feeBps > 0 && strings.TrimSpace(feeRecipient) == "" {
		return printEscrowError(stderr, "--fee-recipient is required when --fee-bps > 0")
	}
	if arbitrationFeeBps > 0 && strings.TrimSpace(feeRecipientBech32) == "" {
		return printEscrowError(stderr, "--fee-recipient-bech32 is required when --arbitration-fee-bps > 0")
	}
	if keyFile == "" {
		return printEscrowError(stderr, "--key is required (path to a private key file holding ROLE_ESCROW_REALM_ADMIN)")
	}

	payload := createRealmCLIPayload{
		ID:                 strings.TrimSpace(id),
		Threshold:          uint32(threshold),
		Scheme:             schemeValue,
		Members:            memberList,
		Scope:              scopeValue,
		ProviderProfile:    strings.TrimSpace(providerProfile),
		FeeBps:             uint32(feeBps),
		FeeRecipient:       strings.TrimSpace(feeRecipient),
		ArbitrationFeeBps:  uint32(arbitrationFeeBps),
		FeeRecipientBech32: strings.TrimSpace(feeRecipientBech32),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return printEscrowError(stderr, fmt.Sprintf("encoding payload: %v", err))
	}
	if err := signAndSendTx(types.TxTypeEscrowCreateRealm, data, keyFile); err != nil {
		fmt.Fprintf(stderr, "Error creating realm: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Realm creation transaction sent for realm %q. Check the node logs for confirmation.\n", payload.ID)
	return 0
}

func runEscrowExpire(args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet("escrow expire", stderr)
	var (
		id      string
		keyFile string
	)
	fs.StringVar(&id, "id", "", "escrow identifier")
	fs.StringVar(&keyFile, "key", "", "path to any private key file -- TxTypeExpireEscrow is deliberately permissionless, any funded account may pay gas to sweep a stale escrow")
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
	if keyFile == "" {
		return printEscrowError(stderr, "--key is required")
	}
	idBytes, err := decodeEscrowIDHex(id)
	if err != nil {
		return printEscrowError(stderr, err.Error())
	}
	if err := signAndSendTx(types.TxTypeExpireEscrow, idBytes, keyFile); err != nil {
		fmt.Fprintf(stderr, "Error expiring escrow: %v\n", err)
		return 1
	}
	fmt.Fprintln(stdout, "Escrow expire transaction sent. Check the node logs for confirmation.")
	return 0
}

func runEscrowTransition(txType types.TxType, description string, args []string, stdout, stderr io.Writer) int {
	fs := newEscrowFlagSet(fmt.Sprintf("escrow %s", description), stderr)
	var (
		id      string
		keyFile string
	)
	fs.StringVar(&id, "id", "", "escrow identifier")
	fs.StringVar(&keyFile, "key", "", "path to the actor's private key file")
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
	if keyFile == "" {
		return printEscrowError(stderr, "--key is required")
	}
	idBytes, err := decodeEscrowIDHex(id)
	if err != nil {
		return printEscrowError(stderr, err.Error())
	}
	if err := signAndSendTx(txType, idBytes, keyFile); err != nil {
		fmt.Fprintf(stderr, "Error performing %s: %v\n", description, err)
		return 1
	}
	fmt.Fprintf(stdout, "Escrow %s transaction sent. Check the node logs for confirmation.\n", description)
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
  create  Sign and submit a new escrow (requires --key)
  get     Fetch escrow details by id (read-only)
  fund    Sign and submit funding for an escrow (requires --key)
  release Sign and submit a release to the payee (requires --key)
  refund  Sign and submit a refund to the payer (requires --key)
  expire  Sign and submit a permissionless sweep of a stale escrow (requires --key)
  dispute Sign and submit a dispute flag (requires --key; carries no reason -- see 'dispute' source comment)
  resolve Not available via this command -- requires realm-based arbitrator signatures, see error message
  create-realm Sign and submit a new arbitration realm (requires --key holding ROLE_ESCROW_REALM_ADMIN)
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

// decodeEscrowIDHex parses an already-validated (via validateEscrowID)
// 0x-prefixed 32-byte hex escrow id into the raw bytes core/state_transition.go's
// decodeEscrowID expects as tx.Data for the plain (non-delegated) escrow
// lifecycle transactions -- those decode tx.Data directly as the raw id,
// with no JSON wrapper.
func decodeEscrowIDHex(value string) ([]byte, error) {
	trimmed := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(value), "0x"), "0X")
	raw, err := hex.DecodeString(trimmed)
	if err != nil {
		return nil, fmt.Errorf("--id must be valid hex: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("--id must be 32 bytes")
	}
	return raw, nil
}
