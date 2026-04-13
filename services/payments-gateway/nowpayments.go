package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// NowPaymentsClient defines the subset of the NOWPayments API the service requires.
type NowPaymentsClient interface {
	CreateInvoice(ctx context.Context, req *NowPaymentsInvoiceRequest) (*NowPaymentsInvoice, error)
	GetInvoice(ctx context.Context, id string) (*NowPaymentsInvoice, error)
	Estimate(ctx context.Context, req *NowPaymentsEstimateRequest) (*NowPaymentsEstimate, error)
}

// HTTPNowPaymentsClient implements NowPaymentsClient against the official HTTP API.
type HTTPNowPaymentsClient struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// NowPaymentsInvoiceRequest represents an invoice creation request.
type NowPaymentsInvoiceRequest struct {
	PriceAmount   string `json:"price_amount"`
	PriceCurrency string `json:"price_currency"`
	PayCurrency   string `json:"pay_currency"`
	OrderID       string `json:"order_id"`
	OrderDesc     string `json:"order_description,omitempty"`
	FixedRate     bool   `json:"is_fixed_rate"`
	SuccessURL    string `json:"success_url,omitempty"`
	CancelURL     string `json:"cancel_url,omitempty"`
}

// NowPaymentsInvoice captures the relevant invoice attributes used by the service.
type NowPaymentsInvoice struct {
	ID            string `json:"id"`
	InvoiceID     string `json:"invoice_id"`
	OrderID       string `json:"order_id"`
	PriceAmount   string `json:"price_amount"`
	PayCurrency   string `json:"pay_currency"`
	PriceCurrency string `json:"price_currency"`
	PaymentStatus string `json:"payment_status"`
	InvoiceURL    string `json:"invoice_url"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
	Status        string `json:"status"`
}

// NowPaymentsEstimateRequest represents a request to estimate a pay amount.
type NowPaymentsEstimateRequest struct {
	Amount       string `json:"amount"`
	CurrencyFrom string `json:"currency_from"`
	CurrencyTo   string `json:"currency_to"`
}

// NowPaymentsEstimate captures the invoice-side estimate for a selected pay currency.
type NowPaymentsEstimate struct {
	CurrencyFrom    string `json:"currency_from"`
	CurrencyTo      string `json:"currency_to"`
	EstimatedAmount string `json:"estimated_amount"`
	AmountFrom      string `json:"amount_from"`
	AmountTo        string `json:"amount_to"`
}

// Paid returns whether the invoice is considered settled.
func (i *NowPaymentsInvoice) Paid() bool {
	status := strings.ToLower(strings.TrimSpace(i.PaymentStatus))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(i.Status))
	}
	switch status {
	case "finished", "confirmed", "completed", "paid":
		return true
	}
	return false
}

// NewHTTPNowPaymentsClient constructs an HTTP client with sane defaults.
func NewHTTPNowPaymentsClient(baseURL, apiKey string) *HTTPNowPaymentsClient {
	return &HTTPNowPaymentsClient{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *HTTPNowPaymentsClient) CreateInvoice(ctx context.Context, req *NowPaymentsInvoiceRequest) (*NowPaymentsInvoice, error) {
	return c.doRequest(ctx, http.MethodPost, "/invoice", req)
}

func (c *HTTPNowPaymentsClient) GetInvoice(ctx context.Context, id string) (*NowPaymentsInvoice, error) {
	return c.doRequest(ctx, http.MethodGet, fmt.Sprintf("/invoice/%s", id), nil)
}

func (c *HTTPNowPaymentsClient) Estimate(ctx context.Context, req *NowPaymentsEstimateRequest) (*NowPaymentsEstimate, error) {
	if c == nil {
		return nil, fmt.Errorf("nowpayments client not configured")
	}
	if req == nil {
		return nil, fmt.Errorf("estimate request required")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/estimate", nil)
	if err != nil {
		return nil, err
	}
	query := httpReq.URL.Query()
	query.Set("amount", strings.TrimSpace(req.Amount))
	query.Set("currency_from", strings.ToUpper(strings.TrimSpace(req.CurrencyFrom)))
	query.Set("currency_to", strings.ToUpper(strings.TrimSpace(req.CurrencyTo)))
	httpReq.URL.RawQuery = query.Encode()
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("nowpayments /estimate failed: status=%d", resp.StatusCode)
	}
	var estimate NowPaymentsEstimate
	if err := json.NewDecoder(resp.Body).Decode(&estimate); err != nil {
		return nil, err
	}
	return &estimate, nil
}

func (c *HTTPNowPaymentsClient) doRequest(ctx context.Context, method, path string, payload interface{}) (*NowPaymentsInvoice, error) {
	if c == nil {
		return nil, fmt.Errorf("nowpayments client not configured")
	}
	var body *bytes.Reader
	if payload != nil {
		buf, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(buf)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("nowpayments %s failed: status=%d", path, resp.StatusCode)
	}
	var invoice NowPaymentsInvoice
	if err := json.NewDecoder(resp.Body).Decode(&invoice); err != nil {
		return nil, err
	}
	return &invoice, nil
}
