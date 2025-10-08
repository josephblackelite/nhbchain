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
	gasLimit, positional, err := parseSendCommandFlags("send-nhb", args, defaultNHBGasLimit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		printSendNHBUsage()
		return 1
	}

	if err := sendNHB(positional[0], positional[1], positional[2], gasLimit); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	return 0
}

func runSendZNHBCommand(args []string) int {
	gasLimit, positional, err := parseSendCommandFlags("send-znhb", args, defaultZNHBGasLimit)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		printSendZNHBUsage()
		return 1
	}

	if err := sendZNHB(positional[0], positional[1], positional[2], gasLimit); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return 1
	}

	return 0
}

func parseSendCommandFlags(name string, args []string, defaultGasLimit uint64) (uint64, []string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	rpcFlag := rpcEndpoint
	gasLimit := defaultGasLimit

	fs.StringVar(&rpcFlag, "rpc", rpcEndpoint, "RPC endpoint (overrides RPC_URL)")
	fs.Uint64Var(&gasLimit, "gas", defaultGasLimit, "Gas limit for the transaction")

	if err := fs.Parse(args); err != nil {
		return 0, nil, err
	}

	rpcEndpoint = strings.TrimSpace(rpcFlag)

	positional := fs.Args()
	if len(positional) != 3 {
		return 0, nil, fmt.Errorf("Error: expected recipient, amount, and key file.")
	}

	if gasLimit == 0 {
		return 0, nil, fmt.Errorf("Error: gas limit must be greater than zero.")
	}

	return gasLimit, positional, nil
}

func printSendUsage(command string) {
	fmt.Printf("Usage: %s [--rpc <url>] [--gas <limit>] <recipient> <amount> <key_file>\n", command)
}

func printSendNHBUsage() {
	printSendUsage("send-nhb")
}

func printSendZNHBUsage() {
	printSendUsage("send-znhb")
}

func sendNHB(recipient, amountStr, keyFile string, gasLimit uint64) error {
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
		GasPrice: big.NewInt(1),
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

func sendZNHB(recipient, amountStr, keyFile string, gasLimit uint64) error {
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
		GasPrice: big.NewInt(1),
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
