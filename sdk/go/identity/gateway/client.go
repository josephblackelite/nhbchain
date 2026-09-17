package gateway

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client wraps the identity gateway REST endpoints.
type Client struct {
	baseURL    *url.URL
	apiKey     string
	apiSecret  []byte
	httpClient *http.Client
	now        func() time.Time
}

// Option mutates the client configuration during construction.
type Option func(*Client)

// WithHTTPClient overrides the HTTP client used for requests.
func WithHTTPClient(httpClient *http.Client) Option {
	return func(c *Client) {
		if httpClient != nil {
			c.httpClient = httpClient
		}
	}
}

// WithClock overrides the time source used when signing requests. Primarily for tests.
func WithClock(now func() time.Time) Option {
	return func(c *Client) {
		if now != nil {
			c.now = now
		}
	}
}

// New constructs a client pointed at the supplied base URL.
func New(baseURL, apiKey, apiSecret string, opts ...Option) (*Client, error) {
	trimmedURL := strings.TrimSpace(baseURL)
	if trimmedURL == "" {
		return nil, fmt.Errorf("baseURL required")
	}
	parsed, err := url.Parse(trimmedURL)
	if err != nil {
		return nil, fmt.Errorf("invalid baseURL: %w", err)
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("api key required")
	}
	secret := strings.TrimSpace(apiSecret)
	if secret == "" {
		return nil, fmt.Errorf("api secret required")
	}
	client := &Client{
		baseURL:    parsed,
		apiKey:     key,
		apiSecret:  []byte(secret),
		httpClient: http.DefaultClient,
		now:        time.Now,
	}
	for _, opt := range opts {
		opt(client)
	}
	if client.httpClient == nil {
		client.httpClient = http.DefaultClient
	}
	if client.now == nil {
		client.now = time.Now
	}
	return client, nil
}

// RegisterEmailResponse mirrors the gateway payload returned by POST /identity/email/register.
type RegisterEmailResponse struct {
	Status    string `json:"status"`
	ExpiresIn int    `json:"expiresIn"`
}

// VerifyEmailResponse mirrors POST /identity/email/verify.
type VerifyEmailResponse struct {
	Status     string `json:"status"`
	VerifiedAt string `json:"verifiedAt"`
	EmailHash  string `json:"emailHash"`
}

// BindEmailResponse mirrors POST /identity/alias/bind-email.
type BindEmailResponse struct {
	Status       string `json:"status"`
	AliasID      string `json:"aliasId"`
	EmailHash    string `json:"emailHash"`
	PublicLookup bool   `json:"publicLookup"`
}

// RequestOption tweaks request metadata such as the Idempotency-Key header.
type RequestOption func(*requestOptions)

type requestOptions struct {
	idempotencyKey string
}

// WithIdempotencyKey sets the Idempotency-Key header for the request.
func WithIdempotencyKey(key string) RequestOption {
	return func(opts *requestOptions) {
		opts.idempotencyKey = strings.TrimSpace(key)
	}
}

// RegisterEmail initiates the verification flow for an email address.
func (c *Client) RegisterEmail(ctx context.Context, email, aliasHint string, opts ...RequestOption) (*RegisterEmailResponse, error) {
	payload := map[string]string{"email": email}
	if strings.TrimSpace(aliasHint) != "" {
		payload["aliasHint"] = aliasHint
	}
	var resp RegisterEmailResponse
	if err := c.post(ctx, "/identity/email/register", payload, &resp, opts...); err != nil {
		return nil, err
	}
	return &resp, nil
}

// VerifyEmail submits the verification code delivered out-of-band.
func (c *Client) VerifyEmail(ctx context.Context, email, code string, opts ...RequestOption) (*VerifyEmailResponse, error) {
	payload := map[string]string{"email": email, "code": code}
	var resp VerifyEmailResponse
	if err := c.post(ctx, "/identity/email/verify", payload, &resp, opts...); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BindEmail links a verified email to an alias identifier.
func (c *Client) BindEmail(ctx context.Context, aliasID, email string, consent bool, opts ...RequestOption) (*BindEmailResponse, error) {
	payload := map[string]any{
		"aliasId": aliasID,
		"email":   email,
		"consent": consent,
	}
	var resp BindEmailResponse
	if err := c.post(ctx, "/identity/alias/bind-email", payload, &resp, opts...); err != nil {
		return nil, err
	}
	return &resp, nil
}

func (c *Client) post(ctx context.Context, endpoint string, payload any, out any, opts ...RequestOption) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal payload: %w", err)
	}
	rel := &url.URL{Path: endpoint}
	url := c.baseURL.ResolveReference(rel)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url.String(), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	timestamp := strconv.FormatInt(c.now().Unix(), 10)
	signature := c.sign(http.MethodPost, rel.Path, body, timestamp)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", c.apiKey)
	req.Header.Set("X-API-Timestamp", timestamp)
	req.Header.Set("X-API-Signature", signature)
	var ro requestOptions
	for _, opt := range opts {
		opt(&ro)
	}
	if ro.idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", ro.idempotencyKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("identity gateway %d: %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(bodyBytes, out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func (c *Client) sign(method, path string, body []byte, timestamp string) string {
	sum := sha256.Sum256(body)
	message := fmt.Sprintf("%s\n%s\n%s\n%s", method, path, hex.EncodeToString(sum[:]), timestamp)
	mac := hmac.New(sha256.New, c.apiSecret)
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}
