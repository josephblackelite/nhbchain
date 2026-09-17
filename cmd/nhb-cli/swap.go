package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

func runSwapCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: nhb-cli swap voucher <get|list|export> ...")
		return 1
	}
	switch strings.ToLower(args[0]) {
	case "voucher":
		return runSwapVoucherCommand(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "Unknown swap subcommand %q\n", args[0])
		return 1
	}
}

func runSwapVoucherCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: nhb-cli swap voucher <get|list|export> ...")
		return 1
	}
	switch strings.ToLower(args[0]) {
	case "get":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "Usage: nhb-cli swap voucher get <providerTxId>")
			return 1
		}
		result, err := callSwapRPC("swap_voucher_get", []interface{}{args[1]})
		if err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		var decoded interface{}
		_ = json.Unmarshal(result, &decoded)
		pretty, _ := json.MarshalIndent(decoded, "", "  ")
		fmt.Fprintln(stdout, string(pretty))
		return 0
	case "list":
		if len(args) < 3 {
			fmt.Fprintln(stderr, "Usage: nhb-cli swap voucher list <startTs> <endTs> [cursor] [limit]")
			return 1
		}
		start, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			fmt.Fprintf(stderr, "invalid startTs: %v\n", err)
			return 1
		}
		end, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			fmt.Fprintf(stderr, "invalid endTs: %v\n", err)
			return 1
		}
		params := []interface{}{start, end}
		if len(args) >= 4 && strings.TrimSpace(args[3]) != "" {
			params = append(params, args[3])
		}
		if len(args) >= 5 {
			limit, err := strconv.Atoi(args[4])
			if err != nil {
				fmt.Fprintf(stderr, "invalid limit: %v\n", err)
				return 1
			}
			params = append(params, limit)
		}
		result, err := callSwapRPC("swap_voucher_list", params)
		if err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		var decoded interface{}
		_ = json.Unmarshal(result, &decoded)
		pretty, _ := json.MarshalIndent(decoded, "", "  ")
		fmt.Fprintln(stdout, string(pretty))
		return 0
	case "export":
		if len(args) < 3 {
			fmt.Fprintln(stderr, "Usage: nhb-cli swap voucher export <startTs> <endTs> [output.csv]")
			return 1
		}
		start, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil {
			fmt.Fprintf(stderr, "invalid startTs: %v\n", err)
			return 1
		}
		end, err := strconv.ParseInt(args[2], 10, 64)
		if err != nil {
			fmt.Fprintf(stderr, "invalid endTs: %v\n", err)
			return 1
		}
		result, err := callSwapRPC("swap_voucher_export", []interface{}{start, end})
		if err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
			return 1
		}
		var payload struct {
			CSVBase64    string `json:"csvBase64"`
			Count        int    `json:"count"`
			TotalMintWei string `json:"totalMintWei"`
		}
		if err := json.Unmarshal(result, &payload); err != nil {
			fmt.Fprintf(stderr, "decode export response: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "count: %d\n", payload.Count)
		fmt.Fprintf(stdout, "totalMintWei: %s\n", payload.TotalMintWei)
		data, err := base64.StdEncoding.DecodeString(payload.CSVBase64)
		if err != nil {
			fmt.Fprintf(stderr, "decode csv: %v\n", err)
			return 1
		}
		if len(args) >= 4 && strings.TrimSpace(args[3]) != "" {
			if err := os.WriteFile(args[3], data, 0o644); err != nil {
				fmt.Fprintf(stderr, "write file: %v\n", err)
				return 1
			}
			fmt.Fprintf(stdout, "csv saved to %s\n", args[3])
		} else {
			fmt.Fprintln(stdout, string(data))
		}
		return 0
	default:
		fmt.Fprintf(stderr, "Unknown voucher subcommand %q\n", args[0])
		return 1
	}
}

func callSwapRPC(method string, params []interface{}) (json.RawMessage, error) {
	payload := map[string]interface{}{"id": 1, "method": method, "params": params}
	body, _ := json.Marshal(payload)
	resp, err := doRPCRequest(body, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var rpcResp struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
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
