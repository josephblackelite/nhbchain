package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// VoucherSubmitter abstracts voucher submission for easier testing.
type VoucherSubmitter interface {
	SubmitVoucher(ctx context.Context, voucher VoucherV1, sigHex string) error
}

type NodeClient struct {
	url        string
	httpClient *http.Client
}

func NewNodeClient(url string) *NodeClient {
	return &NodeClient{
		url:        url,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func (c *NodeClient) SubmitVoucher(ctx context.Context, voucher VoucherV1, sigHex string) error {
	payload := rpcRequest{
		JSONRPC: "2.0",
		ID:      1,
		Method:  "swap_submitVoucher",
		Params: []interface{}{
			map[string]interface{}{
				"voucher": voucher,
				"sig":     sigHex,
			},
		},
	}
	buf, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("rpc request: %w", err)
	}
	defer resp.Body.Close()

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

var _ VoucherSubmitter = (*NodeClient)(nil)
