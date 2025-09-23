package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

// NodeClient is a thin JSON-RPC client used by the gateway.
type NodeClient interface {
	EscrowCreate(ctx context.Context, req EscrowCreateRequest) (*EscrowCreateResponse, error)
	EscrowGet(ctx context.Context, id string) (*EscrowState, error)
}

// RPCNodeClient implements NodeClient against the nhb JSON-RPC server.
type RPCNodeClient struct {
	baseURL   string
	authToken string
	http      *http.Client
	nextID    atomic.Int64
}

func NewRPCNodeClient(baseURL, authToken string) *RPCNodeClient {
	return &RPCNodeClient{
		baseURL:   baseURL,
		authToken: authToken,
		http: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      int64       `json:"id"`
}

type jsonRPCResponse struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      int64            `json:"id"`
	Result  json.RawMessage  `json:"result"`
	Error   *jsonRPCErrorObj `json:"error"`
}

type jsonRPCErrorObj struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func (c *RPCNodeClient) EscrowCreate(ctx context.Context, req EscrowCreateRequest) (*EscrowCreateResponse, error) {
	payload := map[string]interface{}{
		"payer":    req.Payer,
		"payee":    req.Payee,
		"token":    req.Token,
		"amount":   req.Amount,
		"feeBps":   req.FeeBps,
		"deadline": req.Deadline,
	}
	if req.Mediator != "" {
		payload["mediator"] = req.Mediator
	}
	if req.Meta != "" {
		payload["meta"] = req.Meta
	}
	var result EscrowCreateResponse
	if err := c.call(ctx, "escrow_create", []interface{}{payload}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *RPCNodeClient) EscrowGet(ctx context.Context, id string) (*EscrowState, error) {
	var result EscrowState
	err := c.call(ctx, "escrow_get", []interface{}{map[string]string{"id": id}}, &result)
	if err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *RPCNodeClient) call(ctx context.Context, method string, params interface{}, out interface{}) error {
	id := c.nextID.Add(1)
	bodyStruct := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      id,
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
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("node rpc %s failed: status=%d body=%s", method, resp.StatusCode, string(body))
	}
	var rpcResp jsonRPCResponse
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
		return errors.New("node rpc returned empty result")
	}
	return json.Unmarshal(rpcResp.Result, out)
}

// EscrowCreateRequest is the request payload accepted by the gateway.
type EscrowCreateRequest struct {
	Payer    string `json:"payer"`
	Payee    string `json:"payee"`
	Token    string `json:"token"`
	Amount   string `json:"amount"`
	FeeBps   uint32 `json:"feeBps"`
	Deadline int64  `json:"deadline"`
	Mediator string `json:"mediator,omitempty"`
	Meta     string `json:"meta,omitempty"`
}

// EscrowCreateResponse mirrors the node RPC result.
type EscrowCreateResponse struct {
	ID string `json:"id"`
}

// EscrowState mirrors the JSON returned by the node for escrow_get.
type EscrowState struct {
	ID        string  `json:"id"`
	Payer     string  `json:"payer"`
	Payee     string  `json:"payee"`
	Mediator  *string `json:"mediator,omitempty"`
	Token     string  `json:"token"`
	Amount    string  `json:"amount"`
	FeeBps    uint32  `json:"feeBps"`
	Deadline  int64   `json:"deadline"`
	CreatedAt int64   `json:"createdAt"`
	Status    string  `json:"status"`
	Meta      string  `json:"meta"`
}
