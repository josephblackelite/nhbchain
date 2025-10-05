package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	ethcrypto "github.com/ethereum/go-ethereum/crypto"

	"nhbchain/core/types"
)

type potsoHeartbeatParams struct {
	User          string `json:"user"`
	LastBlock     uint64 `json:"lastBlock"`
	LastBlockHash string `json:"lastBlockHash"`
	Timestamp     int64  `json:"timestamp"`
	Signature     string `json:"signature"`
}

type potsoUserMetersParams struct {
	User string `json:"user"`
	Day  string `json:"day,omitempty"`
}

type potsoTopParams struct {
	Day   string `json:"day,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

func runPotsoCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, potsoUsage())
		return 1
	}
	switch args[0] {
	case "heartbeat":
		return runPotsoHeartbeat(args[1:], stdout, stderr)
	case "user-meters":
		return runPotsoUserMeters(args[1:], stdout, stderr)
	case "top":
		return runPotsoTop(args[1:], stdout, stderr)
	case "stake":
		return runPotsoStake(args[1:], stdout, stderr)
	case "reward":
		return runPotsoReward(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown potso subcommand: %s\n", args[0])
		fmt.Fprintln(stderr, potsoUsage())
		return 1
	}
}

func potsoUsage() string {
	buf := &bytes.Buffer{}
	fmt.Fprintln(buf, "Usage: nhb-cli potso <subcommand> [options]")
	fmt.Fprintln(buf, "Subcommands:")
	fmt.Fprintln(buf, "  heartbeat     Submit a signed heartbeat")
	fmt.Fprintln(buf, "  user-meters   Fetch the raw meters for a user")
	fmt.Fprintln(buf, "  top           List top participants for a day")
	fmt.Fprintln(buf, "  stake         Manage ZapNHB staking locks")
	fmt.Fprintln(buf, "  reward        Manage reward claims, history, and exports")
	return buf.String()
}

func runPotsoHeartbeat(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso heartbeat", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		user    string
		keyFile string
		dayTime string
	)
	fs.StringVar(&user, "user", "", "bech32 address of the participant")
	fs.StringVar(&keyFile, "key", "wallet.key", "path to the signing key (generate with ./nhb-cli generate-key)")
	fs.StringVar(&dayTime, "timestamp", "", "optional explicit UNIX timestamp")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if user == "" {
		fmt.Fprintln(stderr, "Error: --user is required")
		return 1
	}
	privKey, err := loadPrivateKey(keyFile)
	if err != nil {
		fmt.Fprintf(stderr, "Error loading key: %v\n", err)
		return 1
	}
	signerAddr := privKey.PubKey().Address().String()
	if !strings.EqualFold(strings.TrimSpace(signerAddr), strings.TrimSpace(user)) {
		fmt.Fprintf(stderr, "Error: signing key belongs to %s but --user was %s\n", signerAddr, user)
		return 1
	}
	blocks, err := potsoFetchLatestBlocks(1)
	if err != nil || len(blocks) == 0 {
		if err == nil {
			err = fmt.Errorf("no blocks available")
		}
		fmt.Fprintf(stderr, "Error fetching latest block: %v\n", err)
		return 1
	}
	latest := blocks[0]
	hash, err := latest.Header.Hash()
	if err != nil {
		fmt.Fprintf(stderr, "Error hashing block header: %v\n", err)
		return 1
	}
	ts := time.Now().UTC().Unix()
	if strings.TrimSpace(dayTime) != "" {
		parsed, parseErr := parseTimestamp(dayTime)
		if parseErr != nil {
			fmt.Fprintf(stderr, "Error parsing --timestamp: %v\n", parseErr)
			return 1
		}
		ts = parsed
	}
	digest := heartbeatDigest(user, latest.Header.Height, hash, ts)
	sig, err := ethcrypto.Sign(digest, privKey.PrivateKey)
	if err != nil {
		fmt.Fprintf(stderr, "Error signing heartbeat: %v\n", err)
		return 1
	}
	params := potsoHeartbeatParams{
		User:          user,
		LastBlock:     latest.Header.Height,
		LastBlockHash: "0x" + hex.EncodeToString(hash),
		Timestamp:     ts,
		Signature:     "0x" + hex.EncodeToString(sig),
	}
	result, err := callPotsoRPC("potso_heartbeat", params)
	if err != nil {
		fmt.Fprintf(stderr, "Error submitting heartbeat: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func parseTimestamp(value string) (int64, error) {
	cleaned := strings.TrimSpace(value)
	if cleaned == "" {
		return 0, fmt.Errorf("timestamp required")
	}
	if strings.HasPrefix(cleaned, "+") || strings.HasPrefix(cleaned, "-") {
		duration, err := time.ParseDuration(cleaned)
		if err != nil {
			return 0, err
		}
		return time.Now().UTC().Add(duration).Unix(), nil
	}
	if strings.Contains(cleaned, "T") {
		t, err := time.Parse(time.RFC3339, cleaned)
		if err != nil {
			return 0, err
		}
		return t.UTC().Unix(), nil
	}
	return strconv.ParseInt(cleaned, 10, 64)
}

func heartbeatDigest(user string, block uint64, hash []byte, ts int64) []byte {
	payload := fmt.Sprintf("potso_heartbeat|%s|%d|%s|%d", strings.ToLower(strings.TrimSpace(user)), block, strings.ToLower(hex.EncodeToString(hash)), ts)
	digest := sha256.Sum256([]byte(payload))
	return digest[:]
}

func runPotsoUserMeters(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso user-meters", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		user string
		day  string
	)
	fs.StringVar(&user, "user", "", "bech32 address of the participant")
	fs.StringVar(&day, "day", "", "UTC day in YYYY-MM-DD format")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if user == "" {
		fmt.Fprintln(stderr, "Error: --user is required")
		return 1
	}
	params := potsoUserMetersParams{User: user}
	if strings.TrimSpace(day) != "" {
		params.Day = strings.TrimSpace(day)
	}
	result, err := callPotsoRPC("potso_userMeters", params)
	if err != nil {
		fmt.Fprintf(stderr, "Error fetching meters: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func runPotsoTop(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("potso top", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		day   string
		limit int
	)
	fs.StringVar(&day, "day", "", "UTC day in YYYY-MM-DD format")
	fs.IntVar(&limit, "limit", 10, "number of entries to return")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	params := potsoTopParams{Limit: limit}
	if strings.TrimSpace(day) != "" {
		params.Day = strings.TrimSpace(day)
	}
	result, err := callPotsoRPC("potso_top", params)
	if err != nil {
		fmt.Fprintf(stderr, "Error fetching leaderboard: %v\n", err)
		return 1
	}
	printJSONResult(result)
	return 0
}

func callPotsoRPC(method string, param interface{}) (json.RawMessage, error) {
	return callPotsoRPCWithAuth(method, param, false)
}

func callPotsoRPCWithAuth(method string, param interface{}, requireAuth bool) (json.RawMessage, error) {
	payload := map[string]interface{}{"id": 1, "method": method}
	if param != nil {
		payload["params"] = []interface{}{param}
	} else {
		payload["params"] = []interface{}{}
	}
	body, _ := json.Marshal(payload)
	resp, err := doRPCRequest(body, requireAuth)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("failed to decode response from node")
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("error from node: %s", rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}

func potsoFetchLatestBlocks(count int) ([]*types.Block, error) {
	payload := map[string]interface{}{"id": 1, "method": "nhb_getLatestBlocks"}
	if count > 0 {
		payload["params"] = []interface{}{count}
	}
	body, _ := json.Marshal(payload)
	resp, err := doRPCRequest(body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rpcResp struct {
		Result []*types.Block `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return nil, fmt.Errorf("failed to decode response from node")
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("error from node: %s", rpcResp.Error.Message)
	}
	return rpcResp.Result, nil
}
