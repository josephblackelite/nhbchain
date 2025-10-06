package identity

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Config defines the HTTP client settings for the identity gateway.
type Config struct {
	BaseURL string
	APIKey  string
	Timeout time.Duration
}

// Client retrieves compliance attestations and partner metadata required for minting.
type Client struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

// Resolution captures the identity payload returned by the upstream service.
type Resolution struct {
	PartnerDID       string          `json:"partnerDid"`
	Verified         bool            `json:"verified"`
	SanctionsStatus  string          `json:"sanctionsStatus"`
	ComplianceTags   []string        `json:"complianceTags"`
	TravelRulePacket json.RawMessage `json:"travelRulePacket"`
}

// NewClient constructs a client with sane defaults.
func NewClient(cfg Config) (*Client, error) {
	base := strings.TrimSpace(cfg.BaseURL)
	if base == "" {
		return nil, fmt.Errorf("identity: base url required")
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	return &Client{
		baseURL: strings.TrimRight(base, "/"),
		apiKey:  strings.TrimSpace(cfg.APIKey),
		httpClient: &http.Client{
			Timeout: timeout,
		},
	}, nil
}

// ResolvePartner fetches DID and compliance metadata for the supplied partner identifier.
func (c *Client) ResolvePartner(ctx context.Context, partnerID uuid.UUID) (*Resolution, error) {
	if c == nil {
		return nil, fmt.Errorf("identity: client not configured")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/partners/%s/compliance", c.baseURL, partnerID.String()), nil)
	if err != nil {
		return nil, fmt.Errorf("identity: request: %w", err)
	}
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("identity: call: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("identity: unexpected status %d", resp.StatusCode)
	}
	var payload Resolution
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("identity: decode: %w", err)
	}
	return &payload, nil
}
