package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"nhbchain/core"
)

// NodeClient exposes the minimal RPC surface required by the payments gateway.
type NodeClient interface {
	MintWithSig(ctx context.Context, voucher core.MintVoucher, signature string) (string, error)
}

// RPCNodeClient is a lightweight JSON-RPC client.
type RPCNodeClient struct {
	baseURL   string
	authToken string
	http      *http.Client
	nextID    atomic.Int64
}

// NewRPCNodeClient constructs a new RPC client.
func NewRPCNodeClient(baseURL, authToken string) *RPCNodeClient {
	return &RPCNodeClient{
		baseURL:   baseURL,
		authToken: authToken,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *RPCNodeClient) MintWithSig(ctx context.Context, voucher core.MintVoucher, signature string) (string, error) {
	params := []interface{}{voucher, signature}
	var result struct {
		TxHash string `json:"txHash"`
	}
	if err := c.call(ctx, "mint_with_sig", params, &result); err != nil {
		return "", err
	}
	return result.TxHash, nil
}

func (c *RPCNodeClient) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	id := c.nextID.Add(1)
	bodyStruct := map[string]interface{}{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	buf, err := json.Marshal(bodyStruct)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if strings.TrimSpace(c.authToken) != "" {
		req.Header.Set("Authorization", "Bearer "+c.authToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("node rpc %s failed: status=%d", method, resp.StatusCode)
	}
	var rpcResp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return err
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("node rpc error: %s", rpcResp.Error.Message)
	}
	if out == nil {
		return nil
	}
	if len(rpcResp.Result) == 0 {
		return fmt.Errorf("node rpc returned empty result")
	}
	return json.Unmarshal(rpcResp.Result, out)
}
