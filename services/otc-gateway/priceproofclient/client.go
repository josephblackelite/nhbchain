// Package priceproofclient implements the otc-gateway side of the
// otc-gateway <-> swapd wiring gap identified alongside gap 2b: an HTTP
// client for swapd's POST /v1/price-proof endpoint
// (services/swapd/server/priceproof_handlers.go), authenticated with the
// same partner HMAC scheme swaprpc.Client already uses against the chain
// RPC (nhbchain/gateway/auth).
package priceproofclient

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	gatewayauth "nhbchain/gateway/auth"
	swap "nhbchain/native/swap"
)

// Client fetches signed price proofs from swapd.
type Client struct {
	url        string
	httpClient *http.Client

	apiKey    string
	apiSecret string

	nowFn   func() time.Time
	nonceFn func() (string, error)
}

// Config configures a Client.
type Config struct {
	URL       string
	Timeout   time.Duration
	APIKey    string
	APISecret string
	Now       func() time.Time
	Nonce     func() (string, error)
}

// NewClient constructs a price-proof HTTP client targeting swapd's
// POST /v1/price-proof endpoint.
func NewClient(cfg Config) (*Client, error) {
	url := strings.TrimSpace(cfg.URL)
	if url == "" {
		return nil, fmt.Errorf("priceproofclient: URL is required")
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("priceproofclient: API key is required")
	}
	apiSecret := strings.TrimSpace(cfg.APISecret)
	if apiSecret == "" {
		return nil, fmt.Errorf("priceproofclient: API secret is required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	nonceFn := cfg.Nonce
	if nonceFn == nil {
		nonceFn = randomNonce
	}
	return &Client{
		url:        url,
		httpClient: &http.Client{Timeout: timeout},
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		nowFn:      nowFn,
		nonceFn:    nonceFn,
	}, nil
}

type priceProofResponse struct {
	Domain    string `json:"domain"`
	Provider  string `json:"provider"`
	Pair      string `json:"pair"`
	Rate      string `json:"rate"`
	Timestamp int64  `json:"timestamp"`
	Signature string `json:"signature"`
}

// PriceProof fetches a freshly-signed swap.PriceProof for the supplied
// "BASE/QUOTE" pair (e.g. "ZNHB/USD"). It satisfies
// nhbchain/services/otc-gateway/server.PriceProofSource.
func (c *Client) PriceProof(ctx context.Context, pair string) (*swap.PriceProof, error) {
	if c == nil || c.httpClient == nil {
		return nil, fmt.Errorf("priceproofclient: client not configured")
	}
	trimmedPair := strings.TrimSpace(pair)
	if trimmedPair == "" {
		return nil, fmt.Errorf("priceproofclient: pair required")
	}
	reqBody, err := json.Marshal(map[string]string{"pair": trimmedPair})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	timestamp := strconv.FormatInt(c.nowFn().UTC().Unix(), 10)
	nonce, err := c.nonceFn()
	if err != nil {
		return nil, fmt.Errorf("priceproofclient: generate nonce: %w", err)
	}
	signature := gatewayauth.ComputeSignature(c.apiSecret, timestamp, nonce, http.MethodPost, gatewayauth.CanonicalRequestPath(req), reqBody)
	req.Header.Set(gatewayauth.HeaderAPIKey, c.apiKey)
	req.Header.Set(gatewayauth.HeaderTimestamp, timestamp)
	req.Header.Set(gatewayauth.HeaderNonce, nonce)
	req.Header.Set(gatewayauth.HeaderSignature, hex.EncodeToString(signature))

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("priceproofclient: unexpected status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded priceProofResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("priceproofclient: decode response: %w", err)
	}
	sigHex := strings.TrimPrefix(strings.TrimSpace(decoded.Signature), "0x")
	sig, err := hex.DecodeString(sigHex)
	if err != nil {
		return nil, fmt.Errorf("priceproofclient: invalid signature encoding: %w", err)
	}
	proof, err := swap.NewPriceProof(decoded.Domain, decoded.Provider, decoded.Pair, decoded.Rate, decoded.Timestamp, sig)
	if err != nil {
		return nil, fmt.Errorf("priceproofclient: invalid price proof: %w", err)
	}
	return proof, nil
}

func randomNonce() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

var _ interface {
	PriceProof(context.Context, string) (*swap.PriceProof, error)
} = (*Client)(nil)
