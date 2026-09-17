package rpcclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// Config controls how the Client connects to the lending engine RPC endpoint.
type Config struct {
	BaseURL            string
	BearerToken        string
	SharedSecretHeader string
	SharedSecretValue  string
	TLSClientCAFile    string
	AllowInsecure      bool
}

// Client implements the minimal subset of JSON-RPC 2.0 used by the lending engine adapter.
type Client struct {
	baseURL      string
	http         *http.Client
	bearer       string
	sharedHeader string
	sharedValue  string
}

// NewClient constructs a Client from the provided configuration.
func NewClient(cfg Config) (*Client, error) {
	tlsConfig := &tls.Config{}
	if cfg.AllowInsecure {
		tlsConfig.InsecureSkipVerify = true
	} else {
		systemPool, err := x509.SystemCertPool()
		if err != nil {
			return nil, fmt.Errorf("load system cert pool: %w", err)
		}
		if systemPool == nil {
			systemPool = x509.NewCertPool()
		}
		if strings.TrimSpace(cfg.TLSClientCAFile) != "" {
			pemBytes, err := os.ReadFile(cfg.TLSClientCAFile)
			if err != nil {
				return nil, fmt.Errorf("read client ca file: %w", err)
			}
			if ok := systemPool.AppendCertsFromPEM(pemBytes); !ok {
				return nil, fmt.Errorf("append client ca certificates: invalid pem data")
			}
		}
		tlsConfig.RootCAs = systemPool
	}

	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		return nil, fmt.Errorf("base url is required")
	}

	transport := &http.Transport{TLSClientConfig: tlsConfig}
	httpClient := &http.Client{Timeout: 10 * time.Second, Transport: transport}

	return &Client{
		baseURL:      baseURL,
		http:         httpClient,
		bearer:       strings.TrimSpace(cfg.BearerToken),
		sharedHeader: strings.TrimSpace(cfg.SharedSecretHeader),
		sharedValue:  strings.TrimSpace(cfg.SharedSecretValue),
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

// Call performs a JSON-RPC request to the configured endpoint.
func (c *Client) Call(ctx context.Context, method string, params any, result any) error {
	if c == nil {
		return fmt.Errorf("client is nil")
	}
	reqBody := rpcRequest{JSONRPC: "2.0", ID: 1, Method: method, Params: params}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(reqBody); err != nil {
		return fmt.Errorf("encode request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, &buf)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("X-Client", "lendingd")
	if c.bearer != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	if c.sharedHeader != "" && c.sharedValue != "" {
		httpReq.Header.Set(c.sharedHeader, c.sharedValue)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("call rpc: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("rpc call failed with status %s", resp.Status)
	}

	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if rpcResp.Error != nil {
		return rpcResp.Error
	}
	if result != nil && rpcResp.Result != nil {
		if err := json.Unmarshal(rpcResp.Result, result); err != nil {
			return fmt.Errorf("decode result: %w", err)
		}
	}
	return nil
}
