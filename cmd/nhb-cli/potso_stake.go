package main

import (
	"flag"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"

	"github.com/ethereum/go-ethereum/rlp"

	"nhbchain/core/types"
)

type potsoStakeInfoParams struct {
	Owner string `json:"owner"`
}

// potsoStakeAmountPayload mirrors core/potso_stake_tx.go's
// applyPotsoStakeLockTransaction/applyPotsoStakeUnbondTransaction's
// unexported decode-side payload shape -- RLP encodes structs positionally,
// so this only needs to match structurally. Withdraw carries no payload at
// all (see sendPotsoStakeTx).
type potsoStakeAmountPayload struct {
	Amount *big.Int
}

func runPotsoStake(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, potsoStakeUsage())
		return 1
	}
	switch args[0] {
	case "lock":
		return runPotsoStakeLock(args[1:], stdout, stderr)
	case "unbond":
		return runPotsoStakeUnbond(args[1:], stdout, stderr)
	case "withdraw":
		return runPotsoStakeWithdraw(args[1:], stdout, stderr)
	case "info":
		return runPotsoStakeInfo(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown potso stake subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, potsoStakeUsage())
		return 1
	}
}

func potsoStakeUsage() string {
	return "Usage: nhb-cli potso stake <lock|unbond|withdraw|info> [options]"
}

func parseStakeAmount(value string) (*big.Int, error) {
	cleaned := strings.ReplaceAll(strings.TrimSpace(value), "_", "")
	if cleaned == "" {
		return nil, fmt.Errorf("amount is required")
	}
	var exp int
	base := cleaned
	if strings.ContainsAny(cleaned, "eE") {
		parts := strings.Split(strings.ToLower(cleaned), "e")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid amount format")
		}
		base = parts[0]
		parsedExp, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return nil, fmt.Errorf("invalid exponent: %v", err)
		}
		exp = parsedExp
	}
	scale := 0
	if strings.Contains(base, ".") {
		comps := strings.SplitN(base, ".", 2)
		scale = len(comps[1])
		base = comps[0] + comps[1]
	}
	digits := strings.TrimLeft(base, "+")
	if digits == "" {
		return nil, fmt.Errorf("amount is required")
	}
	amt := new(big.Int)
	if _, ok := amt.SetString(digits, 10); !ok {
		return nil, fmt.Errorf("invalid amount")
	}
	expTotal := exp - scale
	if expTotal < 0 {
		return nil, fmt.Errorf("amount must be an integer")
	}
	if expTotal > 0 {
		ten := big.NewInt(10)
		pow := new(big.Int).Exp(ten, big.NewInt(int64(expTotal)), nil)
		amt.Mul(amt, pow)
	}
	if amt.Sign() <= 0 {
		return nil, fmt.Errorf("amount must be positive")
	}
	return amt, nil
}

// sendPotsoStakeTx signs payload as the given POTSO-stake TxType with the
// key loaded from keyFile and broadcasts it via the standard
// nhb_sendTransaction path -- lock/unbond/withdraw are real signed native
// transactions now (core/potso_stake_tx.go), the same as any other
// transaction type in this CLI, not a bespoke RPC call carrying its own
// sha256/secp256k1 signature and authNonce. The owner is whichever key
// signs the transaction; there is no separate --owner address to prove
// against a detached signature anymore.
func sendPotsoStakeTx(keyFile string, txType types.TxType, payload interface{}) (string, error) {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		return "", fmt.Errorf("loading private key: %w", err)
	}
	account, err := fetchAccount(privKey.PubKey().Address().String())
	if err != nil {
		return "", fmt.Errorf("fetching account details: %w", err)
	}
	var data []byte
	if payload != nil {
		data, err = rlp.EncodeToBytes(payload)
		if err != nil {
			return "", fmt.Errorf("encoding payload: %w", err)
		}
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

func runPotsoStakeLock(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso stake lock", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		amount string
		key    string
	)
	fs.StringVar(&amount, "amount", "", "amount of ZNHB to lock (supports scientific notation)")
	fs.StringVar(&key, "key", "wallet.key", "path to the signing key (generate with ./nhb-cli generate-key)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if amount == "" {
		fmt.Fprintln(stderr, "Error: --amount is required")
		return 1
	}
	amt, err := parseStakeAmount(amount)
	if err != nil {
		fmt.Fprintf(stderr, "Error parsing amount: %v\n", err)
		return 1
	}
	hash, err := sendPotsoStakeTx(key, types.TxTypePotsoStakeLock, potsoStakeAmountPayload{Amount: amt})
	if err != nil {
		fmt.Fprintf(stderr, "Error locking stake: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Broadcasted stake lock: %s\n", hash)
	return 0
}

func runPotsoStakeUnbond(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso stake unbond", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		amount string
		key    string
	)
	fs.StringVar(&amount, "amount", "", "amount of ZNHB to unbond")
	fs.StringVar(&key, "key", "wallet.key", "path to the signing key (generate with ./nhb-cli generate-key)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if amount == "" {
		fmt.Fprintln(stderr, "Error: --amount is required")
		return 1
	}
	amt, err := parseStakeAmount(amount)
	if err != nil {
		fmt.Fprintf(stderr, "Error parsing amount: %v\n", err)
		return 1
	}
	hash, err := sendPotsoStakeTx(key, types.TxTypePotsoStakeUnbond, potsoStakeAmountPayload{Amount: amt})
	if err != nil {
		fmt.Fprintf(stderr, "Error unbonding stake: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Broadcasted stake unbond: %s\n", hash)
	return 0
}

func runPotsoStakeWithdraw(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso stake withdraw", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var key string
	fs.StringVar(&key, "key", "wallet.key", "path to the signing key (generate with ./nhb-cli generate-key)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	hash, err := sendPotsoStakeTx(key, types.TxTypePotsoStakeWithdraw, nil)
	if err != nil {
		fmt.Fprintf(stderr, "Error withdrawing stake: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Broadcasted stake withdraw: %s\n", hash)
	return 0
}

func runPotsoStakeInfo(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso stake info", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var owner string
	fs.StringVar(&owner, "owner", "", "bech32 address of the owner")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if owner == "" {
		fmt.Fprintln(stderr, "Error: --owner is required")
		return 1
	}
	params := potsoStakeInfoParams{Owner: owner}
	result, err := callPotsoRPCWithAuth("potso_stake_info", params, true)
	if err != nil {
		fmt.Fprintf(stderr, "Error fetching stake info: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}
