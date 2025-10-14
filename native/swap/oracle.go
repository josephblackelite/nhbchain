package swap

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
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

// TWAPResult captures the summary statistics for a time-weighted average price
// calculation. Average holds the computed rate while Start/End describe the
// time window covered by the observation set.
type TWAPResult struct {
	Average *big.Rat
	Median  *big.Rat
	Start   time.Time
	End     time.Time
	Count   int
	Window  time.Duration
	Feeders []string
	ProofID string
}

// FeedHealth captures metadata about individual feed observations used to drive
// the aggregator and TWAP calculations.
type FeedHealth struct {
	Base         string
	Quote        string
	LastObserved time.Time
	Observations int
}

// Pair renders the canonical pair string in BASE/QUOTE form.
func (fh FeedHealth) Pair() string {
	base := strings.TrimSpace(fh.Base)
	quote := strings.TrimSpace(fh.Quote)
	if base == "" && quote == "" {
		return ""
	}
	if quote == "" {
		return base
	}
	if base == "" {
		return quote
	}
	return base + "/" + quote
}

// OracleHealth aggregates health information for all tracked pairs.
type OracleHealth struct {
	Feeds []FeedHealth
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

// TWAPOracle extends the PriceOracle interface with the ability to report
// time-weighted average price information for auditing and downstream risk
// systems.
type TWAPOracle interface {
	PriceOracle
	TWAP(base, quote string, window time.Duration) (TWAPResult, error)
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
	history  map[string][]PriceQuote
	twapWin  time.Duration
	twapCap  int
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
		history:  make(map[string][]PriceQuote),
		twapCap:  128,
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

// SetTWAPWindow configures the rolling observation window used when computing
// time-weighted average prices. A zero duration disables aggregation while
// negative durations are coerced to zero for safety.
func (a *OracleAggregator) SetTWAPWindow(window time.Duration) {
	if a == nil {
		return
	}
	if window < 0 {
		window = 0
	}
	a.mu.Lock()
	a.twapWin = window
	a.mu.Unlock()
}

// SetTWAPSampleCap bounds the stored sample count per market when calculating
// TWAP values. A non-positive value resets the cap to the default.
func (a *OracleAggregator) SetTWAPSampleCap(cap int) {
	if a == nil {
		return
	}
	if cap <= 0 {
		cap = 128
	}
	a.mu.Lock()
	a.twapCap = cap
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

	for _, name := range priority {
		a.mu.RLock()
		oracle := a.oracles[strings.ToLower(name)]
		a.mu.RUnlock()
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
		a.recordSample(baseSym, quoteSym, result)
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

func (a *OracleAggregator) pairKey(base, quote string) string {
	return normaliseSymbol(base) + ":" + normaliseSymbol(quote)
}

func parsePairKey(key string) (string, string) {
	parts := strings.SplitN(key, ":", 2)
	base := ""
	quote := ""
	if len(parts) > 0 {
		base = strings.TrimSpace(parts[0])
	}
	if len(parts) > 1 {
		quote = strings.TrimSpace(parts[1])
	}
	return base, quote
}

func (a *OracleAggregator) recordSample(base, quote string, quoteOut PriceQuote) {
	if a == nil {
		return
	}
	key := a.pairKey(base, quote)
	sample := quoteOut.Clone()
	if sample.Timestamp.IsZero() {
		sample.Timestamp = time.Now().UTC()
	} else {
		sample.Timestamp = sample.Timestamp.UTC()
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.history == nil {
		a.history = make(map[string][]PriceQuote)
	}
	bucket := append([]PriceQuote{}, a.history[key]...)
	bucket = append(bucket, sample)
	window := a.twapWin
	if window > 0 {
		cutoff := sample.Timestamp.Add(-window)
		filtered := bucket[:0]
		for _, entry := range bucket {
			if entry.Timestamp.Before(cutoff) {
				continue
			}
			filtered = append(filtered, entry)
		}
		bucket = filtered
	}
	if a.twapCap > 0 && len(bucket) > a.twapCap {
		bucket = append([]PriceQuote{}, bucket[len(bucket)-a.twapCap:]...)
	}
	a.history[key] = bucket
}

// TWAP computes the time-weighted average price across the configured rolling
// window. When no observations are available ErrNoFreshQuote is returned to
// mirror the freshness semantics of GetRate.
func (a *OracleAggregator) TWAP(base, quote string, window time.Duration) (TWAPResult, error) {
	if a == nil {
		return TWAPResult{}, fmt.Errorf("oracle aggregator not configured")
	}
	baseSym := normaliseSymbol(base)
	quoteSym := normaliseSymbol(quote)
	if baseSym == "" || quoteSym == "" {
		return TWAPResult{}, fmt.Errorf("oracle: base and quote required")
	}
	key := a.pairKey(baseSym, quoteSym)
	a.mu.RLock()
	defer a.mu.RUnlock()
	bucket := append([]PriceQuote{}, a.history[key]...)
	if len(bucket) == 0 {
		return TWAPResult{}, ErrNoFreshQuote
	}
	if window <= 0 {
		window = a.twapWin
	}
	var (
		cutoff time.Time
		start  time.Time
		end    time.Time
	)
	if window > 0 {
		end = bucket[len(bucket)-1].Timestamp
		if end.IsZero() {
			end = time.Now().UTC()
		}
		cutoff = end.Add(-window)
	}
	sum := big.NewRat(0, 1)
	used := 0
	samples := make([]PriceQuote, 0, len(bucket))
	feeders := make(map[string]struct{})
	for i := 0; i < len(bucket); i++ {
		entry := bucket[i]
		if window > 0 && entry.Timestamp.Before(cutoff) {
			continue
		}
		if entry.Rate == nil {
			continue
		}
		if entry.Timestamp.IsZero() {
			entry.Timestamp = time.Now().UTC()
		}
		if start.IsZero() || entry.Timestamp.Before(start) {
			start = entry.Timestamp
		}
		if entry.Timestamp.After(end) {
			end = entry.Timestamp
		}
		sum.Add(sum, new(big.Rat).Set(entry.Rate))
		used++
		sample := entry.Clone()
		sample.Timestamp = sample.Timestamp.UTC()
		samples = append(samples, sample)
		source := strings.TrimSpace(strings.ToLower(sample.Source))
		if source != "" {
			feeders[source] = struct{}{}
		}
	}
	if used == 0 {
		return TWAPResult{}, ErrNoFreshQuote
	}
	avg := new(big.Rat).Quo(sum, big.NewRat(int64(used), 1))

	median := computeMedian(samples)
	feederList := make([]string, 0, len(feeders))
	for name := range feeders {
		feederList = append(feederList, name)
	}
	sort.Strings(feederList)

	proofID := computeTWAPProofID(baseSym, quoteSym, window, samples)

	return TWAPResult{
		Average: avg,
		Median:  median,
		Start:   start,
		End:     end,
		Count:   used,
		Window:  window,
		Feeders: feederList,
		ProofID: proofID,
	}, nil
}

func computeMedian(samples []PriceQuote) *big.Rat {
	if len(samples) == 0 {
		return nil
	}
	values := make([]*big.Rat, 0, len(samples))
	for _, sample := range samples {
		if sample.Rate == nil {
			continue
		}
		values = append(values, new(big.Rat).Set(sample.Rate))
	}
	if len(values) == 0 {
		return nil
	}
	sort.Slice(values, func(i, j int) bool {
		return values[i].Cmp(values[j]) < 0
	})
	mid := len(values) / 2
	if len(values)%2 == 1 {
		return new(big.Rat).Set(values[mid])
	}
	sum := new(big.Rat).Add(values[mid-1], values[mid])
	return sum.Quo(sum, big.NewRat(2, 1))
}

func computeTWAPProofID(base, quote string, window time.Duration, samples []PriceQuote) string {
	if len(samples) == 0 {
		return ""
	}
	builder := strings.Builder{}
	builder.WriteString(strings.TrimSpace(strings.ToUpper(base)))
	builder.WriteString(":" + strings.TrimSpace(strings.ToUpper(quote)))
	builder.WriteString("|w=")
	builder.WriteString(strconv.FormatInt(int64(window/time.Nanosecond), 10))
	for _, sample := range samples {
		builder.WriteString("|t=")
		builder.WriteString(strconv.FormatInt(sample.Timestamp.UTC().UnixNano(), 10))
		if sample.Rate != nil {
			builder.WriteString("|r=")
			builder.WriteString(sample.Rate.FloatString(18))
		}
		trimmed := strings.TrimSpace(strings.ToLower(sample.Source))
		if trimmed != "" {
			builder.WriteString("|s=")
			builder.WriteString(trimmed)
		}
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

// Health reports the last observation timestamp and sample counts for each
// tracked pair. The information is safe for concurrent access.
func (a *OracleAggregator) Health() OracleHealth {
	if a == nil {
		return OracleHealth{}
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	feeds := make([]FeedHealth, 0, len(a.history))
	for key, samples := range a.history {
		if len(samples) == 0 {
			continue
		}
		last := samples[len(samples)-1]
		base, quote := parsePairKey(key)
		feeds = append(feeds, FeedHealth{
			Base:         base,
			Quote:        quote,
			LastObserved: last.Timestamp,
			Observations: len(samples),
		})
	}
	sort.Slice(feeds, func(i, j int) bool {
		return feeds[i].Pair() < feeds[j].Pair()
	})
	return OracleHealth{Feeds: feeds}
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
	AllowedFiat               []string
	MaxQuoteAgeSeconds        int64
	SlippageBps               uint64
	OraclePriority            []string
	TwapWindowSeconds         int64
	TwapSampleCap             int
	PriceProofMaxDeviationBps uint64
	PayoutAuthorities         []string
	Risk                      RiskConfig      `toml:"risk"`
	Providers                 ProviderConfig  `toml:"providers"`
	Sanctions                 SanctionsConfig `toml:"sanctions"`
}

// Normalise applies defaults and canonical casing to the configuration values.
func (c Config) Normalise() Config {
	cfg := Config{
		AllowedFiat:               append([]string{}, c.AllowedFiat...),
		MaxQuoteAgeSeconds:        c.MaxQuoteAgeSeconds,
		SlippageBps:               c.SlippageBps,
		OraclePriority:            append([]string{}, c.OraclePriority...),
		TwapWindowSeconds:         c.TwapWindowSeconds,
		TwapSampleCap:             c.TwapSampleCap,
		PriceProofMaxDeviationBps: c.PriceProofMaxDeviationBps,
		PayoutAuthorities:         append([]string{}, c.PayoutAuthorities...),
		Risk:                      c.Risk.Normalise(),
		Providers:                 c.Providers.Normalise(),
		Sanctions:                 c.Sanctions.Normalise(),
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
	if cfg.TwapWindowSeconds < 0 {
		cfg.TwapWindowSeconds = 0
	}
	if cfg.TwapSampleCap <= 0 {
		cfg.TwapSampleCap = 128
	}
	if cfg.PriceProofMaxDeviationBps == 0 {
		cfg.PriceProofMaxDeviationBps = 100
	}
	if len(cfg.PayoutAuthorities) == 0 {
		cfg.PayoutAuthorities = []string{"treasury"}
	}
	trimmed := make([]string, 0, len(cfg.PayoutAuthorities))
	seen := make(map[string]struct{}, len(cfg.PayoutAuthorities))
	for _, authority := range cfg.PayoutAuthorities {
		canonical := strings.ToLower(strings.TrimSpace(authority))
		if canonical == "" {
			continue
		}
		if _, ok := seen[canonical]; ok {
			continue
		}
		seen[canonical] = struct{}{}
		trimmed = append(trimmed, canonical)
	}
	cfg.PayoutAuthorities = trimmed
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

// TwapWindow converts the configured TWAP window seconds into a duration.
func (c Config) TwapWindow() time.Duration {
	if c.TwapWindowSeconds <= 0 {
		return 0
	}
	return time.Duration(c.TwapWindowSeconds) * time.Second
}
