package compat

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Mapping struct {
	Service string
	Path    string
	Method  string
}

type Service struct {
	Name    string
	BaseURL *url.URL
	Client  *http.Client
}

type Dispatcher struct {
	services map[string]*Service
	mappings map[string]Mapping
	guard    ScopeGuard
}

// maxBatchRequests bounds how many calls one /rpc request may carry. The
// gateway's rate limit counts requests, so an unbounded batch would be an
// unbounded number of upstream calls for one token.
const maxBatchRequests = 10

// codeInsufficientScope is the JSON-RPC error for a call whose service needs a
// scope that the caller's token does not hold.
const codeInsufficientScope = -32004

// ScopeGuard lets the dispatcher hold each call to the scopes the caller's
// token needs for the service that would answer it: the same scopes the
// service's own routes require, so /rpc is no way around them.
type ScopeGuard struct {
	// Required lists, by service name, the scopes a caller needs for that
	// service's methods. A service that is not listed asks for none.
	Required map[string][]string
	// FromContext returns the scopes of the authenticated caller of the request
	// and whether the request was authenticated at all. A request that was not
	// (authentication is off, or the path is one the operator left open) is not
	// held to scopes here: the authenticator in front of the dispatcher decided
	// to let it in.
	FromContext func(ctx context.Context) (scopes []string, authenticated bool)
}

// SetScopeGuard makes the dispatcher check scopes before it forwards a call.
func (d *Dispatcher) SetScopeGuard(guard ScopeGuard) {
	d.guard = guard
}

func (d *Dispatcher) scopesAllow(ctx context.Context, service string) bool {
	required := d.guard.Required[service]
	if len(required) == 0 || d.guard.FromContext == nil {
		return true
	}
	held, authenticated := d.guard.FromContext(ctx)
	if !authenticated {
		return true
	}
	have := make(map[string]struct{}, len(held))
	for _, scope := range held {
		have[scope] = struct{}{}
	}
	for _, scope := range required {
		if _, ok := have[scope]; !ok {
			return false
		}
	}
	return true
}

func NewDispatcher(services []*Service, mappings map[string]Mapping) *Dispatcher {
	svcMap := make(map[string]*Service, len(services))
	for _, svc := range services {
		if svc.Client == nil {
			svc.Client = &http.Client{Timeout: 15 * time.Second}
		}
		svcMap[svc.Name] = svc
	}
	return &Dispatcher{services: svcMap, mappings: mappings}
}

func (d *Dispatcher) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload json.RawMessage
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeError(w, nil, -32700, fmt.Sprintf("read body: %v", err))
			return
		}
		payload = body
		if len(bytes.TrimSpace(payload)) == 0 {
			writeError(w, nil, -32600, "empty request body")
			return
		}
		// The caller's own credential goes upstream with each call, as the
		// gateway's proxied routes do, so a service that checks it sees it.
		authorization := r.Header.Get("Authorization")
		if bytes.HasPrefix(bytes.TrimSpace(payload), []byte("[")) {
			var requests []rpcRequest
			if err := json.Unmarshal(payload, &requests); err != nil {
				writeError(w, nil, -32700, fmt.Sprintf("decode batch: %v", err))
				return
			}
			if len(requests) > maxBatchRequests {
				writeError(w, nil, -32600, fmt.Sprintf("batch of %d calls exceeds the limit of %d", len(requests), maxBatchRequests))
				return
			}
			responses := make([]rpcResponse, 0, len(requests))
			for _, req := range requests {
				responses = append(responses, d.handleSingle(r.Context(), req, authorization))
			}
			writeJSON(w, responses)
			return
		}
		var request rpcRequest
		if err := json.Unmarshal(payload, &request); err != nil {
			writeError(w, nil, -32700, fmt.Sprintf("decode request: %v", err))
			return
		}
		resp := d.handleSingle(r.Context(), request, authorization)
		writeJSON(w, resp)
	})
}

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	ID      any             `json:"id"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
	ID      any             `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (d *Dispatcher) handleSingle(ctx context.Context, req rpcRequest, authorization string) rpcResponse {
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	mapping, ok := d.mappings[req.Method]
	if !ok {
		resp.Error = &rpcError{Code: -32601, Message: "method not found"}
		return resp
	}
	service, ok := d.services[mapping.Service]
	if !ok {
		resp.Error = &rpcError{Code: -32001, Message: "service unavailable"}
		return resp
	}
	if !d.scopesAllow(ctx, mapping.Service) {
		resp.Error = &rpcError{Code: codeInsufficientScope, Message: "insufficient scope"}
		return resp
	}
	method := mapping.Method
	if method == "" {
		method = http.MethodPost
	}
	endpoint := singleJoiningSlash(service.BaseURL.String(), mapping.Path)
	payload := req.Params
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = []byte("{}")
	}
	var bodyReader io.Reader
	if method == http.MethodGet || method == http.MethodHead {
		bodyReader = nil
	} else {
		bodyReader = bytes.NewReader(payload)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, endpoint, bodyReader)
	if err != nil {
		resp.Error = &rpcError{Code: -32602, Message: fmt.Sprintf("build request: %v", err)}
		return resp
	}
	if bodyReader != nil {
		httpReq.Header.Set("Content-Type", "application/json")
	}
	if authorization != "" {
		httpReq.Header.Set("Authorization", authorization)
	}
	httpResp, err := service.Client.Do(httpReq)
	if err != nil {
		resp.Error = &rpcError{Code: -32002, Message: fmt.Sprintf("upstream error: %v", err)}
		return resp
	}
	defer httpResp.Body.Close()
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		resp.Error = &rpcError{Code: -32003, Message: fmt.Sprintf("read response: %v", err)}
		return resp
	}
	if httpResp.StatusCode >= 400 {
		resp.Error = &rpcError{Code: -32000, Message: "upstream error", Data: string(body)}
		return resp
	}
	if len(body) == 0 {
		resp.Result = json.RawMessage("null")
	} else {
		resp.Result = json.RawMessage(body)
	}
	return resp
}

func writeError(w http.ResponseWriter, id any, code int, msg string) {
	writeJSON(w, rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &rpcError{Code: code, Message: msg},
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	notice := DefaultNotice()
	warning := strings.ReplaceAll(notice.Warning, "\"", "'")
	if warning != "" {
		w.Header().Add("Warning", fmt.Sprintf("299 - \"%s\"", warning))
	}
	if notice.Link != "" {
		w.Header().Add("Link", fmt.Sprintf("<%s>; rel=\"deprecation\"; type=\"text/html\"", notice.Link))
	}
	if notice.Phase != "" {
		w.Header().Set("X-NHB-Compat-Phase", notice.Phase)
	}
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	_ = enc.Encode(v)
}

func singleJoiningSlash(a, b string) string {
	aslash := strings.HasSuffix(a, "/")
	bslash := strings.HasPrefix(b, "/")
	switch {
	case aslash && bslash:
		return a + b[1:]
	case !aslash && !bslash:
		return a + "/" + b
	}
	return a + b
}
