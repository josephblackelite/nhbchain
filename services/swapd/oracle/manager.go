package oracle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"sort"
	"strings"
	"sync"
	"time"

	swap "nhbchain/native/swap"
	"nhbchain/services/swapd/storage"
)

// Source resolves a price quote for a currency pair.
type Source interface {
	Name() string
	Fetch(ctx context.Context, base, quote string) (swap.PriceQuote, error)
}

// Publisher pushes oracle updates onto the consensus layer.
type Publisher interface {
	PublishOracleUpdate(ctx context.Context, update Update) error
}

// Update models the payload forwarded to consensus.
type Update struct {
	Base    string
	Quote   string
	Median  string
	Feeders []string
	ProofID string
	Time    time.Time
}

// Manager orchestrates periodic aggregation across configured sources.
type Manager struct {
	logger    *log.Logger
	storage   *storage.Storage
	sources   []Source
	pairs     []Pair
	minFeeds  int
	maxAge    time.Duration
	interval  time.Duration
	publisher Publisher
	once      sync.Once
}

// Pair identifies a base/quote pair.
type Pair struct {
	Base  string
	Quote string
}

// Option configures a Manager.
type Option func(*Manager)

// WithLogger installs a custom logger.
func WithLogger(l *log.Logger) Option {
	return func(m *Manager) {
		m.logger = l
	}
}

// WithPublisher overrides the default publisher.
func WithPublisher(p Publisher) Option {
	return func(m *Manager) {
		m.publisher = p
	}
}

// New constructs a manager instance.
func New(store *storage.Storage, sources []Source, pairs []Pair, interval, maxAge time.Duration, minFeeds int, opts ...Option) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("storage required")
	}
	if len(sources) == 0 {
		return nil, fmt.Errorf("at least one source required")
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("at least one pair required")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("interval must be positive")
	}
	if maxAge <= 0 {
		maxAge = time.Minute
	}
	if minFeeds <= 0 {
		minFeeds = 1
	}
	mgr := &Manager{
		logger:   log.Default(),
		storage:  store,
		sources:  append([]Source{}, sources...),
		pairs:    append([]Pair{}, pairs...),
		interval: interval,
		maxAge:   maxAge,
		minFeeds: minFeeds,
		publisher: PublisherFunc(func(context.Context, Update) error {
			return nil
		}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(mgr)
		}
	}
	if mgr.publisher == nil {
		mgr.publisher = PublisherFunc(func(context.Context, Update) error { return nil })
	}
	return mgr, nil
}

// Run blocks, periodically polling upstream feeds until the context is cancelled.
func (m *Manager) Run(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("manager not configured")
	}
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	m.once.Do(func() {
		m.logger.Printf("swapd: oracle manager started with %d sources", len(m.sources))
	})
	for {
		if err := m.Tick(ctx); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.logger.Printf("swapd: tick error: %v", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Tick performs a single aggregation cycle across all configured pairs.
func (m *Manager) Tick(ctx context.Context) error {
	if m == nil {
		return fmt.Errorf("manager not configured")
	}
	for _, pair := range m.pairs {
		if err := m.processPair(ctx, pair); err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) processPair(ctx context.Context, pair Pair) error {
	base := strings.TrimSpace(pair.Base)
	quote := strings.TrimSpace(pair.Quote)
	if base == "" || quote == "" {
		return fmt.Errorf("invalid pair configuration")
	}
	now := time.Now()
	quotes := make([]swap.PriceQuote, 0, len(m.sources))
	feeders := make([]string, 0, len(m.sources))
	for _, src := range m.sources {
		if src == nil {
			continue
		}
		quoteOut, err := src.Fetch(ctx, base, quote)
		if err != nil {
			m.logger.Printf("swapd: source %s failed for %s/%s: %v", src.Name(), base, quote, err)
			continue
		}
		if quoteOut.Rate == nil || quoteOut.Rate.Sign() <= 0 {
			m.logger.Printf("swapd: source %s returned invalid rate", src.Name())
			continue
		}
		if quoteOut.Timestamp.After(now.Add(5 * time.Second)) {
			m.logger.Printf("swapd: source %s produced future timestamp", src.Name())
			continue
		}
		if m.maxAge > 0 && quoteOut.Timestamp.Before(now.Add(-m.maxAge)) {
			m.logger.Printf("swapd: source %s quote expired", src.Name())
			continue
		}
		feeders = append(feeders, src.Name())
		quotes = append(quotes, quoteOut.Clone())
		if err := m.storage.RecordSample(ctx, base, quote, src.Name(), quoteOut, now); err != nil {
			m.logger.Printf("swapd: record sample: %v", err)
		}
	}
	if len(quotes) < m.minFeeds {
		return fmt.Errorf("insufficient oracle feeds for %s/%s", base, quote)
	}
	median := computeMedian(quotes)
	if median == nil || median.Sign() <= 0 {
		return fmt.Errorf("median computation failed for %s/%s", base, quote)
	}
	proof := proofID(base, quote, feeders, now)
	medianStr := median.FloatString(18)
	if err := m.storage.RecordSnapshot(ctx, base, quote, medianStr, feeders, proof, now); err != nil {
		return fmt.Errorf("record snapshot: %w", err)
	}
	update := Update{Base: base, Quote: quote, Median: medianStr, Feeders: feeders, ProofID: proof, Time: now}
	if err := m.publisher.PublishOracleUpdate(ctx, update); err != nil {
		return fmt.Errorf("publish update: %w", err)
	}
	return nil
}

func computeMedian(quotes []swap.PriceQuote) *big.Rat {
	if len(quotes) == 0 {
		return nil
	}
	sorted := make([]*big.Rat, 0, len(quotes))
	for _, q := range quotes {
		if q.Rate == nil {
			continue
		}
		sorted = append(sorted, new(big.Rat).Set(q.Rate))
	}
	if len(sorted) == 0 {
		return nil
	}
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Cmp(sorted[j]) < 0
	})
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return new(big.Rat).Set(sorted[mid])
	}
	sum := new(big.Rat).Add(sorted[mid-1], sorted[mid])
	return sum.Quo(sum, big.NewRat(2, 1))
}

func proofID(base, quote string, feeders []string, ts time.Time) string {
	digest := sha256.New()
	digest.Write([]byte(strings.ToUpper(strings.TrimSpace(base))))
	digest.Write([]byte("/"))
	digest.Write([]byte(strings.ToUpper(strings.TrimSpace(quote))))
	digest.Write([]byte(ts.UTC().Format(time.RFC3339Nano)))
	sorted := append([]string{}, feeders...)
	sort.Strings(sorted)
	for _, f := range sorted {
		digest.Write([]byte(strings.ToLower(strings.TrimSpace(f))))
	}
	return hex.EncodeToString(digest.Sum(nil))
}

// PublisherFunc adapts ordinary functions to Publisher.
type PublisherFunc func(ctx context.Context, update Update) error

// PublishOracleUpdate implements Publisher.
func (f PublisherFunc) PublishOracleUpdate(ctx context.Context, update Update) error {
	if f == nil {
		return nil
	}
	return f(ctx, update)
}
