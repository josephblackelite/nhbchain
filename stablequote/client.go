package stablequote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPClient satisfies Engine by calling out to a stable-quote-service
// process (see nhbchain-services' cmd/stable-quote-service) over HTTP,
// instead of running the real engine in-process -- see this package's
// header comment for why. Intended to run reachable only from localhost or
// a private network path (the service itself enforces this; this client
// has no special access of its own beyond a plain HTTP call).
type HTTPClient struct {
	baseURL string
	http    *http.Client
}

// NewHTTPClient builds a client against baseURL (e.g.
// "http://127.0.0.1:7091"), no trailing slash required.
func NewHTTPClient(baseURL string, timeout time.Duration) *HTTPClient {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &HTTPClient{baseURL: baseURL, http: &http.Client{Timeout: timeout}}
}

// errorCodeSentinels maps the stable JSON error "code" field (set by the
// service side, see nhbchain-services' http server) back to the exact
// sentinel error values in types.go, so errors.Is(...) keeps working for
// callers on this side of the network boundary exactly as it did when the
// engine was in-process.
var errorCodeSentinels = map[string]error{
	"not_supported":        ErrNotSupported,
	"quote_not_found":      ErrQuoteNotFound,
	"quote_expired":        ErrQuoteExpired,
	"reservation_not_found": ErrReservationNotFound,
	"price_unavailable":     ErrPriceUnavailable,
	"slippage_exceeded":     ErrSlippageExceeded,
	"insufficient_reserve":  ErrInsufficientReserve,
	"daily_cap_exceeded":    ErrDailyCapExceeded,
	"quote_amount_mismatch": ErrQuoteAmountMismatch,
	"reservation_expired":   ErrReservationExpired,
	"reservation_consumed":  ErrReservationConsumed,
}

type errorEnvelope struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (c *HTTPClient) do(ctx context.Context, method, path string, body, out interface{}) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("stablequote: encode request: %w", err)
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("stablequote: build request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("stablequote: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var envelope errorEnvelope
		if decodeErr := json.NewDecoder(resp.Body).Decode(&envelope); decodeErr == nil {
			if sentinel, ok := errorCodeSentinels[envelope.Code]; ok {
				return sentinel
			}
			if envelope.Message != "" {
				return errors.New(envelope.Message)
			}
		}
		return fmt.Errorf("stablequote: service returned status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("stablequote: decode response: %w", err)
	}
	return nil
}

func (c *HTTPClient) Price(ctx context.Context, req QuoteRequest) (QuoteResponse, error) {
	var resp QuoteResponse
	err := c.do(ctx, http.MethodPost, "/v1/price", req, &resp)
	return resp, err
}

func (c *HTTPClient) Reserve(ctx context.Context, req ReserveRequest) (ReserveResponse, error) {
	var resp ReserveResponse
	err := c.do(ctx, http.MethodPost, "/v1/reserve", req, &resp)
	return resp, err
}

func (c *HTTPClient) CashOut(ctx context.Context, req CashOutRequest) (CashOutResponse, error) {
	var resp CashOutResponse
	err := c.do(ctx, http.MethodPost, "/v1/cash-out", req, &resp)
	return resp, err
}

func (c *HTTPClient) Status(ctx context.Context) Status {
	var resp Status
	if err := c.do(ctx, http.MethodGet, "/v1/status", nil, &resp); err != nil {
		// Engine.Status has no error return (see the real engine's own
		// signature) -- a transport failure here just reads as "empty",
		// matching how a never-configured engine already reads today.
		return Status{}
	}
	return resp
}

type currentPriceResponse struct {
	Rate      float64   `json:"rate"`
	UpdatedAt time.Time `json:"updatedAt"`
	Stale     bool      `json:"stale"`
	OK        bool      `json:"ok"`
}

func (c *HTTPClient) CurrentPrice(base, quote string) (float64, time.Time, bool, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), c.http.Timeout)
	defer cancel()
	var resp currentPriceResponse
	path := fmt.Sprintf("/v1/current-price?base=%s&quote=%s", base, quote)
	if err := c.do(ctx, http.MethodGet, path, nil, &resp); err != nil {
		return 0, time.Time{}, false, false
	}
	return resp.Rate, resp.UpdatedAt, resp.Stale, resp.OK
}

var _ Engine = (*HTTPClient)(nil)
