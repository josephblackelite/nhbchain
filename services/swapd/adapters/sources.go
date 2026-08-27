package adapters

import (
	"context"
	"fmt"
	"math/big"
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
	case "fixed":
		return newFixedSource(name, assets)
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

// newFixedSource builds a source for an asset that has no real external
// market to price -- NHB (this chain's own native asset) is pegged 1:1 to
// USD by product design (see payments-gateway's computeMintAmount, which
// already special-cases NHB the same way instead of consulting an oracle),
// not something NOWPayments or CoinGecko can look up. `assets` reuses the
// same map[string]string shape the coingecko source's config already uses,
// but here each value is the fixed rate itself (e.g. "1.0"), not an
// external ticker/id.
//
// Rates are parsed eagerly, at config-build time, and any parse failure or
// non-positive rate is a hard error rather than being silently skipped: a
// misconfigured fixed rate that's simply dropped would surface later as a
// confusing, unexplained "price unavailable" for that asset instead of a
// clear startup failure pointing at the actual bad config.
func newFixedSource(name string, assets map[string]string) (oracle.Source, error) {
	rates := make(map[string]*big.Rat, len(assets))
	for symbol, rateStr := range assets {
		trimmedSymbol := strings.ToUpper(strings.TrimSpace(symbol))
		if trimmedSymbol == "" {
			return nil, fmt.Errorf("fixed source %s: asset symbol must not be empty", label(name, "fixed"))
		}
		rat, ok := new(big.Rat).SetString(strings.TrimSpace(rateStr))
		if !ok {
			return nil, fmt.Errorf("fixed source %s: invalid rate %q for asset %s", label(name, "fixed"), rateStr, trimmedSymbol)
		}
		if rat.Sign() <= 0 {
			return nil, fmt.Errorf("fixed source %s: rate for asset %s must be positive, got %s", label(name, "fixed"), trimmedSymbol, rateStr)
		}
		rates[trimmedSymbol] = rat
	}
	sourceName := label(name, "fixed")
	return &sourceAdapter{name: sourceName, fetch: func(ctx context.Context, base, quote string) (swap.PriceQuote, error) {
		_ = ctx
		_ = quote
		rat, ok := rates[strings.ToUpper(strings.TrimSpace(base))]
		if !ok {
			return swap.PriceQuote{}, fmt.Errorf("fixed source %s: no configured rate for %s", sourceName, base)
		}
		return swap.PriceQuote{Rate: new(big.Rat).Set(rat), Timestamp: time.Now(), Source: sourceName}, nil
	}}, nil
}

func label(name, fallback string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed != "" {
		return trimmed
	}
	return fallback
}
