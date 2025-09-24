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

	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens = math.Min(b.capacity, b.tokens+elapsed*b.rate)
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens -= 1
		return true
	}
	return false
}
