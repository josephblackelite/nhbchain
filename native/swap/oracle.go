package swap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PriceQuote captures an exchange rate for a specific currency pair along with the
// timestamp reported by the upstream oracle and the oracle identifier.
type PriceQuote struct {
	Rate      *big.Rat
	Timestamp time.Time
	Source    string
}

// Clone returns a deep copy of the quote to prevent accidental mutations.
func (q PriceQuote) Clone() PriceQuote {
	clone := PriceQuote{Timestamp: q.Timestamp, Source: q.Source}
	if q.Rate != nil {
		clone.Rate = new(big.Rat).Set(q.Rate)
	}
	return clone
}

// RateString renders the rate using the supplied precision. The value is rounded
// using bankers rounding in line with big.Rat formatting semantics.
func (q PriceQuote) RateString(precision int) string {
	if q.Rate == nil {
		return ""
	}
	if precision < 0 {
		precision = 18
	}
	return q.Rate.FloatString(precision)
}

// PriceOracle resolves an exchange rate for the provided base/quote currency pair.
type PriceOracle interface {
	GetRate(base, quote string) (PriceQuote, error)
}

// ErrNoFreshQuote indicates that the aggregator could not retrieve a quote within
// the configured freshness window.
var ErrNoFreshQuote = errors.New("swap: no fresh oracle quote available")

// OracleAggregator consults a list of registered oracles in priority order until a
// fresh quote is obtained.
type OracleAggregator struct {
	mu       sync.RWMutex
	priority []string
	oracles  map[string]PriceOracle
	maxAge   time.Duration
}

// NewOracleAggregator constructs a new aggregator with the provided priority and
// freshness window. When priority is nil a zero-length slice is initialised so that
// Register can safely append identifiers without additional checks.
func NewOracleAggregator(priority []string, maxAge time.Duration) *OracleAggregator {
	prio := append([]string{}, priority...)
	return &OracleAggregator{
		priority: prio,
		oracles:  make(map[string]PriceOracle),
		maxAge:   maxAge,
	}
}

// SetMaxAge updates the freshness window used when filtering quotes.
func (a *OracleAggregator) SetMaxAge(maxAge time.Duration) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.maxAge = maxAge
	a.mu.Unlock()
}

// SetPriority replaces the priority ordering used when consulting child oracles.
func (a *OracleAggregator) SetPriority(priority []string) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.priority = append([]string{}, priority...)
	a.mu.Unlock()
}

// Register adds or replaces an oracle under the supplied identifier. Identifiers
// are stored in lowercase to ensure lookups remain consistent regardless of the
// configuration casing.
func (a *OracleAggregator) Register(name string, oracle PriceOracle) {
	if a == nil {
		return
	}
	trimmed := strings.ToLower(strings.TrimSpace(name))
	if trimmed == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.oracles[trimmed] = oracle
	exists := false
	for _, entry := range a.priority {
		if strings.EqualFold(entry, trimmed) {
			exists = true
			break
		}
	}
	if !exists {
		a.priority = append(a.priority, trimmed)
	}
}

// GetRate fetches a rate from the configured oracles respecting the priority
// ordering. The aggregator enforces the freshness window and ensures the returned
// quote contains a defensive copy of the upstream value to avoid callers mutating
// shared state.
func (a *OracleAggregator) GetRate(base, quote string) (PriceQuote, error) {
	if a == nil {
		return PriceQuote{}, fmt.Errorf("oracle aggregator not configured")
	}
	a.mu.RLock()
	priority := append([]string{}, a.priority...)
	maxAge := a.maxAge
	a.mu.RUnlock()

	baseSym := normaliseSymbol(base)
	quoteSym := normaliseSymbol(quote)
	if baseSym == "" || quoteSym == "" {
		return PriceQuote{}, fmt.Errorf("oracle: base and quote required")
	}

	var lastErr error
	cutoff := time.Time{}
	if maxAge > 0 {
		cutoff = time.Now().Add(-maxAge)
	}

	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, name := range priority {
		oracle := a.oracles[strings.ToLower(name)]
		if oracle == nil {
			continue
		}
		quote, err := oracle.GetRate(baseSym, quoteSym)
		if err != nil {
			lastErr = err
			continue
		}
		if quote.Rate == nil || quote.Rate.Sign() <= 0 {
			lastErr = fmt.Errorf("oracle %s returned invalid rate", name)
			continue
		}
		if maxAge > 0 && quote.Timestamp.Before(cutoff) {
			lastErr = ErrNoFreshQuote
			continue
		}
		result := quote.Clone()
		if strings.TrimSpace(result.Source) == "" {
			result.Source = strings.ToLower(name)
		}
		return result, nil
	}

	if lastErr == nil {
		lastErr = ErrNoFreshQuote
	}
	return PriceQuote{}, lastErr
}

