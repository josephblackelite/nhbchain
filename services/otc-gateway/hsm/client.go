package hsm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
)

// Signer abstracts the capability to sign mint voucher digests using the MINTER_NHB key.
type Signer interface {
	Sign(ctx context.Context, digest []byte) ([]byte, string, error)
}

// Config captures the parameters required to establish an mTLS session with the HSM proxy.
type Config struct {
	BaseURL    string
	KeyLabel   string
	CACertPath string
	ClientCert string
	ClientKey  string
	Timeout    time.Duration
	SignPath   string
	OverrideDN string
}

// Client implements the Signer interface using an mTLS authenticated HTTP client.
type Client struct {
	keyLabel   string
	httpClient *http.Client
	baseURL    string
	signPath   string
	overrideDN string
}

// NewClient builds an HSM client using the supplied configuration.
func NewClient(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, fmt.Errorf("hsm: base url required")
	}
	if strings.TrimSpace(cfg.KeyLabel) == "" {
		return nil, fmt.Errorf("hsm: key label required")
	}
	tlsConfig, err := buildTLSConfig(cfg)
	if err != nil {
		return nil, err
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	signPath := strings.TrimSpace(cfg.SignPath)
	if signPath == "" {
		signPath = "/sign"
	}
	client := &Client{
		keyLabel: strings.TrimSpace(cfg.KeyLabel),
		httpClient: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		signPath:   signPath,
		overrideDN: strings.TrimSpace(cfg.OverrideDN),
	}
	return client, nil
}

func buildTLSConfig(cfg Config) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(cfg.ClientCert, cfg.ClientKey)
	if err != nil {
		return nil, fmt.Errorf("hsm: load client certificate: %w", err)
	}
	rootPool, err := loadCACert(cfg.CACertPath)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		RootCAs:      rootPool,
	}, nil
}

func loadCACert(path string) (*x509.CertPool, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("hsm: ca certificate required")
	}
	pemBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("hsm: read ca certificate: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		return nil, fmt.Errorf("hsm: failed to append ca certificate %s", path)
	}
	return pool, nil
}

type signRequest struct {
	KeyLabel string `json:"key"`
	Digest   string `json:"digest"`
}

type signResponse struct {
	Signature string `json:"signature"`
	SignerDN  string `json:"signerDn"`
}

// Sign requests the HSM proxy to sign the supplied digest. It returns the DER encoded signature
// and the distinguished name of the signing certificate when available.
func (c *Client) Sign(ctx context.Context, digest []byte) ([]byte, string, error) {
	if c == nil || c.httpClient == nil {
		return nil, "", fmt.Errorf("hsm: client not configured")
	}
	trimmed := strings.TrimSpace(hex.EncodeToString(digest))
	if trimmed == "" {
		return nil, "", fmt.Errorf("hsm: digest required")
	}
	payload := signRequest{KeyLabel: c.keyLabel, Digest: trimmed}
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, "", err
	}
	url := c.baseURL + path.Clean("/"+c.signPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("hsm: sign failed: status=%d", resp.StatusCode)
	}
	var decoded signResponse
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return nil, "", fmt.Errorf("hsm: decode response: %w", err)
	}
	sigHex := strings.TrimSpace(decoded.Signature)
	if sigHex == "" {
		return nil, "", fmt.Errorf("hsm: empty signature")
	}
	sigHex = strings.TrimPrefix(sigHex, "0x")
	signature, err := hex.DecodeString(sigHex)
	if err != nil {
		return nil, "", fmt.Errorf("hsm: invalid signature encoding: %w", err)
	}
	signerDN := strings.TrimSpace(decoded.SignerDN)
	if signerDN == "" {
		signerDN = c.overrideDN
	}
	return signature, signerDN, nil
}

var _ Signer = (*Client)(nil)
