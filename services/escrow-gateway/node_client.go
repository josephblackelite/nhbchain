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
	EscrowRelease(ctx context.Context, escrowID, caller string) error
	EscrowRefund(ctx context.Context, escrowID, caller string) error
	EscrowDispute(ctx context.Context, escrowID, caller string) error
	EscrowResolve(ctx context.Context, escrowID, caller, outcome string) error
	P2PCreateTrade(ctx context.Context, req P2PAcceptRequest) (*P2PAcceptResponse, error)
	P2PGetTrade(ctx context.Context, tradeID string) (*P2PTradeState, error)
	FetchEvents(ctx context.Context, afterSeq int64, limit int) ([]NodeEvent, error)
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
	if trimmed := strings.TrimSpace(req.Realm); trimmed != "" {
		payload["realm"] = trimmed
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

func (c *RPCNodeClient) EscrowRelease(ctx context.Context, escrowID, caller string) error {
	params := map[string]string{"id": escrowID, "caller": caller}
	return c.call(ctx, "escrow_release", []interface{}{params}, nil)
}

func (c *RPCNodeClient) EscrowRefund(ctx context.Context, escrowID, caller string) error {
	params := map[string]string{"id": escrowID, "caller": caller}
	return c.call(ctx, "escrow_refund", []interface{}{params}, nil)
}

func (c *RPCNodeClient) EscrowDispute(ctx context.Context, escrowID, caller string) error {
	params := map[string]string{"id": escrowID, "caller": caller}
	return c.call(ctx, "escrow_dispute", []interface{}{params}, nil)
}

func (c *RPCNodeClient) EscrowResolve(ctx context.Context, escrowID, caller, outcome string) error {
	params := map[string]string{"id": escrowID, "caller": caller, "outcome": outcome}
	return c.call(ctx, "escrow_resolve", []interface{}{params}, nil)
}

func (c *RPCNodeClient) P2PCreateTrade(ctx context.Context, req P2PAcceptRequest) (*P2PAcceptResponse, error) {
	payload := map[string]interface{}{
		"offerId":     req.OfferID,
		"buyer":       req.Buyer,
		"seller":      req.Seller,
		"baseToken":   req.BaseToken,
		"baseAmount":  req.BaseAmount,
		"quoteToken":  req.QuoteToken,
		"quoteAmount": req.QuoteAmount,
		"deadline":    req.Deadline,
	}
	var result P2PAcceptResponse
	if err := c.call(ctx, "p2p_createTrade", []interface{}{payload}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *RPCNodeClient) P2PGetTrade(ctx context.Context, tradeID string) (*P2PTradeState, error) {
	var result P2PTradeState
	if err := c.call(ctx, "p2p_getTrade", []interface{}{map[string]string{"tradeId": tradeID}}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *RPCNodeClient) FetchEvents(ctx context.Context, afterSeq int64, limit int) ([]NodeEvent, error) {
	params := map[string]interface{}{
		"after": afterSeq,
	}
	if limit > 0 {
		params["limit"] = limit
	}
	var result []NodeEvent
	if err := c.call(ctx, "events_since", []interface{}{params}, &result); err != nil {
		return nil, err
	}
	return result, nil
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
	Realm    string `json:"realm,omitempty"`
	Meta     string `json:"meta,omitempty"`
}

// EscrowCreateResponse mirrors the node RPC result.
type EscrowCreateResponse struct {
	ID string `json:"id"`
}

// EscrowState mirrors the JSON returned by the node for escrow_get.
type EscrowState struct {
	ID        string   `json:"id"`
	Payer     string   `json:"payer"`
	Payee     string   `json:"payee"`
	Mediator  *string  `json:"mediator,omitempty"`
	Token     string   `json:"token"`
	Amount    string   `json:"amount"`
	FeeBps    uint32   `json:"feeBps"`
	Deadline  int64    `json:"deadline"`
	CreatedAt int64    `json:"createdAt"`
	Status    string   `json:"status"`
	Meta      string   `json:"meta"`
	Realm     *string  `json:"realm,omitempty"`
	FrozenAt  *int64   `json:"frozenAt,omitempty"`
	Scheme    *uint8   `json:"arbScheme,omitempty"`
	Threshold *uint32  `json:"arbThreshold,omitempty"`
	Nonce     *uint64  `json:"policyNonce,omitempty"`
	Version   *uint64  `json:"realmVersion,omitempty"`
	Members   []string `json:"arbitrators,omitempty"`
}

// P2PAcceptRequest captures the gateway request forwarded to the node RPC when
// creating a dual-escrow trade.
type P2PAcceptRequest struct {
	OfferID     string `json:"offerId"`
	Buyer       string `json:"buyer"`
	Seller      string `json:"seller"`
	BaseToken   string `json:"baseToken"`
	BaseAmount  string `json:"baseAmount"`
	QuoteToken  string `json:"quoteToken"`
	QuoteAmount string `json:"quoteAmount"`
	Deadline    int64  `json:"deadline"`
}

// P2PAcceptResponse mirrors the node RPC response for trade creation.
type P2PAcceptResponse struct {
	TradeID       string                         `json:"tradeId"`
	EscrowBaseID  string                         `json:"escrowBaseId"`
	EscrowQuoteID string                         `json:"escrowQuoteId"`
	PayIntents    map[string]P2PPayIntentPayload `json:"payIntents"`
}

// P2PPayIntentPayload mirrors a pay intent object returned by the node.
type P2PPayIntentPayload struct {
	To     string `json:"to"`
	Token  string `json:"token"`
	Amount string `json:"amount"`
	Memo   string `json:"memo"`
}

// P2PTradeState mirrors the node RPC response for fetching trade details.
type P2PTradeState struct {
	ID          string `json:"id"`
	OfferID     string `json:"offerId"`
	Buyer       string `json:"buyer"`
	Seller      string `json:"seller"`
	QuoteToken  string `json:"quoteToken"`
	QuoteAmount string `json:"quoteAmount"`
	EscrowQuote string `json:"escrowQuoteId"`
	BaseToken   string `json:"baseToken"`
	BaseAmount  string `json:"baseAmount"`
	EscrowBase  string `json:"escrowBaseId"`
	Deadline    int64  `json:"deadline"`
	CreatedAt   int64  `json:"createdAt"`
	Status      string `json:"status"`
}

// NodeEvent represents an emitted escrow or trade event returned by the node.
type NodeEvent struct {
	Sequence   int64             `json:"sequence"`
	Type       string            `json:"type"`
	Attributes map[string]string `json:"attributes"`
	Height     uint64            `json:"height"`
	TxHash     string            `json:"txHash"`
	Timestamp  int64             `json:"timestamp"`
}