// ManualOracle provides an in-memory oracle implementation used for tests and
// manual overrides during incident response.
type ManualOracle struct {
	mu     sync.RWMutex
	quotes map[string]PriceQuote
}

// NewManualOracle constructs an empty manual oracle instance.
func NewManualOracle() *ManualOracle {
	return &ManualOracle{quotes: make(map[string]PriceQuote)}
}

func manualKey(base, quote string) string {
	return normaliseSymbol(base) + "_" + normaliseSymbol(quote)
}

// SetDecimal records the supplied decimal rate for the currency pair using the
// provided timestamp.
func (m *ManualOracle) SetDecimal(base, quote, rate string, ts time.Time) error {
	if m == nil {
		return fmt.Errorf("manual oracle not configured")
	}
	trimmed := strings.TrimSpace(rate)
	if trimmed == "" {
		return fmt.Errorf("manual oracle: rate required")
	}
	rat, ok := new(big.Rat).SetString(trimmed)
	if !ok {
		return fmt.Errorf("manual oracle: invalid rate %q", rate)
	}
	if rat.Sign() <= 0 {
		return fmt.Errorf("manual oracle: rate must be positive")
	}
	m.Set(base, quote, rat, ts)
	return nil
}

// Set stores the provided rational rate for the currency pair.
func (m *ManualOracle) Set(base, quote string, rate *big.Rat, ts time.Time) {
	if m == nil || rate == nil {
		return
	}
	key := manualKey(base, quote)
	if strings.Contains(key, "_") {
		m.mu.Lock()
		clone := PriceQuote{Timestamp: ts, Source: "manual"}
		clone.Rate = new(big.Rat).Set(rate)
		m.quotes[key] = clone
		m.mu.Unlock()
	}
}

// GetRate retrieves the stored rate for the currency pair.
func (m *ManualOracle) GetRate(base, quote string) (PriceQuote, error) {
	if m == nil {
		return PriceQuote{}, fmt.Errorf("manual oracle not configured")
	}
	key := manualKey(base, quote)
	m.mu.RLock()
	stored, ok := m.quotes[key]
	m.mu.RUnlock()
	if !ok {
		return PriceQuote{}, fmt.Errorf("manual oracle: quote for %s/%s not found", base, quote)
	}
	return stored.Clone(), nil
}

// HTTPDoer abstracts http.Client for ease of testing.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// NowPaymentsOracle fetches price data from the NOWPayments quote endpoint.
type NowPaymentsOracle struct {
	client   HTTPDoer
	endpoint string
	apiKey   string
}

const defaultNowPaymentsEndpoint = "https://api.nowpayments.io/v1/exchange/rates"

// NewNowPaymentsOracle constructs a NOWPayments oracle adapter. When the client is
// nil http.DefaultClient is used. The API key is optional and only added to the
// request headers when supplied.
func NewNowPaymentsOracle(client HTTPDoer, endpoint, apiKey string) *NowPaymentsOracle {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		ep = defaultNowPaymentsEndpoint
	}
	if client == nil {
		client = http.DefaultClient
	}
	return &NowPaymentsOracle{client: client, endpoint: ep, apiKey: strings.TrimSpace(apiKey)}
}

