package adapters

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	swap "nhbchain/native/swap"
	"nhbchain/services/swapd/oracle"
)

// Registry constructs oracle sources based on configuration.
type Registry struct {
	HTTPClient *http.Client
}

// NewRegistry builds a registry with sane defaults.
func NewRegistry() *Registry {
	return &Registry{HTTPClient: &http.Client{Timeout: 10 * time.Second}}
}

// Build creates a source from the supplied configuration.
func (r *Registry) Build(name, typ, endpoint, apiKey string, assets map[string]string) (oracle.Source, error) {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "nowpayments":
		return newNowPaymentsSource(r.client(), name, endpoint, apiKey), nil
	case "coingecko":
		return newCoinGeckoSource(r.client(), name, endpoint, assets), nil
	default:
		return nil, fmt.Errorf("unknown oracle type %q", typ)
	}
}

func (r *Registry) client() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return &http.Client{Timeout: 10 * time.Second}
}

type sourceAdapter struct {
	name  string
	fetch func(ctx context.Context, base, quote string) (swap.PriceQuote, error)
}

func (s *sourceAdapter) Name() string { return s.name }

func (s *sourceAdapter) Fetch(ctx context.Context, base, quote string) (swap.PriceQuote, error) {
	return s.fetch(ctx, base, quote)
}

func newNowPaymentsSource(client *http.Client, name, endpoint, apiKey string) oracle.Source {
	ora := swap.NewNowPaymentsOracle(client, endpoint, apiKey)
	return &sourceAdapter{name: label(name, "nowpayments"), fetch: func(ctx context.Context, base, quote string) (swap.PriceQuote, error) {
		_ = ctx
		return ora.GetRate(base, quote)
	}}
}

func newCoinGeckoSource(client *http.Client, name, endpoint string, assets map[string]string) oracle.Source {
	ora := swap.NewCoinGeckoOracle(client, endpoint, assets)
	return &sourceAdapter{name: label(name, "coingecko"), fetch: func(ctx context.Context, base, quote string) (swap.PriceQuote, error) {
		_ = ctx
		return ora.GetRate(base, quote)
	}}
}

func label(name, fallback string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed != "" {
		return trimmed
	}
	return fallback
}
