// Package rpcclient is a minimal JSON-RPC 2.0 client scoped to the
// chain-side methods buybackd needs: the buyback engine's own
// buyback_getRefPriceStatus/buyback_submitRefPrice, plus
// lending_getRefPriceStatus/lending_submitRefPrice for the lending oracle
// submission this same process also performs (see
// services/buybackd/refprice's AttemptLendingRefPrice doc comment on why
// buybackd, not a separate service, owns this) -- mirroring
// services/lending/engine/rpcclient's shape rather than importing it, since
// that package is scoped to the lending engine's own method set and pulling
// it in here would be a layering violation for an unrelated domain.
package rpcclient

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// Config controls how the Client connects to the chain node's RPC endpoint.
type Config struct {
	BaseURL     string
	BearerToken string
	Timeout     time.Duration
}

// Client is a small, dependency-free JSON-RPC 2.0 client.
type Client struct {
	baseURL string
	bearer  string
	http    *http.Client
}

// NewClient constructs a Client from cfg.
func NewClient(cfg Config) (*Client, error) {
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("rpcclient: base URL required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &Client{
		baseURL: baseURL,
		bearer:  strings.TrimSpace(cfg.BearerToken),
		http:    &http.Client{Timeout: timeout},
	}, nil
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
}

type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string {
	if e == nil {
		return ""
	}
	if len(e.Data) > 0 {
		return fmt.Sprintf("rpc error %d: %s: %s", e.Code, e.Message, string(e.Data))
	}
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

func (c *Client) call(ctx context.Context, method string, params any, result any) error {
	if c == nil {
		return fmt.Errorf("rpcclient: client is nil")
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params}); err != nil {
		return fmt.Errorf("rpcclient: encode request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, &buf)
	if err != nil {
		return fmt.Errorf("rpcclient: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Client", "buybackd")
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rpcclient: call %s: %w", method, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("rpcclient: %s failed with status %s", method, resp.Status)
	}
	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("rpcclient: decode %s response: %w", method, err)
	}
	if rpcResp.Error != nil {
		return rpcResp.Error
	}
	if result != nil && rpcResp.Result != nil {
		if err := json.Unmarshal(rpcResp.Result, result); err != nil {
			return fmt.Errorf("rpcclient: decode %s result: %w", method, err)
		}
	}
	return nil
}

// RefPriceStatus mirrors core.BuybackRefPriceStatus's JSON shape.
type RefPriceStatus struct {
	Epoch       uint64 `json:"epoch"`
	HasRefPrice bool   `json:"hasRefPrice"`
	RateNum     string `json:"rateNum,omitempty"`
	RateDenom   string `json:"rateDenom,omitempty"`
	TimestampAt uint64 `json:"timestampAt,omitempty"`
	SignerCount int    `json:"signerCount,omitempty"`
}

// GetRefPriceStatus calls buyback_getRefPriceStatus. Pass epoch=nil to ask
// for the current open epoch's status.
func (c *Client) GetRefPriceStatus(ctx context.Context, epoch *uint64) (*RefPriceStatus, error) {
	var params any
	if epoch != nil {
		params = []map[string]uint64{{"epoch": *epoch}}
	}
	var status RefPriceStatus
	if err := c.call(ctx, "buyback_getRefPriceStatus", params, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// SubmitRefPrice calls the auth-gated buyback_submitRefPrice with an
// already-signed M-of-N bundle.
func (c *Client) SubmitRefPrice(ctx context.Context, rateNum, rateDenom *big.Int, epoch, timestamp uint64, signatures [][]byte) (string, error) {
	if rateNum == nil || rateDenom == nil {
		return "", fmt.Errorf("rpcclient: rate required")
	}
	sigHexes := make([]string, len(signatures))
	for i, sig := range signatures {
		sigHexes[i] = "0x" + hex.EncodeToString(sig)
	}
	payload := map[string]any{
		"rateNum":    rateNum.String(),
		"rateDenom":  rateDenom.String(),
		"epoch":      epoch,
		"timestamp":  timestamp,
		"signatures": sigHexes,
	}
	var result struct {
		TxHash string `json:"txHash"`
	}
	if err := c.call(ctx, "buyback_submitRefPrice", []any{payload}, &result); err != nil {
		return "", err
	}
	return result.TxHash, nil
}

// LendingRefPriceStatus mirrors core.LendingRefPriceStatus's JSON shape.
type LendingRefPriceStatus struct {
	HasRefPrice  bool   `json:"hasRefPrice"`
	RateNum      string `json:"rateNum,omitempty"`
	RateDenom    string `json:"rateDenom,omitempty"`
	Timestamp    uint64 `json:"timestamp,omitempty"`
	SignerCount  int    `json:"signerCount,omitempty"`
	AppliedBlock uint64 `json:"appliedBlock,omitempty"`
	MarketCount  int    `json:"marketCount,omitempty"`
}

// GetLendingRefPriceStatus calls lending_getRefPriceStatus.
func (c *Client) GetLendingRefPriceStatus(ctx context.Context) (*LendingRefPriceStatus, error) {
	var status LendingRefPriceStatus
	if err := c.call(ctx, "lending_getRefPriceStatus", nil, &status); err != nil {
		return nil, err
	}
	return &status, nil
}

// SubmitLendingRefPrice calls the auth-gated lending_submitRefPrice with an
// already-signed M-of-N bundle.
func (c *Client) SubmitLendingRefPrice(ctx context.Context, rateNum, rateDenom *big.Int, timestamp uint64, signatures [][]byte) (string, error) {
	if rateNum == nil || rateDenom == nil {
		return "", fmt.Errorf("rpcclient: rate required")
	}
	sigHexes := make([]string, len(signatures))
	for i, sig := range signatures {
		sigHexes[i] = "0x" + hex.EncodeToString(sig)
	}
	payload := map[string]any{
		"rateNum":    rateNum.String(),
		"rateDenom":  rateDenom.String(),
		"timestamp":  timestamp,
		"signatures": sigHexes,
	}
	var result struct {
		TxHash string `json:"txHash"`
	}
	if err := c.call(ctx, "lending_submitRefPrice", []any{payload}, &result); err != nil {
		return "", err
	}
	return result.TxHash, nil
}
