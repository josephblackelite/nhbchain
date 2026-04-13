package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"nhbchain/core/types"
)

const transactionsRequestLimit = 1 << 20 // 1 MiB

// transactionsRoutes proxies nhb_sendTransaction requests to the consensus RPC
// while validating supported transaction types.
type transactionsRoutes struct {
	target  *url.URL
	client  *http.Client
	timeout time.Duration
}

type sendTransactionRequest struct {
	JSONRPC string            `json:"jsonrpc,omitempty"`
	ID      json.RawMessage   `json:"id,omitempty"`
	Method  string            `json:"method,omitempty"`
	Params  []json.RawMessage `json:"params"`
}

func newTransactionsRoutes(target *url.URL) (*transactionsRoutes, error) {
	if target == nil {
		return nil, fmt.Errorf("nil transactions target")
	}
	cloned := *target
	if strings.TrimSpace(cloned.Scheme) == "" {
		return nil, fmt.Errorf("transactions target scheme required")
	}
	if strings.TrimSpace(cloned.Host) == "" {
		return nil, fmt.Errorf("transactions target host required")
	}
	if strings.TrimSpace(cloned.Path) == "" {
		cloned.Path = "/"
	}
	return &transactionsRoutes{
		target:  &cloned,
		client:  &http.Client{Timeout: 15 * time.Second},
		timeout: 10 * time.Second,
	}, nil
}

func (tr *transactionsRoutes) mount(r chi.Router) {
	r.Post("/send", tr.send)
}

func (tr *transactionsRoutes) send(w http.ResponseWriter, r *http.Request) {
	if tr == nil || tr.target == nil {
		writeInternalError(w, errors.New("transactions route misconfigured"))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, transactionsRequestLimit))
	if err != nil {
		writeBadRequest(w, fmt.Errorf("read request body: %w", err))
		return
	}
	if len(bytes.TrimSpace(body)) == 0 {
		writeBadRequest(w, errors.New("request body is empty"))
		return
	}

	var req sendTransactionRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeBadRequest(w, fmt.Errorf("decode request: %w", err))
		return
	}
	if strings.TrimSpace(req.Method) == "" {
		req.Method = "nhb_sendTransaction"
	}
	if req.Method != "nhb_sendTransaction" {
		writeBadRequest(w, fmt.Errorf("unsupported method %q", req.Method))
		return
	}
	if req.JSONRPC == "" {
		req.JSONRPC = "2.0"
	}
	if len(req.Params) == 0 {
		writeBadRequest(w, errors.New("transaction parameter required"))
		return
	}

	var tx types.Transaction
	if err := json.Unmarshal(req.Params[0], &tx); err != nil {
		writeBadRequest(w, fmt.Errorf("decode transaction: %w", err))
		return
	}
	switch tx.Type {
	case types.TxTypeTransfer, types.TxTypeTransferZNHB:
	default:
		writeBadRequest(w, fmt.Errorf("unsupported transaction type 0x%x", byte(tx.Type)))
		return
	}

	forwardBody, err := json.Marshal(req)
	if err != nil {
		writeInternalError(w, fmt.Errorf("encode upstream request: %w", err))
		return
	}

	ctx, cancel := tr.context(r.Context())
	defer cancel()

	forwardReq, err := http.NewRequestWithContext(ctx, http.MethodPost, tr.target.String(), bytes.NewReader(forwardBody))
	if err != nil {
		writeInternalError(w, fmt.Errorf("build upstream request: %w", err))
		return
	}
	forwardReq.Header.Set("Content-Type", "application/json")
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); auth != "" {
		forwardReq.Header.Set("Authorization", auth)
	}
	if chainID := strings.TrimSpace(r.Header.Get("X-Chain-Id")); chainID != "" {
		forwardReq.Header.Set("X-Chain-Id", chainID)
	}
	forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if remote := clientIP(r.RemoteAddr); remote != "" {
		if forwarded != "" {
			forwarded = fmt.Sprintf("%s, %s", forwarded, remote)
		} else {
			forwarded = remote
		}
	}
	if forwarded != "" {
		forwardReq.Header.Set("X-Forwarded-For", forwarded)
	}

	resp, err := tr.client.Do(forwardReq)
	if err != nil {
		writeInternalError(w, fmt.Errorf("forward request: %w", err))
		return
	}
	defer resp.Body.Close()

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (tr *transactionsRoutes) context(parent context.Context) (context.Context, context.CancelFunc) {
	timeout := tr.timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return context.WithTimeout(parent, timeout)
}

func copyHeaders(dst, src http.Header) {
	for key, values := range src {
		// Skip Content-Length to allow Go's http server to set it automatically.
		if strings.EqualFold(key, "Content-Length") {
			continue
		}
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func clientIP(addr string) string {
	host := strings.TrimSpace(addr)
	if host == "" {
		return ""
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	return strings.TrimSpace(host)
}
