package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"
)

type potsoRewardClaimCLIParams struct {
	Epoch     uint64 `json:"epoch"`
	Address   string `json:"address"`
	Signature string `json:"signature"`
}

type potsoRewardHistoryCLIParams struct {
	Address string `json:"address"`
	Cursor  string `json:"cursor,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type potsoRewardExportCLIParams struct {
	Epoch uint64 `json:"epoch"`
}

type potsoRewardExportCLIResult struct {
	Epoch     uint64 `json:"epoch"`
	CSVBase64 string `json:"csvBase64"`
	TotalPaid string `json:"totalPaid"`
	Winners   int    `json:"winners"`
}

func runPotsoReward(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, potsoRewardUsage())
		return 1
	}
	switch args[0] {
	case "claim":
		return runPotsoRewardClaim(args[1:], stdout, stderr)
	case "history":
		return runPotsoRewardHistory(args[1:], stdout, stderr)
	case "export":
		return runPotsoRewardExport(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown potso reward subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, potsoRewardUsage())
		return 1
	}
}

func potsoRewardUsage() string {
	builder := &strings.Builder{}
	fmt.Fprintln(builder, "Usage: nhb-cli potso reward <subcommand> [options]")
	fmt.Fprintln(builder, "Subcommands:")
	fmt.Fprintln(builder, "  claim    Claim a pending reward")
	fmt.Fprintln(builder, "  history  View reward settlement history")
	fmt.Fprintln(builder, "  export   Export an epoch payout ledger as CSV")
	return builder.String()
}

func potsoRewardClaimDigest(epoch uint64, addr string) []byte {
	normalized := strings.ToLower(strings.TrimSpace(addr))
	payload := fmt.Sprintf("potso_reward_claim|%d|%s", epoch, normalized)
	digest := sha256.Sum256([]byte(payload))
	return digest[:]
}

func runPotsoRewardClaim(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso reward claim", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		epoch uint64
		addr  string
		key   string
	)
	fs.Uint64Var(&epoch, "epoch", 0, "reward epoch number")
	fs.StringVar(&addr, "addr", "", "bech32 address to claim for")
	fs.StringVar(&key, "key", "wallet.key", "path to signing key (generate with ./nhb-cli generate-key)")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if addr == "" {
		fmt.Fprintln(stderr, "Error: --addr is required")
		return 1
	}
	if epoch == 0 {
		fmt.Fprintln(stderr, "Error: --epoch is required")
		return 1
	}
	privKey, err := loadPrivateKey(key)
	if err != nil {
		fmt.Fprintf(stderr, "Error loading key: %v\n", err)
		return 1
	}
	signer := privKey.PubKey().Address().String()
	if !strings.EqualFold(strings.TrimSpace(signer), strings.TrimSpace(addr)) {
		fmt.Fprintf(stderr, "Error: signing key belongs to %s but --addr was %s\n", signer, addr)
		return 1
	}
	digest := potsoRewardClaimDigest(epoch, addr)
	sig, err := ethcrypto.Sign(digest, privKey.PrivateKey)
	if err != nil {
		fmt.Fprintf(stderr, "Error signing claim: %v\n", err)
		return 1
	}
	params := potsoRewardClaimCLIParams{Epoch: epoch, Address: addr, Signature: "0x" + strings.ToLower(hex.EncodeToString(sig))}
	result, err := callPotsoRPCWithAuth("potso_reward_claim", params, true)
	if err != nil {
		fmt.Fprintf(stderr, "Error submitting claim: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func runPotsoRewardHistory(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso reward history", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		addr   string
		cursor string
		limit  int
	)
	fs.StringVar(&addr, "addr", "", "bech32 address to query")
	fs.StringVar(&cursor, "cursor", "", "optional pagination cursor")
	fs.IntVar(&limit, "limit", 0, "optional page size")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if addr == "" {
		fmt.Fprintln(stderr, "Error: --addr is required")
		return 1
	}
	params := potsoRewardHistoryCLIParams{Address: addr}
	if strings.TrimSpace(cursor) != "" {
		params.Cursor = strings.TrimSpace(cursor)
	}
	if limit > 0 {
		params.Limit = limit
	}
	result, err := callPotsoRPC("potso_rewards_history", params)
	if err != nil {
		fmt.Fprintf(stderr, "Error fetching history: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func runPotsoRewardExport(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso reward export", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var epoch uint64
	fs.Uint64Var(&epoch, "epoch", 0, "reward epoch number")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if epoch == 0 {
		fmt.Fprintln(stderr, "Error: --epoch is required")
		return 1
	}
	params := potsoRewardExportCLIParams{Epoch: epoch}
	raw, err := callPotsoRPC("potso_export_epoch", params)
	if err != nil {
		fmt.Fprintf(stderr, "Error exporting epoch: %v\n", err)
		return 1
	}
	var result potsoRewardExportCLIResult
	if err := json.Unmarshal(raw, &result); err != nil {
		fmt.Fprintf(stderr, "Error decoding export result: %v\n", err)
		return 1
	}
	data, err := base64.StdEncoding.DecodeString(result.CSVBase64)
	if err != nil {
		fmt.Fprintf(stderr, "Error decoding CSV payload: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(data); err != nil {
		fmt.Fprintf(stderr, "Error writing CSV: %v\n", err)
		return 1
	}
	return 0
}
