package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// NowPaymentsClient defines the subset of the NOWPayments API the service requires.
type NowPaymentsClient interface {
	CreateInvoice(ctx context.Context, req *NowPaymentsInvoiceRequest) (*NowPaymentsInvoice, error)
	GetInvoice(ctx context.Context, id string) (*NowPaymentsInvoice, error)
	Estimate(ctx context.Context, req *NowPaymentsEstimateRequest) (*NowPaymentsEstimate, error)
	// CreatePayment creates a headless (deposit-address) payment via POST
	// /v1/payment -- unlike CreateInvoice, this returns a pay_address and
	// pay_amount for the caller to display directly, with no NOWPayments
	// checkout page involved.
	CreatePayment(ctx context.Context, req *NowPaymentsPaymentRequest) (*NowPaymentsPayment, error)
	// GetPayment re-fetches a headless payment's current status server-side
	// via GET /v1/payment/{id}, mirroring GetInvoice's role in the invoice
	// flow: the webhook handler uses this to confirm state rather than
	// trusting the webhook body's fields directly.
	GetPayment(ctx context.Context, id string) (*NowPaymentsPayment, error)
	// ListMerchantCoins returns the lowercased currency codes enabled for
	// this NOWPayments merchant account via GET /v1/merchant/coins.
	ListMerchantCoins(ctx context.Context) ([]string, error)
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
	// IsFeePaidByUser instructs NOWPayments to gross up the amount charged
	// to the payer so the merchant still receives the full PriceAmount
	// after NOWPayments' own processing fee is deducted. Without this, the
	// swapper's payment would be net of NOWPayments' fee and NHBCoin would
	// effectively absorb it by minting the full quoted amount anyway.
	IsFeePaidByUser bool   `json:"is_fee_paid_by_user"`
	SuccessURL      string `json:"success_url,omitempty"`
	CancelURL       string `json:"cancel_url,omitempty"`
	IpnCallbackURL  string `json:"ipn_callback_url,omitempty"`
}

// NowPaymentsInvoice captures the relevant invoice attributes used by the service.
type NowPaymentsInvoice struct {
	ID            string         `json:"id"`
	InvoiceID     string         `json:"invoice_id"`
	OrderID       string         `json:"order_id"`
	PriceAmount   flexibleAmount `json:"price_amount"`
	PayCurrency   string         `json:"pay_currency"`
	PriceCurrency string         `json:"price_currency"`
	PaymentStatus string         `json:"payment_status"`
	InvoiceURL    string         `json:"invoice_url"`
	CreatedAt     string         `json:"created_at"`
	UpdatedAt     string         `json:"updated_at"`
	Status        string         `json:"status"`
}

// NowPaymentsEstimateRequest represents a request to estimate a pay amount.
type NowPaymentsEstimateRequest struct {
	Amount       string `json:"amount"`
	CurrencyFrom string `json:"currency_from"`
	CurrencyTo   string `json:"currency_to"`
}

// NowPaymentsEstimate captures the invoice-side estimate for a selected pay currency.
type NowPaymentsEstimate struct {
	CurrencyFrom    string         `json:"currency_from"`
	CurrencyTo      string         `json:"currency_to"`
	EstimatedAmount flexibleAmount `json:"estimated_amount"`
	AmountFrom      flexibleAmount `json:"amount_from"`
	AmountTo        flexibleAmount `json:"amount_to"`
}

// flexibleAmount decodes a NOWPayments amount field regardless of whether
// their API returns it as a JSON string or a bare number -- confirmed live
// that /estimate returns amount_from as a number while this client
// originally only accepted a string, which made json.Decode fail the
// *entire* response (Go aborts the whole unmarshal on one field's type
// mismatch, even for fields nothing downstream reads). Every amount in
// this package is kept as decimal text end-to-end to avoid float64
// precision loss, so this preserves that: a numeric literal's raw JSON
// bytes are already valid decimal text, no float parsing needed.
type flexibleAmount string

func (a *flexibleAmount) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		*a = ""
		return nil
	}
	if len(trimmed) >= 2 && trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return fmt.Errorf("flexibleAmount: %w", err)
		}
		*a = flexibleAmount(s)
		return nil
	}
	*a = flexibleAmount(trimmed)
	return nil
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

// NowPaymentsPaymentRequest represents a headless (deposit-address) payment
// creation request against POST /v1/payment. Mirrors
// NowPaymentsInvoiceRequest's shape and fee/rate policy exactly: NOWPayments
// absorbs FX risk between quote and payment (is_fixed_rate) and the payer
// bears NOWPayments' own processing fee, not the NHBCoin treasury
// (is_fee_paid_by_user) -- confirmed by the project owner as sufficient
// slippage protection on its own, no extra buffer logic needed.
type NowPaymentsPaymentRequest struct {
	PriceAmount     string `json:"price_amount"`
	PriceCurrency   string `json:"price_currency"`
	PayCurrency     string `json:"pay_currency"`
	OrderID         string `json:"order_id"`
	OrderDesc       string `json:"order_description,omitempty"`
	FixedRate       bool   `json:"is_fixed_rate"`
	IsFeePaidByUser bool   `json:"is_fee_paid_by_user"`
	IpnCallbackURL  string `json:"ipn_callback_url,omitempty"`
}

