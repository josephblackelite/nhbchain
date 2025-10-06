package swaprpc

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"nhbchain/core"
	gatewayauth "nhbchain/gateway/auth"
)

// Client provides a thin JSON-RPC wrapper for swap voucher submission.
type Client struct {
	url        string
	provider   string
	httpClient *http.Client
	nextID     atomic.Int64

	apiKey    string
	apiSecret string
	allowed   map[string]struct{}

	rateMu     sync.Mutex
	rateLimit  int
	rateWindow time.Duration
	rateStart  time.Time
	rateCount  int

	nowFn   func() time.Time
	nonceFn func() (string, error)
}

// Config represents the client configuration.
type Config struct {
	URL               string
	Provider          string
	Timeout           time.Duration
	APIKey            string
	APISecret         string
	AllowedMethods    []string
	RequestsPerMinute int
	RateWindow        time.Duration
	Now               func() time.Time
	Nonce             func() (string, error)
}

// NewClient constructs a JSON-RPC client targeting the supplied URL.
func NewClient(cfg Config) (*Client, error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, fmt.Errorf("swaprpc: API key is required")
	}
	apiSecret := strings.TrimSpace(cfg.APISecret)
	if apiSecret == "" {
		return nil, fmt.Errorf("swaprpc: API secret is required")
	}
	allowed := make(map[string]struct{})
	for _, method := range cfg.AllowedMethods {
		trimmed := strings.TrimSpace(method)
		if trimmed == "" {
			continue
		}
		allowed[trimmed] = struct{}{}
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("swaprpc: allowed method list cannot be empty")
	}
	window := cfg.RateWindow
	if window <= 0 {
		window = time.Minute
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
		url:      strings.TrimSpace(cfg.URL),
		provider: strings.TrimSpace(cfg.Provider),
		httpClient: &http.Client{
			Timeout: timeout,
		},
		apiKey:     apiKey,
		apiSecret:  apiSecret,
		allowed:    allowed,
		rateLimit:  cfg.RequestsPerMinute,
		rateWindow: window,
		nowFn:      nowFn,
		nonceFn:    nonceFn,
	}
}

// VoucherStatus reflects the current state of a voucher recorded on the node.
type VoucherStatus struct {
	Status string
	TxHash string
}

// MintCompliance bundles the partner identity metadata that accompanies voucher submissions.
type MintCompliance struct {
	PartnerDID       string          `json:"partnerDid,omitempty"`
	ComplianceTags   []string        `json:"complianceTags,omitempty"`
	TravelRulePacket json.RawMessage `json:"travelRulePacket,omitempty"`
	SanctionsStatus  string          `json:"sanctionsStatus,omitempty"`
}

// MintSubmission wraps the voucher payload, signature, and compliance metadata.
type MintSubmission struct {
	Voucher      core.MintVoucher `json:"voucher"`
	SignatureHex string           `json:"sig"`
	Provider     string           `json:"provider"`
	ProviderTxID string           `json:"providerTxId"`
	Compliance   *MintCompliance  `json:"compliance,omitempty"`
}

// VoucherExportRecord captures the decoded row returned by swap_voucher_export.
type VoucherExportRecord struct {
	ProviderTxID      string
	Provider          string
	FiatCurrency      string
	FiatAmount        string
	USD               string
	Rate              string
	Token             string
	MintAmountWei     string
	RecipientHex      string
	Username          string
	Address           string
	QuoteTimestamp    int64
	Source            string
	OracleMedian      string
	OracleFeeders     []string
	PriceProofID      string
	MinterSignature   string
	Status            string
	CreatedAt         int64
	TwapRate          string
	TwapObservations  int
	TwapWindowSeconds int64
	TwapStart         int64
	TwapEnd           int64
}

// SubmitMintVoucher posts a mint voucher with signature to the swap gateway RPC.
func (c *Client) SubmitMintVoucher(ctx context.Context, submission MintSubmission) (string, bool, error) {
	payload := submission
	payload.Provider = c.provider
	var result struct {
		TxHash string `json:"txHash"`
		Minted bool   `json:"minted"`
	}
	if err := c.call(ctx, "swap_submitVoucher", []interface{}{payload}, &result); err != nil {
		return "", false, err
	}
	return strings.TrimSpace(result.TxHash), result.Minted, nil
}

// GetVoucher retrieves the on-chain voucher record for the supplied provider identifier.
func (c *Client) GetVoucher(ctx context.Context, providerTxID string) (*VoucherStatus, error) {
	var result map[string]interface{}
	if err := c.call(ctx, "swap_voucher_get", []interface{}{providerTxID}, &result); err != nil {
		return nil, err
	}
	status := ""
	if v, ok := result["status"].(string); ok {
		status = strings.TrimSpace(v)
	}
	txHash := ""
	if v, ok := result["txHash"].(string); ok {
		txHash = strings.TrimSpace(v)
	}
	return &VoucherStatus{Status: status, TxHash: txHash}, nil
}

