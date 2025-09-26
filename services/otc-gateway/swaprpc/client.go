package swaprpc

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

// Client provides a thin JSON-RPC wrapper for swap voucher submission.
type Client struct {
	url        string
	provider   string
	httpClient *http.Client
	nextID     atomic.Int64
}

// Config represents the client configuration.
type Config struct {
	URL      string
	Provider string
	Timeout  time.Duration
}

// NewClient constructs a JSON-RPC client targeting the supplied URL.
func NewClient(cfg Config) *Client {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{
		url:      strings.TrimSpace(cfg.URL),
		provider: strings.TrimSpace(cfg.Provider),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}
}

// VoucherStatus reflects the current state of a voucher recorded on the node.
type VoucherStatus struct {
	Status string
	TxHash string
}

// SubmitMintVoucher posts a mint voucher with signature to the swap gateway RPC.
func (c *Client) SubmitMintVoucher(ctx context.Context, voucher core.MintVoucher, signatureHex, providerTxID string) (string, bool, error) {
	payload := map[string]interface{}{
		"voucher":      voucher,
		"sig":          signatureHex,
		"provider":     c.provider,
		"providerTxId": providerTxID,
	}
	var result struct {
		TxHash string `json:"txHash"`
		Minted bool   `json:"minted"`
	}
	if err := c.call(ctx, "swap_submitVoucher", []interface{}{payload}, &result); err != nil {
		return "", false, err
	}
	return strings.TrimSpace(result.TxHash), result.Minted, nil
}

// GetVoucher retrieves the on-chain voucher record for the supplied provider identifier.
func (c *Client) GetVoucher(ctx context.Context, providerTxID string) (*VoucherStatus, error) {
	var result map[string]interface{}
	if err := c.call(ctx, "swap_voucher_get", []interface{}{providerTxID}, &result); err != nil {
		return nil, err
	}
	status := ""
	if v, ok := result["status"].(string); ok {
		status = strings.TrimSpace(v)
	}
	txHash := ""
	if v, ok := result["txHash"].(string); ok {
		txHash = strings.TrimSpace(v)
	}
	return &VoucherStatus{Status: status, TxHash: txHash}, nil
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int64         `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	if c == nil || c.httpClient == nil {
		return fmt.Errorf("swaprpc: client not configured")
	}
	id := c.nextID.Add(1)
	reqBody := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("swaprpc: decode response: %w", err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("swaprpc: error %d %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("swaprpc: unexpected status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if len(rpcResp.Result) == 0 {
		return fmt.Errorf("swaprpc: empty result")
	}
	return json.Unmarshal(rpcResp.Result, out)
}

var _ interface {
	SubmitMintVoucher(context.Context, core.MintVoucher, string, string) (string, bool, error)
	GetVoucher(context.Context, string) (*VoucherStatus, error)
} = (*Client)(nil)