// NowPaymentsPayment captures the relevant headless payment attributes.
// PaymentID/ID are typed as flexibleAmount, not string, for the same reason
// documented on flexibleAmount itself: NOWPayments' payment product
// commonly returns payment_id as a bare JSON number, and a plain string
// field would abort decoding the entire response the same way amount_from
// once did for /estimate.
type NowPaymentsPayment struct {
	PaymentID       flexibleAmount `json:"payment_id"`
	ID              flexibleAmount `json:"id"`
	OrderID         string         `json:"order_id"`
	PayAddress      string         `json:"pay_address"`
	PayAmount       flexibleAmount `json:"pay_amount"`
	PayCurrency     string         `json:"pay_currency"`
	PayinExtraID    string         `json:"payin_extra_id"`
	PriceAmount     flexibleAmount `json:"price_amount"`
	PriceCurrency   string         `json:"price_currency"`
	PaymentStatus   string         `json:"payment_status"`
	ActuallyPaid    flexibleAmount `json:"actually_paid"`
	OutcomeAmount   flexibleAmount `json:"outcome_amount"`
	OutcomeCurrency string         `json:"outcome_currency"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
}

// Finished returns whether the headless payment has reached NOWPayments'
// documented terminal-success status. Unlike NowPaymentsInvoice.Paid --
// which treats several loosely-related status strings as settled -- the
// payment product's own status vocabulary (waiting, confirming, confirmed,
// sending, partially_paid, finished, failed, expired, refunded) reserves
// "finished" specifically for "funds received and the full amount
// converted/forwarded"; "confirmed" is only an intermediate on-chain
// confirmation, not proof the full quoted amount actually arrived. Minting
// is only ever triggered on this narrower check.
func (p *NowPaymentsPayment) Finished() bool {
	return strings.EqualFold(strings.TrimSpace(p.PaymentStatus), "finished")
}

// NowPaymentsID returns the payment identifier NOWPayments uses to key its
// IPN callbacks and GET /v1/payment/{id} lookups, preferring payment_id and
// falling back to id.
func (p *NowPaymentsPayment) NowPaymentsID() string {
	return firstNonEmpty(string(p.PaymentID), string(p.ID))
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

func (c *HTTPNowPaymentsClient) CreatePayment(ctx context.Context, req *NowPaymentsPaymentRequest) (*NowPaymentsPayment, error) {
	return c.doPaymentRequest(ctx, http.MethodPost, "/payment", req)
}

func (c *HTTPNowPaymentsClient) GetPayment(ctx context.Context, id string) (*NowPaymentsPayment, error) {
	return c.doPaymentRequest(ctx, http.MethodGet, fmt.Sprintf("/payment/%s", id), nil)
}

func (c *HTTPNowPaymentsClient) ListMerchantCoins(ctx context.Context) ([]string, error) {
	if c == nil {
		return nil, fmt.Errorf("nowpayments client not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/merchant/coins", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-api-key", c.apiKey)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("nowpayments /merchant/coins failed: status=%d", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseMerchantCoins(raw)
}

// parseMerchantCoins tolerates the several response shapes NOWPayments'
// merchant/coins endpoint has been observed to return: a bare array, or an
// object wrapping the list under selectedCurrencies, currencies, or result.
func parseMerchantCoins(raw []byte) ([]string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return []string{}, nil
	}
	if trimmed[0] == '[' {
		var arr []string
		if err := json.Unmarshal(trimmed, &arr); err != nil {
			return nil, fmt.Errorf("nowpayments /merchant/coins: %w", err)
		}
		return arr, nil
	}
	var wrapped struct {
		SelectedCurrencies []string `json:"selectedCurrencies"`
		Currencies         []string `json:"currencies"`
		Result             []string `json:"result"`
	}
	if err := json.Unmarshal(trimmed, &wrapped); err != nil {
		return nil, fmt.Errorf("nowpayments /merchant/coins: unrecognized response shape: %w", err)
	}
	switch {
	case len(wrapped.SelectedCurrencies) > 0:
		return wrapped.SelectedCurrencies, nil
	case len(wrapped.Currencies) > 0:
		return wrapped.Currencies, nil
	case len(wrapped.Result) > 0:
		return wrapped.Result, nil
	}
	return []string{}, nil
}

func (c *HTTPNowPaymentsClient) doPaymentRequest(ctx context.Context, method, path string, payload interface{}) (*NowPaymentsPayment, error) {
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
	var payment NowPaymentsPayment
	if err := json.NewDecoder(resp.Body).Decode(&payment); err != nil {
		return nil, err
	}
	return &payment, nil
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
