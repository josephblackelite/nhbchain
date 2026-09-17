package pricing

import (
	"fmt"
	"math"
	"math/big"
	"strings"
	"time"

	"nhbchain/native/loyalty"
	"nhbchain/native/swap"
)

// PriceStatus captures the health classification assigned to an oracle quote.
type PriceStatus string

const (
	// PriceStatusOK indicates the quote passed all configured guardrails.
	PriceStatusOK PriceStatus = "ok"
	// PriceStatusStale signals the quote exceeded the configured freshness window.
	PriceStatusStale PriceStatus = "stale"
	// PriceStatusDeviant indicates the quote deviated from the configured TWAP threshold.
	PriceStatusDeviant PriceStatus = "deviant"
)

// ZNHBUSDQuote summarises the price feed response for the ZNHB/USD pair.
type ZNHBUSDQuote struct {
	// PriceQ64 encodes the USD-per-ZNHB rate using Q64.64 fixed-point representation.
	PriceQ64 *big.Int
	// AgeSeconds reports how old the underlying observation is relative to the provided clock.
	AgeSeconds uint32
	// Status classifies the quote as healthy, stale, or deviant.
	Status PriceStatus
}

// PriceFeed exposes the pricing helpers consumed by modules enforcing guardrails.
type PriceFeed interface {
	// GetZNHBUSD resolves the latest ZNHB/USD price according to the configured guards.
	GetZNHBUSD(tsNow time.Time) (ZNHBUSDQuote, error)
}

// defaultQuoteFn resolves an instantaneous oracle quote.
var defaultQuoteFn = func(o swap.PriceOracle, base, quote string) (swap.PriceQuote, error) {
	if o == nil {
		return swap.PriceQuote{}, fmt.Errorf("pricing: oracle not configured")
	}
	return o.GetRate(base, quote)
}

// defaultTWAPFn resolves the TWAP observation for the configured window.
var defaultTWAPFn = func(o swap.TWAPOracle, base, quote string, window time.Duration) (swap.TWAPResult, error) {
	if o == nil {
		return swap.TWAPResult{}, fmt.Errorf("pricing: oracle not configured")
	}
	return o.TWAP(base, quote, window)
}

// DefaultPriceFeed wires a swap oracle together with the loyalty price guard settings.
type DefaultPriceFeed struct {
	oracle swap.TWAPOracle
	guard  loyalty.PriceGuardConfig
	token  string
	fiat   string
}

// NewDefaultPriceFeed constructs the canonical price feed backed by the supplied oracle and guard config.
func NewDefaultPriceFeed(oracle swap.TWAPOracle, guard loyalty.PriceGuardConfig) (PriceFeed, error) {
	if oracle == nil {
		return nil, fmt.Errorf("pricing: oracle required")
	}
	normalized := guard
	normalized.Normalize()
	parts := strings.Split(normalized.PricePair, "/")
	if len(parts) != 2 {
		return nil, fmt.Errorf("pricing: invalid price pair %q", guard.PricePair)
	}
	token := strings.ToUpper(strings.TrimSpace(parts[0]))
	fiat := strings.ToUpper(strings.TrimSpace(parts[1]))
	if token == "" || fiat == "" {
		return nil, fmt.Errorf("pricing: invalid price pair %q", guard.PricePair)
	}
	feed := &DefaultPriceFeed{
		oracle: oracle,
		guard:  normalized,
		token:  token,
		fiat:   fiat,
	}
	return feed, nil
}

// GetZNHBUSD resolves the guarded ZNHB/USD quote using the configured oracle and guardrails.
func (f *DefaultPriceFeed) GetZNHBUSD(tsNow time.Time) (ZNHBUSDQuote, error) {
	if f == nil {
		return ZNHBUSDQuote{}, fmt.Errorf("pricing: feed not initialised")
	}
	if tsNow.IsZero() {
		tsNow = time.Now()
	}
	tsNow = tsNow.UTC()

	quote, err := defaultQuoteFn(f.oracle, f.fiat, f.token)
	if err != nil {
		return ZNHBUSDQuote{}, err
	}
	if quote.Rate == nil || quote.Rate.Sign() <= 0 {
		return ZNHBUSDQuote{}, fmt.Errorf("pricing: invalid oracle rate")
	}

	status := PriceStatusOK
	observationTs := quote.Timestamp.UTC()
	ageSeconds := computeAgeSeconds(observationTs, tsNow)

	if f.guard.PriceMaxAgeSeconds > 0 {
		if observationTs.IsZero() || uint64(ageSeconds) > uint64(f.guard.PriceMaxAgeSeconds) {
			status = PriceStatusStale
		}
	}

	if status != PriceStatusStale && f.guard.MaxDeviationBps > 0 {
		window := time.Duration(f.guard.TwapWindowSeconds) * time.Second
		twap, err := defaultTWAPFn(f.oracle, f.fiat, f.token, window)
		if err == nil && twap.Average != nil && twap.Average.Sign() > 0 {
			if deviatesBeyondThreshold(quote.Rate, twap.Average, f.guard.MaxDeviationBps) {
				status = PriceStatusDeviant
			}
		}
	}

	priceQ64 := ratToQ64(quote.Rate)
	if priceQ64 == nil {
		return ZNHBUSDQuote{}, fmt.Errorf("pricing: failed to encode oracle rate")
	}

	return ZNHBUSDQuote{PriceQ64: priceQ64, AgeSeconds: ageSeconds, Status: status}, nil
}

func computeAgeSeconds(observed, now time.Time) uint32 {
	if observed.IsZero() || now.IsZero() {
		return math.MaxUint32
	}
	observed = observed.UTC()
	now = now.UTC()
	if observed.After(now) {
		return 0
	}
	delta := now.Sub(observed)
	if delta <= 0 {
		return 0
	}
	seconds := delta / time.Second
	if seconds < 0 {
		return 0
	}
	if seconds > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(seconds)
}

func deviatesBeyondThreshold(spot, average *big.Rat, thresholdBps uint32) bool {
	if spot == nil || average == nil {
		return false
	}
	if average.Sign() <= 0 {
		return false
	}
	diff := new(big.Rat).Sub(spot, average)
	if diff.Sign() < 0 {
		diff.Neg(diff)
	}
	if diff.Sign() == 0 {
		return false
	}
	ratio := new(big.Rat).Quo(diff, average)
	ratio.Mul(ratio, big.NewRat(10000, 1))
	threshold := big.NewRat(int64(thresholdBps), 1)
	return ratio.Cmp(threshold) == 1
}

func ratToQ64(rate *big.Rat) *big.Int {
	if rate == nil {
		return nil
	}
	numerator := new(big.Int).Set(rate.Num())
	numerator.Mul(numerator, new(big.Int).Lsh(big.NewInt(1), 64))
	denominator := rate.Denom()
	if denominator.Sign() == 0 {
		return nil
	}
	return numerator.Quo(numerator, denominator)
}