func (o *NowPaymentsOracle) GetRate(base, quote string) (PriceQuote, error) {
	if o == nil {
		return PriceQuote{}, fmt.Errorf("nowpayments oracle not configured")
	}
	baseSym := normaliseSymbol(base)
	quoteSym := normaliseSymbol(quote)
	req, err := http.NewRequest(http.MethodGet, o.endpoint, nil)
	if err != nil {
		return PriceQuote{}, err
	}
	values := url.Values{}
	values.Set("from", baseSym)
	values.Set("to", quoteSym)
	req.URL.RawQuery = values.Encode()
	if o.apiKey != "" {
		req.Header.Set("x-api-key", o.apiKey)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return PriceQuote{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return PriceQuote{}, fmt.Errorf("nowpayments oracle: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var payload struct {
		Rate      string `json:"rate"`
		Timestamp int64  `json:"timestamp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return PriceQuote{}, fmt.Errorf("nowpayments oracle: decode: %w", err)
	}
	rate := strings.TrimSpace(payload.Rate)
	if rate == "" {
		return PriceQuote{}, fmt.Errorf("nowpayments oracle: empty rate")
	}
	rat, ok := new(big.Rat).SetString(rate)
	if !ok || rat.Sign() <= 0 {
		return PriceQuote{}, fmt.Errorf("nowpayments oracle: invalid rate %q", payload.Rate)
	}
	ts := time.Unix(payload.Timestamp, 0)
	return PriceQuote{Rate: rat, Timestamp: ts, Source: "nowpayments"}, nil
}

// CoinGeckoOracle adapts the public CoinGecko simple price API.
type CoinGeckoOracle struct {
	client   HTTPDoer
	endpoint string
	idMap    map[string]string
}

const defaultCoinGeckoEndpoint = "https://api.coingecko.com/api/v3/simple/price"

// NewCoinGeckoOracle constructs a new adapter. idMap allows the caller to map
// on-chain token symbols to CoinGecko asset identifiers.
func NewCoinGeckoOracle(client HTTPDoer, endpoint string, idMap map[string]string) *CoinGeckoOracle {
	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		ep = defaultCoinGeckoEndpoint
	}
	if client == nil {
		client = http.DefaultClient
	}
	mapped := make(map[string]string, len(idMap))
	for k, v := range idMap {
		mapped[normaliseSymbol(k)] = strings.TrimSpace(v)
	}
	return &CoinGeckoOracle{client: client, endpoint: ep, idMap: mapped}
}

func (o *CoinGeckoOracle) assetID(symbol string) string {
	if o == nil {
		return ""
	}
	if id, ok := o.idMap[normaliseSymbol(symbol)]; ok && id != "" {
		return id
	}
	return strings.ToLower(strings.TrimSpace(symbol))
}

func (o *CoinGeckoOracle) GetRate(base, quote string) (PriceQuote, error) {
	if o == nil {
		return PriceQuote{}, fmt.Errorf("coingecko oracle not configured")
	}
	baseSym := strings.ToLower(normaliseSymbol(base))
	quoteSym := normaliseSymbol(quote)
	id := o.assetID(quoteSym)
	if id == "" {
		return PriceQuote{}, fmt.Errorf("coingecko oracle: unmapped asset %s", quoteSym)
	}
	req, err := http.NewRequest(http.MethodGet, o.endpoint, nil)
	if err != nil {
		return PriceQuote{}, err
	}
	values := url.Values{}
	values.Set("ids", id)
	values.Set("vs_currencies", baseSym)
	values.Set("include_last_updated_at", "true")
	req.URL.RawQuery = values.Encode()
	resp, err := o.client.Do(req)
	if err != nil {
		return PriceQuote{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return PriceQuote{}, fmt.Errorf("coingecko oracle: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()
	var payload map[string]map[string]interface{}
	if err := decoder.Decode(&payload); err != nil {
		return PriceQuote{}, fmt.Errorf("coingecko oracle: decode: %w", err)
	}
	entry, ok := payload[id]
	if !ok {
		return PriceQuote{}, fmt.Errorf("coingecko oracle: quote missing for %s", quoteSym)
	}
	var priceStr string
	// Attempt lookups for the requested base currency in different casings.
	keys := []string{baseSym, strings.ToLower(baseSym), strings.ToUpper(baseSym)}
	for _, key := range keys {
		if raw, exists := entry[key]; exists {
			switch v := raw.(type) {
			case json.Number:
				priceStr = v.String()
			case string:
				priceStr = v
			case float64:
				priceStr = strconv.FormatFloat(v, 'f', -1, 64)
			default:
				priceStr = fmt.Sprintf("%v", v)
			}
			break
		}
	}
	priceStr = strings.TrimSpace(priceStr)
	if priceStr == "" {
		return PriceQuote{}, fmt.Errorf("coingecko oracle: empty price")
	}
	rat, ok := new(big.Rat).SetString(priceStr)
	if !ok || rat.Sign() <= 0 {
		return PriceQuote{}, fmt.Errorf("coingecko oracle: invalid rate %q", priceStr)
	}
	var ts time.Time
	if rawTs, exists := entry["last_updated_at"]; exists {
		switch v := rawTs.(type) {
		case json.Number:
			if parsed, err := v.Int64(); err == nil && parsed > 0 {
				ts = time.Unix(parsed, 0)
			}
		case string:
			if parsed, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && parsed > 0 {
				ts = time.Unix(parsed, 0)
			}
		case float64:
			if v > 0 {
				ts = time.Unix(int64(v), 0)
			}
		}
	}
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	return PriceQuote{Rate: rat, Timestamp: ts, Source: "coingecko"}, nil
}

func normaliseSymbol(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}

// ComputeMintAmount calculates the mint amount in wei using the provided fiat
// amount (denominated in USD), oracle rate (USD per token) and token decimals.
func ComputeMintAmount(fiatAmount string, rate *big.Rat, decimals uint8) (*big.Int, error) {
	trimmedAmount := strings.TrimSpace(fiatAmount)
	if trimmedAmount == "" {
		return nil, fmt.Errorf("swap: fiat amount required")
	}
	fiat, ok := new(big.Rat).SetString(trimmedAmount)
	if !ok {
		return nil, fmt.Errorf("swap: invalid fiat amount %q", fiatAmount)
	}
	if fiat.Sign() <= 0 {
		return nil, fmt.Errorf("swap: fiat amount must be positive")
	}
	if rate == nil || rate.Sign() <= 0 {
		return nil, fmt.Errorf("swap: oracle rate must be positive")
	}
	tokens := new(big.Rat).Quo(fiat, rate)
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	tokens.Mul(tokens, new(big.Rat).SetInt(scale))
	result := new(big.Int).Quo(tokens.Num(), tokens.Denom())
	if result.Sign() < 0 {
		return nil, fmt.Errorf("swap: computed mint amount negative")
	}
	return result, nil
}

// Config controls swap oracle behaviour and mint validation thresholds.
type Config struct {
	AllowedFiat        []string
	MaxQuoteAgeSeconds int64
	SlippageBps        uint64
	OraclePriority     []string
	Risk               RiskConfig     `toml:"risk"`
	Providers          ProviderConfig `toml:"providers"`
}

// Normalise applies defaults and canonical casing to the configuration values.
func (c Config) Normalise() Config {
	cfg := Config{
		AllowedFiat:        append([]string{}, c.AllowedFiat...),
		MaxQuoteAgeSeconds: c.MaxQuoteAgeSeconds,
		SlippageBps:        c.SlippageBps,
		OraclePriority:     append([]string{}, c.OraclePriority...),
		Risk:               c.Risk.Normalise(),
		Providers:          c.Providers.Normalise(),
	}
	if len(cfg.AllowedFiat) == 0 {
		cfg.AllowedFiat = []string{"USD"}
	}
	for i := range cfg.AllowedFiat {
		cfg.AllowedFiat[i] = normaliseSymbol(cfg.AllowedFiat[i])
	}
	if cfg.MaxQuoteAgeSeconds <= 0 {
		cfg.MaxQuoteAgeSeconds = 120
	}
	if cfg.SlippageBps == 0 {
		cfg.SlippageBps = 50
	}
	if len(cfg.OraclePriority) == 0 {
		cfg.OraclePriority = []string{"manual"}
	}
	return cfg
}

// IsFiatAllowed reports whether the provided fiat currency code is accepted.
func (c Config) IsFiatAllowed(fiat string) bool {
	needle := normaliseSymbol(fiat)
	for _, cur := range c.AllowedFiat {
		if cur == needle {
			return true
		}
	}
	return false
}

// MaxQuoteAge returns the configured freshness window as a duration.
func (c Config) MaxQuoteAge() time.Duration {
	return time.Duration(c.MaxQuoteAgeSeconds) * time.Second
}