// ExportVouchers retrieves the swap voucher export rows for the provided window.
func (c *Client) ExportVouchers(ctx context.Context, start, end time.Time) ([]VoucherExportRecord, error) {
	if c == nil {
		return nil, fmt.Errorf("swaprpc: client not configured")
	}
	params := []interface{}{start.Unix(), end.Unix()}
	var payload struct {
		CSVBase64 string `json:"csvBase64"`
	}
	if err := c.call(ctx, "swap_voucher_export", params, &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.CSVBase64) == "" {
		return []VoucherExportRecord{}, nil
	}
	raw, err := base64.StdEncoding.DecodeString(payload.CSVBase64)
	if err != nil {
		return nil, fmt.Errorf("swaprpc: decode export: %w", err)
	}
	reader := csv.NewReader(bytes.NewReader(raw))
	header, err := reader.Read()
	if err != nil {
		if err == io.EOF {
			return []VoucherExportRecord{}, nil
		}
		return nil, fmt.Errorf("swaprpc: read export header: %w", err)
	}
	expectedColumns := len(header)
	if expectedColumns < 24 {
		return nil, fmt.Errorf("swaprpc: unexpected export columns %d", expectedColumns)
	}
	records := []VoucherExportRecord{}
	for {
		row, err := reader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("swaprpc: read export row: %w", err)
		}
		if len(row) < expectedColumns {
			// pad missing cells with blanks to avoid index panics
			padded := make([]string, expectedColumns)
			copy(padded, row)
			row = padded
		}
		rec := VoucherExportRecord{
			ProviderTxID:    strings.TrimSpace(row[0]),
			Provider:        strings.TrimSpace(row[1]),
			FiatCurrency:    strings.TrimSpace(row[2]),
			FiatAmount:      strings.TrimSpace(row[3]),
			USD:             strings.TrimSpace(row[4]),
			Rate:            strings.TrimSpace(row[5]),
			Token:           strings.TrimSpace(row[6]),
			MintAmountWei:   strings.TrimSpace(row[7]),
			RecipientHex:    strings.TrimSpace(row[8]),
			Username:        strings.TrimSpace(row[9]),
			Address:         strings.TrimSpace(row[10]),
			Source:          strings.TrimSpace(row[12]),
			OracleMedian:    strings.TrimSpace(row[13]),
			OracleFeeders:   splitCSV(row[14]),
			PriceProofID:    strings.TrimSpace(row[15]),
			MinterSignature: strings.TrimSpace(row[16]),
			Status:          strings.TrimSpace(row[17]),
			TwapRate:        strings.TrimSpace(row[19]),
		}
		rec.QuoteTimestamp = parseInt64(row[11])
		rec.CreatedAt = parseInt64(row[18])
		rec.TwapObservations = parseInt(row[20])
		rec.TwapWindowSeconds = parseInt64(row[21])
		rec.TwapStart = parseInt64(row[22])
		rec.TwapEnd = parseInt64(row[23])
		records = append(records, rec)
	}
	return records, nil
}

func splitCSV(value string) []string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseInt(raw string) int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	v, err := strconv.Atoi(trimmed)
	if err != nil {
		return 0
	}
	return v
}

func parseInt64(raw string) int64 {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0
	}
	v, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0
	}
	return v
}

type rpcRequest struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int64         `json:"id"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *rpcError       `json:"error"`
}

func (c *Client) call(ctx context.Context, method string, params []interface{}, out interface{}) error {
	if c == nil || c.httpClient == nil {
		return fmt.Errorf("swaprpc: client not configured")
	}
	if _, ok := c.allowed[strings.TrimSpace(method)]; !ok {
		return fmt.Errorf("swaprpc: method %q not allowed", method)
	}
	if err := c.consumeRateSlot(); err != nil {
		return err
	}
	id := c.nextID.Add(1)
	reqBody := rpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	buf, err := json.Marshal(reqBody)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	timestamp := strconv.FormatInt(c.nowFn().UTC().Unix(), 10)
	nonce, err := c.nonceFn()
	if err != nil {
		return fmt.Errorf("swaprpc: generate nonce: %w", err)
	}
	signature := gatewayauth.ComputeSignature(c.apiSecret, timestamp, nonce, http.MethodPost, gatewayauth.CanonicalRequestPath(req), buf)
	req.Header.Set(gatewayauth.HeaderAPIKey, c.apiKey)
	req.Header.Set(gatewayauth.HeaderTimestamp, timestamp)
	req.Header.Set(gatewayauth.HeaderNonce, nonce)
	req.Header.Set(gatewayauth.HeaderSignature, hex.EncodeToString(signature))
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var rpcResp rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&rpcResp); err != nil {
		return fmt.Errorf("swaprpc: decode response: %w", err)
	}
	if rpcResp.Error != nil {
		return fmt.Errorf("swaprpc: error %d %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("swaprpc: unexpected status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if len(rpcResp.Result) == 0 {
		return fmt.Errorf("swaprpc: empty result")
	}
	return json.Unmarshal(rpcResp.Result, out)
}

func (c *Client) consumeRateSlot() error {
	if c.rateLimit <= 0 {
		return nil
	}
	now := c.nowFn()
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	if c.rateStart.IsZero() || now.Sub(c.rateStart) >= c.rateWindow {
		c.rateStart = now
		c.rateCount = 0
	}
	if c.rateCount >= c.rateLimit {
		return fmt.Errorf("swaprpc: rate limit exceeded")
	}
	c.rateCount++
	return nil
}

func randomNonce() (string, error) {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf[:]), nil
}

var _ interface {
	SubmitMintVoucher(context.Context, MintSubmission) (string, bool, error)
	GetVoucher(context.Context, string) (*VoucherStatus, error)
} = (*Client)(nil)
