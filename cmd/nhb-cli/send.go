package main

import (
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"

	"nhbchain/core/types"
	"nhbchain/crypto"
)

const (
	defaultNHBGasLimit  = 21000
	defaultZNHBGasLimit = 25000
)

func runSendNHBCommand(args []string) int {
	gasLimit, gasPrice, positional, err := parseSendCommandFlags("send-nhb", args, defaultNHBGasLimit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		printSendNHBUsage()
		return 1
	}

	if err := sendNHB(positional[0], positional[1], positional[2], gasLimit, gasPrice); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	return 0
}

func runSendZNHBCommand(args []string) int {
	gasLimit, gasPrice, positional, err := parseSendCommandFlags("send-znhb", args, defaultZNHBGasLimit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		printSendZNHBUsage()
		return 1
	}

	if err := sendZNHB(positional[0], positional[1], positional[2], gasLimit, gasPrice); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	return 0
}

func parseSendCommandFlags(name string, args []string, defaultGasLimit uint64) (uint64, uint64, []string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	rpcFlag := rpcEndpoint
	gasLimit := defaultGasLimit
	// Default gas price is 1, matching every other tx type this CLI builds
	// (stake, heartbeat, etc). That default can collide with an
	// already-pending transaction at the same nonce and the same price --
	// concretely, a validator's own account auto-submits heartbeats at
	// price 1, so a manual send from that same key can land on the exact
	// nonce a heartbeat already occupies and get rejected by the mempool's
	// replace-by-fee rule ("fee is not higher"), every single retry,
	// until something actually outbids it. --gas-price exists so an
	// operator hitting that collision can break the tie instead of being
	// stuck -- mirrors the bump-on-retry fix already used internally for
	// the node's own heartbeat resubmission (core/node.go).
	var gasPrice uint64 = 1

	fs.StringVar(&rpcFlag, "rpc", rpcEndpoint, "RPC endpoint (overrides RPC_URL)")
	fs.Uint64Var(&gasLimit, "gas", defaultGasLimit, "Gas limit for the transaction")
	fs.Uint64Var(&gasPrice, "gas-price", 1, "Gas price for the transaction (bump above 1 to outbid a same-nonce transaction already pending in the mempool)")

	if err := fs.Parse(args); err != nil {
		return 0, 0, nil, err
	}

	rpcEndpoint = strings.TrimSpace(rpcFlag)

	positional := fs.Args()
	if len(positional) != 3 {
		return 0, 0, nil, fmt.Errorf("Error: expected recipient, amount, and key file.")
	}

	if gasLimit == 0 {
		return 0, 0, nil, fmt.Errorf("Error: gas limit must be greater than zero.")
	}

	if gasPrice == 0 {
		return 0, 0, nil, fmt.Errorf("Error: gas price must be greater than zero.")
	}

	return gasLimit, gasPrice, positional, nil
}

func printSendUsage(command string) {
	fmt.Printf("Usage: %s [--rpc <url>] [--gas <limit>] [--gas-price <price>] <recipient> <amount> <key_file>\n", command)
}

func printSendNHBUsage() {
	printSendUsage("send-nhb")
}

func printSendZNHBUsage() {
	printSendUsage("send-znhb")
}

func sendNHB(recipient, amountStr, keyFile string, gasLimit, gasPrice uint64) error {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		return fmt.Errorf("loading private key: %w", err)
	}

	dest, err := crypto.DecodeAddress(recipient)
	if err != nil {
		return fmt.Errorf("parsing recipient address: %w", err)
	}

	amount, ok := new(big.Int).SetString(strings.TrimSpace(amountStr), 10)
	if !ok || amount.Sign() <= 0 {
		return fmt.Errorf("amount must be a positive integer")
	}

	account, err := fetchAccount(privKey.PubKey().Address().String())
	if err != nil {
		return fmt.Errorf("fetching account details: %w", err)
	}

	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransfer,
		Nonce:    account.Nonce,
		To:       dest.Bytes(),
		Value:    amount,
		GasLimit: gasLimit,
		GasPrice: new(big.Int).SetUint64(gasPrice),
	}

	if err := tx.Sign(privKey.PrivateKey); err != nil {
		return fmt.Errorf("signing transaction: %w", err)
	}

	hash, err := sendTransaction(&tx)
	if err != nil {
		return fmt.Errorf("sending NHB transfer: %w", err)
	}

	fmt.Printf("Broadcasted NHB transfer: %s\n", hash)
	return nil
}

func sendZNHB(recipient, amountStr, keyFile string, gasLimit, gasPrice uint64) error {
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		return fmt.Errorf("loading private key: %w", err)
	}

	dest, err := crypto.DecodeAddress(recipient)
	if err != nil {
		return fmt.Errorf("parsing recipient address: %w", err)
	}

	amount, ok := new(big.Int).SetString(strings.TrimSpace(amountStr), 10)
	if !ok || amount.Sign() <= 0 {
		return fmt.Errorf("amount must be a positive integer")
	}

	account, err := fetchAccount(privKey.PubKey().Address().String())
	if err != nil {
		return fmt.Errorf("fetching account details: %w", err)
	}

	tx := types.Transaction{
		ChainID:  types.NHBChainID(),
		Type:     types.TxTypeTransferZNHB,
		Nonce:    account.Nonce,
		To:       dest.Bytes(),
		Value:    amount,
		GasLimit: gasLimit,
		GasPrice: new(big.Int).SetUint64(gasPrice),
	}

	if err := tx.Sign(privKey.PrivateKey); err != nil {
		return fmt.Errorf("signing transaction: %w", err)
	}

	hash, err := sendTransaction(&tx)
	if err != nil {
		return fmt.Errorf("sending ZNHB transfer: %w", err)
	}

	fmt.Printf("Broadcasted ZNHB transfer: %s\n", hash)
	return nil
}
