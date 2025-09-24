package p2p

import (
	"math"
	"sync"
	"time"
)

type tokenBucket struct {
	capacity float64
	tokens   float64
	rate     float64
	last     time.Time
	mu       sync.Mutex
}

func newTokenBucket(rate float64, burst float64) *tokenBucket {
	if rate <= 0 {
		return nil
	}
	if burst < 1 {
		burst = 1
	}
	if burst < rate {
		burst = rate
	}
	now := time.Now()
	return &tokenBucket{
		capacity: burst,
		tokens:   burst,
		rate:     rate,
		last:     now,
	}
}

func (b *tokenBucket) allow(now time.Time) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refillLocked(now)
	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}
	return false
}

func (b *tokenBucket) refillLocked(now time.Time) {
	if now.Before(b.last) {
		b.last = now
		return
	}
	elapsed := now.Sub(b.last).Seconds()
	if elapsed <= 0 {
		return
	}
	b.tokens = math.Min(b.capacity, b.tokens+elapsed*b.rate)
	b.last = now
}

func (b *tokenBucket) setRate(rate float64, burst float64) {
	if b == nil {
		return
	}
	if burst < 1 {
		burst = 1
	}
	if burst < rate {
		burst = rate
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refillLocked(time.Now())
	b.rate = rate
	b.capacity = burst
	if b.tokens > b.capacity {
		b.tokens = b.capacity
	}
}

type ipRateLimiter struct {
	rate  float64
	burst float64

	mu     sync.Mutex
	limits map[string]*tokenBucket
}

func newIPRateLimiter(rate float64, burst float64) *ipRateLimiter {
	if rate <= 0 {
		return nil
	}
	if burst < 1 {
		burst = 1
	}
	return &ipRateLimiter{
		rate:   rate,
		burst:  burst,
		limits: make(map[string]*tokenBucket),
	}
}

func (l *ipRateLimiter) allow(ip string, now time.Time) bool {
	if l == nil {
		return true
	}
	if ip == "" {
		return true
	}

	l.mu.Lock()
	bucket := l.limits[ip]
	if bucket == nil {
		bucket = newTokenBucket(l.rate, l.burst)
		l.limits[ip] = bucket
	}
	l.mu.Unlock()

	return bucket.allow(now)
}
