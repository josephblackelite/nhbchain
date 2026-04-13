package main

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// PriceSample captures a single oracle price observation.
type PriceSample struct {
	Value     float64
	Timestamp time.Time
}

// Oracle maintains a set of price feeds per token and exposes resilient median pricing.
type Oracle struct {
	mu           sync.RWMutex
	ttl          time.Duration
	maxDeviation float64
	breaker      float64
	feeds        map[string]map[string]PriceSample
	lastAccepted map[string]float64
}

// ErrPriceUnavailable indicates the oracle cannot provide a price.
var ErrPriceUnavailable = errors.New("price unavailable")

// NewOracle creates a new median oracle.
func NewOracle(ttl time.Duration, maxDeviation, breaker float64) *Oracle {
	return &Oracle{
		ttl:          ttl,
		maxDeviation: maxDeviation,
		breaker:      breaker,
		feeds:        make(map[string]map[string]PriceSample),
		lastAccepted: make(map[string]float64),
	}
}

// Update records a new price observation for a feed.
func (o *Oracle) Update(token, feed string, value float64, observed time.Time) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if _, ok := o.feeds[token]; !ok {
		o.feeds[token] = make(map[string]PriceSample)
	}
	if observed.IsZero() {
		observed = time.Now().UTC()
	}
	o.feeds[token][feed] = PriceSample{Value: value, Timestamp: observed}
}

// Price computes the median price for the specified token. Values outside of the deviation cap are discarded.
func (o *Oracle) Price(token string, now time.Time) (float64, error) {
	o.mu.RLock()
	defer o.mu.RUnlock()
	feeds, ok := o.feeds[token]
	if !ok || len(feeds) == 0 {
		return 0, ErrPriceUnavailable
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	var values []float64
	for _, sample := range feeds {
		if now.Sub(sample.Timestamp) > o.ttl {
			continue
		}
		values = append(values, sample.Value)
	}
	if len(values) == 0 {
		return 0, ErrPriceUnavailable
	}
	sort.Float64s(values)
	median := values[len(values)/2]
	if len(values)%2 == 0 {
		median = (values[len(values)/2-1] + values[len(values)/2]) / 2
	}
	if median <= 0 {
		return 0, ErrPriceUnavailable
	}
	if o.maxDeviation > 0 {
		filtered := make([]float64, 0, len(values))
		for _, v := range values {
			diff := absFloat((v - median) / median)
			if diff <= o.maxDeviation {
				filtered = append(filtered, v)
			}
		}
		if len(filtered) == 0 {
			return 0, ErrPriceUnavailable
		}
		sort.Float64s(filtered)
		median = filtered[len(filtered)/2]
		if len(filtered)%2 == 0 {
			median = (filtered[len(filtered)/2-1] + filtered[len(filtered)/2]) / 2
		}
	}
	if prev, ok := o.lastAccepted[token]; ok && o.breaker > 0 {
		diff := absFloat((median - prev) / prev)
		if diff > o.breaker {
			return 0, ErrPriceUnavailable
		}
	}
	o.lastAccepted[token] = median
	return median, nil
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
