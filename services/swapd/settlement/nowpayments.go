package settlement

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// NowPaymentsConfig configures the HTTP payout client. Email/Password
// authenticate against NOWPayments' JWT login endpoint (required for the
// mass-payout API, distinct from the simple x-api-key auth used elsewhere
// in this codebase for invoices); APIKey is sent alongside the JWT on every
// payout call per NOWPayments' documented contract.
//
// TOTPSecret is optional. When set, CreatePayout automatically generates the
// current RFC 6238 code and submits it to NOWPayments' verify-payout
// endpoint immediately after a batch is created, closing the account's 2FA
// gate without a human ever touching an email. When empty (the default, and
// what every existing caller of this package gets unless it explicitly opts
// in), CreatePayout behaves exactly as before: it returns as soon as the
// batch is created, leaving 2FA confirmation to a human or to
// Manager.ConfirmSettled being called with external evidence. This keeps any
// other consumer of this package (e.g. swapd) fully unaffected by default.
type NowPaymentsConfig struct {
	Email      string
	Password   string
	APIKey     string
	BaseURL    string
	TOTPSecret string
}

// HTTPPayoutClient implements PayoutClient against the real NOWPayments
// mass-payout API. Exercised against a live account starting 2026-08-24
// (real payout creation, real TOTP verification, real status polling).
//
// NOWPayments holds every payout batch unpaid until a 2FA step is completed
// (email code or, once TOTPSecret is configured, an automatically-generated
// TOTP code submitted in the same CreatePayout call -- see NowPaymentsConfig's
// doc comment). Even after verification, a payout still takes real time to
// actually move funds; CreatePayout returns as soon as the batch is created
// (and, if configured, verified) -- never a settled confirmation on its own.
// Reaching StatusSettled requires either an operator confirming completion
// via Manager.ConfirmSettled with real evidence, or automated polling via
// GetPayoutStatus against NOWPayments' own status endpoint.
type HTTPPayoutClient struct {
	email      string
	password   string
	apiKey     string
	baseURL    string
	totpSecret string
	http       *http.Client
	nowFn      func() time.Time

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
		email:      email,
		password:   password,
		apiKey:     apiKey,
		baseURL:    baseURL,
		totpSecret: strings.TrimSpace(cfg.TOTPSecret),
		http:       &http.Client{Timeout: 15 * time.Second},
		nowFn:      time.Now,
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
	// The real NOWPayments endpoint is POST /v1/auth (no "/login" segment) --
	// confirmed 2026-08-24 against NOWPayments' own documentation after this
	// code's first-ever live exercise returned a 404 on the previously
	// assumed "/auth/login" path. This code was written without sandbox
	// access (see this file's own doc comment) and had never actually been
	// run against a real account until then.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/auth", bytes.NewReader(payload))
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
	if c.totpSecret != "" {
		// NOWPayments holds a freshly created batch unpaid until this 2FA
		// step is completed -- without it, the batch auto-rejects after an
		// undocumented-but-observed window (roughly an hour per NOWPayments'
		// own support docs, though we've also seen a batch left unverified
		// for a few minutes get rejected during initial account setup).
		// Verifying immediately, in the same call, removes that window
		// entirely rather than shrinking it.
		code, err := generateTOTP(c.totpSecret, c.nowFn())
		if err != nil {
			return PayoutResult{}, fmt.Errorf("settlement: generate totp code for batch %s: %w", resp.ID, err)
		}
		if verifyErr := c.verifyPayout(ctx, resp.ID, code, false); verifyErr != nil {
			// verifyPayout can fail after NOWPayments already processed the
			// code server-side -- e.g. the response was lost to a timeout or
			// connection reset after NOWPayments received and accepted the
			// request. Reporting this as a hard failure would let a caller
			// (settlement.Manager.Initiate/RetryNowPayments) believe the
			// batch never verified and, if retried later, submit a genuinely
			// new payout for money NOWPayments is already moving. Before
			// giving up, check the batch's real status: anything past
			// NEW/CREATING means verification actually went through.
			status, statusErr := c.GetPayoutStatus(ctx, resp.ID)
			if statusErr != nil || status == "NEW" || status == "CREATING" {
				return PayoutResult{}, fmt.Errorf("settlement: verify batch %s: %w", resp.ID, verifyErr)
			}
			if status == "REJECTED" || status == "REJECTED_NOT_CHECKED" {
				return PayoutResult{}, fmt.Errorf("settlement: verify batch %s failed and nowpayments rejected it (status=%s): %w", resp.ID, status, verifyErr)
			}
			// WAITING/PROCESSING/FINISHED/anything else NOWPayments might
			// add later: the batch demonstrably progressed past requiring
			// verification, so treat this as success despite the transport
			// error on our own verify call.
		}
	}
	return PayoutResult{ExternalRef: resp.ID}, nil
}

type nowPaymentsVerifyRequest struct {
	VerificationCode string `json:"verification_code"`
}

