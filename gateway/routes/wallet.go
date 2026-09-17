package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
)

type walletRoutes struct {
	target  *url.URL
	client  *http.Client
	timeout time.Duration
	nextID  atomic.Int64
}

type walletRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      int64       `json:"id"`
}

type walletRPCError struct {
	Code    int         `json:"code"`
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

type walletRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result"`
	Error   *walletRPCError `json:"error"`
	status  int
}

type walletEscrowResult struct {
	ID     string `json:"id"`
	Payer  string `json:"payer"`
	Payee  string `json:"payee"`
	Status string `json:"status"`
	Token  string `json:"token"`
	Amount string `json:"amount"`
}

type walletIdentityResult struct {
	Alias   string `json:"alias"`
	AliasID string `json:"aliasId"`
}

type walletEscrowResponse struct {
	ID           string  `json:"id"`
	Status       string  `json:"status"`
	Payer        string  `json:"payer"`
	Payee        string  `json:"payee"`
	Token        string  `json:"token"`
	Amount       string  `json:"amount"`
	PayeeAlias   *string `json:"payeeAlias,omitempty"`
	PayeeAliasID *string `json:"payeeAliasId,omitempty"`
}

const (
	walletDefaultTimeout     = 10 * time.Second
	walletCodeEscrowNotFound = -32022
	walletCodeInvalidParams  = -32602
)

func newWalletRoutes(target *url.URL) (*walletRoutes, error) {
	if target == nil {
		return nil, fmt.Errorf("nil wallet target")
	}
	cloned := *target
	if strings.TrimSpace(cloned.Scheme) == "" {
		return nil, fmt.Errorf("wallet target scheme required")
	}
	if strings.TrimSpace(cloned.Host) == "" {
		return nil, fmt.Errorf("wallet target host required")
	}
	if strings.TrimSpace(cloned.Path) == "" {
		cloned.Path = "/"
	}
	return &walletRoutes{
		target:  &cloned,
		client:  &http.Client{Timeout: 15 * time.Second},
		timeout: walletDefaultTimeout,
	}, nil
}

func (wr *walletRoutes) mount(r chi.Router) {
	if wr == nil {
		return
	}
	r.Get("/wallet/escrows/{escrowID}", wr.getEscrow)
}

func (wr *walletRoutes) getEscrow(w http.ResponseWriter, r *http.Request) {
	if wr == nil || wr.target == nil {
		writeInternalError(w, errors.New("wallet routes not configured"))
		return
	}
	escrowID := strings.TrimSpace(chi.URLParam(r, "escrowID"))
	if escrowID == "" {
		writeBadRequest(w, errors.New("escrowID is required"))
		return
	}

	ctx, cancel := wr.context(r.Context())
	defer cancel()

	rpcResp, err := wr.callRPC(ctx, "escrow_get", []interface{}{map[string]string{"id": escrowID}}, r)
	if err != nil {
		writeInternalError(w, fmt.Errorf("escrow_get failed: %w", err))
		return
	}
	if rpcResp.Error != nil {
		if rpcResp.Error.Code == walletCodeEscrowNotFound || rpcResp.status == http.StatusNotFound {
			writeJSONError(w, http.StatusNotFound, errors.New("escrow not found"))
			return
		}
		writeInternalError(w, fmt.Errorf("escrow_get error: %s", rpcResp.Error.Message))
		return
	}

	var escrow walletEscrowResult
	if err := json.Unmarshal(rpcResp.Result, &escrow); err != nil {
		writeInternalError(w, fmt.Errorf("decode escrow response: %w", err))
		return
	}
	if strings.TrimSpace(escrow.Payer) == "" {
		writeInternalError(w, errors.New("escrow response missing payer"))
		return
	}

	var alias *walletIdentityResult
	aliasResp, err := wr.callRPC(ctx, "identity_reverse", []interface{}{strings.TrimSpace(escrow.Payee)}, r)
	if err == nil && aliasResp != nil {
		if aliasResp.Error == nil {
			var identity walletIdentityResult
			if err := json.Unmarshal(aliasResp.Result, &identity); err == nil && strings.TrimSpace(identity.Alias) != "" {
				alias = &identity
			} else if err != nil {
				writeInternalError(w, fmt.Errorf("decode identity response: %w", err))
				return
			}
		} else if aliasResp.Error.Code != walletCodeInvalidParams && aliasResp.status != http.StatusNotFound {
			writeInternalError(w, fmt.Errorf("identity_reverse error: %s", aliasResp.Error.Message))
			return
		}
	} else if err != nil {
		writeInternalError(w, fmt.Errorf("identity_reverse failed: %w", err))
		return
	}

	response := walletEscrowResponse{
		ID:     escrow.ID,
		Status: escrow.Status,
		Payer:  escrow.Payer,
		Payee:  escrow.Payee,
		Token:  escrow.Token,
		Amount: escrow.Amount,
	}
	if alias != nil {
		response.PayeeAlias = &alias.Alias
		response.PayeeAliasID = &alias.AliasID
	}

	payload, err := json.Marshal(response)
	if err != nil {
		writeInternalError(w, fmt.Errorf("marshal response: %w", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(payload)
}

func (wr *walletRoutes) callRPC(ctx context.Context, method string, params interface{}, r *http.Request) (*walletRPCResponse, error) {
	id := wr.nextID.Add(1)
	bodyStruct := walletRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      id,
	}
	payload, err := json.Marshal(bodyStruct)
	if err != nil {
		return nil, fmt.Errorf("encode rpc request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wr.target.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("build rpc request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if chain := strings.TrimSpace(r.Header.Get("X-Chain-Id")); chain != "" {
		req.Header.Set("X-Chain-Id", chain)
	}
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
	}

	resp, err := wr.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("perform rpc request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read rpc response: %w", err)
	}
	var rpcResp walletRPCResponse
	if err := json.Unmarshal(data, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode rpc response: %w", err)
	}
	rpcResp.status = resp.StatusCode
	return &rpcResp, nil
}

func (wr *walletRoutes) context(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := wr.timeout
	if timeout <= 0 {
		timeout = walletDefaultTimeout
	}
	return context.WithTimeout(parent, timeout)
}
