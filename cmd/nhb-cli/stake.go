package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

var stakeRPCCall = callStakeRPC

func runStakeCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, stakeUsage())
		return 1
	}

	switch args[0] {
	case "position":
		return runStakePosition(args[1:], stdout, stderr)
	case "preview":
		return runStakePreview(args[1:], stdout, stderr)
	case "claim":
		return runStakeClaim(args[1:], stdout, stderr)
	default:
		return runLegacyStake(args, stdout, stderr)
	}
}

func runStakePosition(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Usage: nhb-cli stake position <address>")
		return 1
	}
	addr := strings.TrimSpace(args[0])
	if addr == "" {
		fmt.Fprintln(stderr, "Error: address is required")
		return 1
	}

	result, rpcErr, err := stakeRPCCall("stake_getPosition", []interface{}{addr}, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}

	var position stakePositionResponse
	if err := json.Unmarshal(result, &position); err != nil {
		fmt.Fprintf(stderr, "Failed to decode response: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Stake position for %s\n", addr)
	fmt.Fprintf(stdout, "  Shares:       %s\n", position.Shares)
	fmt.Fprintf(stdout, "  Last index:   %s\n", position.LastIndex)
	if position.LastPayoutTs > 0 {
		fmt.Fprintf(stdout, "  Last payout:  %s (%d)\n", formatTimestamp(position.LastPayoutTs), position.LastPayoutTs)
	} else {
		fmt.Fprintln(stdout, "  Last payout:  never")
	}

	return 0
}

func runStakePreview(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Usage: nhb-cli stake preview <address>")
		return 1
	}
	addr := strings.TrimSpace(args[0])
	if addr == "" {
		fmt.Fprintln(stderr, "Error: address is required")
		return 1
	}

	result, rpcErr, err := stakeRPCCall("stake_previewClaim", []interface{}{addr}, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		return handleRPCError(stderr, rpcErr)
	}

	var preview stakePreviewResponse
	if err := json.Unmarshal(result, &preview); err != nil {
		fmt.Fprintf(stderr, "Failed to decode response: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Stake rewards preview for %s\n", addr)
	fmt.Fprintf(stdout, "  Claimable now: %s ZapNHB\n", preview.Payable)
	if preview.NextPayoutTs > 0 {
		fmt.Fprintf(stdout, "  Next payout:   %s (%d)\n", formatTimestamp(preview.NextPayoutTs), preview.NextPayoutTs)
	} else {
		fmt.Fprintln(stdout, "  Next payout:   unavailable")
	}
	return 0
}

func runStakeClaim(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "Usage: nhb-cli stake claim <address>")
		return 1
	}
	addr := strings.TrimSpace(args[0])
	if addr == "" {
		fmt.Fprintln(stderr, "Error: address is required")
		return 1
	}

	result, rpcErr, err := stakeRPCCall("stake_claimRewards", []interface{}{addr}, true)
	if err != nil {
		return handleRPCCallError(stderr, err)
	}
	if rpcErr != nil {
		if strings.EqualFold(rpcErr.Message, "staking not ready") {
			fmt.Fprintln(stdout, "Staking rewards are not available yet. Please try again later.")
			return 0
		}
		return handleRPCError(stderr, rpcErr)
	}

	var claim stakeClaimRewardsResponse
	if err := json.Unmarshal(result, &claim); err != nil {
		fmt.Fprintf(stderr, "Failed to decode response: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Claimed rewards for %s\n", addr)
	fmt.Fprintf(stdout, "  Paid:         %s ZapNHB\n", claim.Paid)
	fmt.Fprintf(stdout, "  Periods:      %d\n", claim.Periods)
	if claim.NextEligible > 0 {
		fmt.Fprintf(stdout, "  Next eligible: %s (%d)\n", formatTimestamp(claim.NextEligible), claim.NextEligible)
	} else {
		fmt.Fprintln(stdout, "  Next eligible: unavailable")
	}

	return 0
}

func runLegacyStake(args []string, stdout, stderr io.Writer) int {
	if len(args) == 2 {
		amountStr := strings.TrimSpace(args[0])
		amount, err := strconv.ParseInt(amountStr, 10, 64)
		if err == nil && amount > 0 {
			stake(amount, args[1])
			return 0
		}
	}
	fmt.Fprintln(stderr, stakeUsage())
	return 1
}

type stakePositionResponse struct {
	Shares       string `json:"shares"`
	LastIndex    string `json:"lastIndex"`
	LastPayoutTs uint64 `json:"lastPayoutTs"`
}

type stakePreviewResponse struct {
	Payable      string `json:"payable"`
	NextPayoutTs uint64 `json:"nextPayoutTs"`
}

type stakeClaimRewardsResponse struct {
	Paid         string `json:"paid"`
	Periods      int    `json:"periods"`
	NextEligible uint64 `json:"next_eligible"`
}

func printStakeAccountSnapshot(w io.Writer, account *balanceResponse) {
	if account == nil {
		return
	}
	fmt.Fprintln(w, "Updated account state:")
	fmt.Fprintf(w, "  Address:   %s\n", account.Address)
	fmt.Fprintf(w, "  NHBCoin:   %s\n", formatBigInt(account.BalanceNHB))
	fmt.Fprintf(w, "  ZapNHB:    %s\n", formatBigInt(account.BalanceZNHB))
	fmt.Fprintf(w, "  Staked:    %s ZapNHB\n", formatBigInt(account.Stake))
	if strings.TrimSpace(account.DelegatedValidator) != "" {
		fmt.Fprintf(w, "  Validator: %s\n", account.DelegatedValidator)
	}
	if len(account.PendingUnbonds) > 0 {
		fmt.Fprintln(w, "  Pending Unbonds:")
		for _, entry := range account.PendingUnbonds {
			fmt.Fprintf(w, "    - ID %d: %s ZapNHB unlocking at %s\n",
				entry.ID,
				formatBigInt(entry.Amount),
				formatTimestamp(entry.ReleaseTime))
		}
	}
}

func formatTimestamp(ts uint64) string {
	if ts == 0 {
		return "unknown"
	}
	return time.Unix(int64(ts), 0).UTC().Format(time.RFC3339)
}

func stakeUsage() string {
	return strings.TrimSpace(`Usage:
  nhb-cli stake <command>

Commands:
  position <address>             Show staking share metadata for an address
  preview <address>              Preview claimable staking rewards and next payout
  claim <address>                Claim staking rewards for an address
  <amount> <key_file>            (legacy) delegate ZapNHB using the original flow
`)
}

func callStakeRPC(method string, params []interface{}, requireAuth bool) (json.RawMessage, *rpcError, error) {
	payload := map[string]interface{}{"id": 1, "method": method}
	if params != nil {
		payload["params"] = params
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