// verifyPayout submits the batch's 2FA code, mirroring doPayoutRequest's
// single-retry-on-401 shape.
func (c *HTTPPayoutClient) verifyPayout(ctx context.Context, batchID, code string, isRetry bool) error {
	token, err := c.authToken(ctx, false)
	if err != nil {
		return fmt.Errorf("settlement: nowpayments auth: %w", err)
	}
	payload, err := json.Marshal(nowPaymentsVerifyRequest{VerificationCode: code})
	if err != nil {
		return fmt.Errorf("settlement: encode nowpayments verify payload: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/payout/"+batchID+"/verify", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("settlement: nowpayments verify request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized && !isRetry {
		if _, refreshErr := c.authToken(ctx, true); refreshErr != nil {
			return fmt.Errorf("settlement: nowpayments verify unauthorized, refresh failed: %w", refreshErr)
		}
		return c.verifyPayout(ctx, batchID, code, true)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("settlement: nowpayments verify failed: status=%d", resp.StatusCode)
	}
	return nil
}

type nowPaymentsWithdrawalStatus struct {
	BatchWithdrawalID string `json:"batch_withdrawal_id"`
	Status            string `json:"status"`
	Error             string `json:"error"`
}

type nowPaymentsPayoutStatusResponse struct {
	ID          string                        `json:"id"`
	Withdrawals []nowPaymentsWithdrawalStatus `json:"withdrawals"`
}

// GetPayoutStatus reads a batch's real, current status directly from
// NOWPayments -- the same information an operator would see on the
// dashboard, just polled programmatically. CreatePayout only ever submits a
// single-withdrawal batch, so the first (and only) entry in the response's
// withdrawals array is the one that matters. Known terminal/in-flight values
// per NOWPayments' own API docs: NEW, CREATING, WAITING, PROCESSING (in
// flight), FINISHED (terminal success, funds moved), REJECTED /
// REJECTED_NOT_CHECKED (terminal failure).
func (c *HTTPPayoutClient) GetPayoutStatus(ctx context.Context, batchID string) (string, error) {
	trimmed := strings.TrimSpace(batchID)
	if trimmed == "" {
		return "", fmt.Errorf("settlement: batch id required")
	}
	token, err := c.authToken(ctx, false)
	if err != nil {
		return "", fmt.Errorf("settlement: nowpayments auth: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/payout/"+trimmed, nil)
	if err != nil {
		return "", err
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("settlement: nowpayments payout status request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("settlement: nowpayments payout status failed: status=%d", resp.StatusCode)
	}
	var statusResp nowPaymentsPayoutStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&statusResp); err != nil {
		return "", fmt.Errorf("settlement: decode nowpayments payout status response: %w", err)
	}
	if len(statusResp.Withdrawals) == 0 {
		return "", fmt.Errorf("settlement: nowpayments payout status response has no withdrawals")
	}
	status := strings.ToUpper(strings.TrimSpace(statusResp.Withdrawals[0].Status))
	if status == "" {
		return "", fmt.Errorf("settlement: nowpayments payout status response missing status")
	}
	return status, nil
}

type nowPaymentsFeeResponse struct {
	Currency string  `json:"currency"`
	Fee      float64 `json:"fee"`
}

// GetPayoutFeeEstimate returns NOWPayments' quoted network fee for a payout
// of the given amount/currency, in the same units as the payout itself
// (e.g. USDT for a USDTTRC20 payout) -- confirmed live on 2026-08-24 to
// return the exact fee actually charged, and to be flat/amount-independent
// for USDTTRC20 (querying amounts from 1 to 5000 all returned the same
// value). Intended for showing a redeemer "you'll receive approximately X
// after fees" before they commit to a burn, not for anything money-moving
// itself.
func (c *HTTPPayoutClient) GetPayoutFeeEstimate(ctx context.Context, currency string, amount float64) (float64, error) {
	trimmedCurrency := strings.ToLower(strings.TrimSpace(currency))
	if trimmedCurrency == "" {
		return 0, fmt.Errorf("settlement: currency required")
	}
	if amount <= 0 {
		return 0, fmt.Errorf("settlement: amount must be positive")
	}
	token, err := c.authToken(ctx, false)
	if err != nil {
		return 0, fmt.Errorf("settlement: nowpayments auth: %w", err)
	}
	query := url.Values{}
	query.Set("currency", trimmedCurrency)
	query.Set("amount", strconv.FormatFloat(amount, 'f', -1, 64))
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/payout/fee?"+query.Encode(), nil)
	if err != nil {
		return 0, err
	}
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(httpReq)
	if err != nil {
		return 0, fmt.Errorf("settlement: nowpayments payout fee request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return 0, fmt.Errorf("settlement: nowpayments payout fee failed: status=%d", resp.StatusCode)
	}
	var feeResp nowPaymentsFeeResponse
	if err := json.NewDecoder(resp.Body).Decode(&feeResp); err != nil {
		return 0, fmt.Errorf("settlement: decode nowpayments payout fee response: %w", err)
	}
	if feeResp.Fee < 0 {
		return 0, fmt.Errorf("settlement: nowpayments payout fee response has a negative fee: %v", feeResp.Fee)
	}
	return feeResp.Fee, nil
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
