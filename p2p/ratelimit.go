package p2p

import (
	"container/list"
	"math"
	"sync"
	"time"
)

type tokenBucket struct {
	capacity float64
	tokens   float64
	rate     float64
	last     time.Time
	lastSeen time.Time
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
		lastSeen: now,
	}
}

func (b *tokenBucket) allow(now time.Time) bool {
	if b == nil {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.refillLocked(now)
	if now.After(b.lastSeen) {
		b.lastSeen = now
	}
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
	rate        float64
	burst       float64
	idleTimeout time.Duration
	maxEntries  int

	mu     sync.Mutex
	limits map[string]*bucketEntry
	order  *list.List
}

type bucketEntry struct {
	bucket   *tokenBucket
	lastSeen time.Time
	element  *list.Element
}

type ipRateLimiterOption func(*ipRateLimiter)

const (
	defaultIPBucketIdleTimeout = 15 * time.Minute
)

// WithIPRateLimiterIdleTimeout sets the duration after which idle token buckets are evicted.
func WithIPRateLimiterIdleTimeout(timeout time.Duration) ipRateLimiterOption {
	return func(l *ipRateLimiter) {
		l.idleTimeout = timeout
	}
}

// WithIPRateLimiterMaxEntries bounds the number of token buckets retained at once.
// A value <= 0 disables the cap.
func WithIPRateLimiterMaxEntries(max int) ipRateLimiterOption {
	return func(l *ipRateLimiter) {
		l.maxEntries = max
	}
}

func newIPRateLimiter(rate float64, burst float64, opts ...ipRateLimiterOption) *ipRateLimiter {
	if rate <= 0 {
		return nil
	}
	if burst < 1 {
		burst = 1
	}
	limiter := &ipRateLimiter{
		rate:        rate,
		burst:       burst,
		idleTimeout: defaultIPBucketIdleTimeout,
		limits:      make(map[string]*bucketEntry),
		order:       list.New(),
	}
	for _, opt := range opts {
		opt(limiter)
	}
	if limiter.idleTimeout < 0 {
		limiter.idleTimeout = 0
	}
	if limiter.maxEntries < 0 {
		limiter.maxEntries = 0
	}
	return limiter
}

func (l *ipRateLimiter) allow(ip string, now time.Time) bool {
	if l == nil {
		return true
	}
	if ip == "" {
		return true
	}

	l.mu.Lock()
	l.evictIdleLocked(now)

	entry := l.limits[ip]
	if entry == nil {
		l.evictLRULocked()
		bucket := newTokenBucket(l.rate, l.burst)
		entry = &bucketEntry{bucket: bucket}
		entry.element = l.order.PushBack(ip)
		l.limits[ip] = entry
	}
	entry.lastSeen = now
	if entry.element != nil {
		l.order.MoveToBack(entry.element)
	}
	bucket := entry.bucket
	l.mu.Unlock()

	return bucket.allow(now)
}

func (l *ipRateLimiter) evictIdleLocked(now time.Time) {
	if l == nil || l.idleTimeout <= 0 {
		return
	}
	cutoff := now.Add(-l.idleTimeout)
	for {
		front := l.order.Front()
		if front == nil {
			return
		}
		ip, _ := front.Value.(string)
		entry, ok := l.limits[ip]
		if !ok {
			l.order.Remove(front)
			continue
		}
		if !entry.lastSeen.Before(cutoff) {
			return
		}
		l.removeEntryLocked(ip)
	}
}

func (l *ipRateLimiter) evictLRULocked() {
	if l == nil || l.maxEntries <= 0 {
		return
	}
	for len(l.limits) >= l.maxEntries {
		front := l.order.Front()
		if front == nil {
			return
		}
		ip, _ := front.Value.(string)
		l.removeEntryLocked(ip)
	}
}

func (l *ipRateLimiter) removeEntryLocked(ip string) {
	entry, ok := l.limits[ip]
	if !ok {
		return
	}
	if entry.element != nil {
		l.order.Remove(entry.element)
		entry.element = nil
	}
	delete(l.limits, ip)
}
