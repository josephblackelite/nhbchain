package settlement

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// NowPaymentsConfig configures the HTTP payout client. Email/Password
// authenticate against NOWPayments' JWT login endpoint (required for the
// mass-payout API, distinct from the simple x-api-key auth used elsewhere
// in this codebase for invoices); APIKey is sent alongside the JWT on every
// payout call per NOWPayments' documented contract.
type NowPaymentsConfig struct {
	Email    string
	Password string
	APIKey   string
	BaseURL  string
}

// HTTPPayoutClient implements PayoutClient against the real NOWPayments
// mass-payout API.
//
// IMPORTANT: this integration is written to NOWPayments' documented payout
// API contract but has not been exercised against a live account -- no
// sandbox credentials were available while building it. Treat CreatePayout
// as "submits the batch as specified" rather than "proven correct against
// production," and verify the request/response shapes against a real
// account before relying on this for real settlement volume. NOWPayments
// payouts additionally require an operator to complete an email 2FA step on
// their dashboard before funds actually move -- CreatePayout only ever
// returns a submitted batch reference, never a settled confirmation; an
// operator must call Manager.ConfirmSettled once they've verified the
// payout cleared.
type HTTPPayoutClient struct {
	email    string
	password string
	apiKey   string
	baseURL  string
	http     *http.Client

	tokenMu     sync.Mutex
	token       string
	tokenExpiry time.Time
}

// NewHTTPPayoutClient constructs a NOWPayments payout client.
func NewHTTPPayoutClient(cfg NowPaymentsConfig) (*HTTPPayoutClient, error) {
	email := strings.TrimSpace(cfg.Email)
	password := strings.TrimSpace(cfg.Password)
	apiKey := strings.TrimSpace(cfg.APIKey)
	if email == "" || password == "" {
		return nil, fmt.Errorf("settlement: nowpayments email and password required")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("settlement: nowpayments api key required")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	if baseURL == "" {
		baseURL = "https://api.nowpayments.io/v1"
	}
	return &HTTPPayoutClient{
		email:    email,
		password: password,
		apiKey:   apiKey,
		baseURL:  baseURL,
		http:     &http.Client{Timeout: 15 * time.Second},
	}, nil
}

type nowPaymentsLoginResponse struct {
	Token string `json:"token"`
}

// authToken returns a cached JWT if it's still fresh, otherwise logs in
// again. NOWPayments JWTs are short-lived; a conservative 4-minute local TTL
// is used rather than parsing the token's own expiry claim, and CreatePayout
// falls back to a forced re-login on a 401 regardless of this cache.
func (c *HTTPPayoutClient) authToken(ctx context.Context, forceRefresh bool) (string, error) {
	c.tokenMu.Lock()
	defer c.tokenMu.Unlock()
	if !forceRefresh && c.token != "" && time.Now().Before(c.tokenExpiry) {
		return c.token, nil
	}
	payload, err := json.Marshal(map[string]string{"email": c.email, "password": c.password})
	if err != nil {
		return "", fmt.Errorf("settlement: encode nowpayments login: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/auth/login", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("settlement: nowpayments login request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("settlement: nowpayments login failed: status=%d", resp.StatusCode)
	}
	var loginResp nowPaymentsLoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return "", fmt.Errorf("settlement: decode nowpayments login response: %w", err)
	}
	if strings.TrimSpace(loginResp.Token) == "" {
		return "", fmt.Errorf("settlement: nowpayments login returned no token")
	}
	c.token = loginResp.Token
	c.tokenExpiry = time.Now().Add(4 * time.Minute)
	return c.token, nil
}

type nowPaymentsWithdrawal struct {
	Address  string  `json:"address"`
	Currency string  `json:"currency"`
	Amount   float64 `json:"amount"`
}

type nowPaymentsPayoutRequest struct {
	Withdrawals []nowPaymentsWithdrawal `json:"withdrawals"`
}

type nowPaymentsPayoutResponse struct {
	ID string `json:"id"`
}

// CreatePayout submits a single-withdrawal payout batch. It always returns
// a "submitted" outcome via ExternalRef (the batch ID) -- see the type-level
// doc comment for why this can never mean "settled" on its own.
func (c *HTTPPayoutClient) CreatePayout(ctx context.Context, req PayoutRequest) (PayoutResult, error) {
	if c == nil {
		return PayoutResult{}, fmt.Errorf("settlement: nowpayments client not configured")
	}
	address := strings.TrimSpace(req.Address)
	if address == "" {
		return PayoutResult{}, fmt.Errorf("settlement: payout address required")
	}
	if req.Amount <= 0 {
		return PayoutResult{}, fmt.Errorf("settlement: payout amount must be positive")
	}
	body := nowPaymentsPayoutRequest{
		Withdrawals: []nowPaymentsWithdrawal{{
			Address:  address,
			Currency: strings.ToLower(strings.TrimSpace(req.Asset)),
			Amount:   req.Amount,
		}},
	}
	resp, err := c.doPayoutRequest(ctx, body, false)
	if err != nil {
		return PayoutResult{}, err
	}
	if strings.TrimSpace(resp.ID) == "" {
		return PayoutResult{}, fmt.Errorf("settlement: nowpayments payout response missing batch id")
	}
	return PayoutResult{ExternalRef: resp.ID}, nil
}

func (c *HTTPPayoutClient) doPayoutRequest(ctx context.Context, body nowPaymentsPayoutRequest, isRetry bool) (*nowPaymentsPayoutResponse, error) {
	token, err := c.authToken(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("settlement: nowpayments auth: %w", err)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("settlement: encode nowpayments payout: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/payout", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("settlement: nowpayments payout request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && !isRetry {
		if _, refreshErr := c.authToken(ctx, true); refreshErr != nil {
			return nil, fmt.Errorf("settlement: nowpayments payout unauthorized, refresh failed: %w", refreshErr)
		}
		return c.doPayoutRequest(ctx, body, true)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("settlement: nowpayments payout failed: status=%d", resp.StatusCode)
	}
	var payoutResp nowPaymentsPayoutResponse
	if err := json.NewDecoder(resp.Body).Decode(&payoutResp); err != nil {
		return nil, fmt.Errorf("settlement: decode nowpayments payout response: %w", err)
	}
	return &payoutResp, nil
}
