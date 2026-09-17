package p2p

import (
	"fmt"
	"testing"
	"time"
)

func TestTokenBucketAllowance(t *testing.T) {
	bucket := newTokenBucket(2, 2)
	if bucket == nil {
		t.Fatalf("expected bucket")
	}
	now := time.Now()
	if !bucket.allow(now) {
		t.Fatalf("first token should be allowed")
	}
	if !bucket.allow(now) {
		t.Fatalf("second token should be allowed")
	}
	if bucket.allow(now) {
		t.Fatalf("bucket should be empty")
	}
	if !bucket.allow(now.Add(500 * time.Millisecond)) {
		t.Fatalf("token should refill after half a second")
	}
}

func TestTokenBucketLastSeen(t *testing.T) {
	bucket := newTokenBucket(1, 1)
	now := bucket.lastSeen
	if !bucket.allow(now) {
		t.Fatalf("token should be available at start")
	}
	if bucket.lastSeen != now {
		t.Fatalf("lastSeen not updated, got %v want %v", bucket.lastSeen, now)
	}
	later := now.Add(time.Second)
	if !bucket.allow(later) {
		t.Fatalf("token should refill after a second")
	}
	if bucket.lastSeen != later {
		t.Fatalf("lastSeen should advance, got %v want %v", bucket.lastSeen, later)
	}
}

func TestIPRateLimiter(t *testing.T) {
	limiter := newIPRateLimiter(1, 1)
	now := time.Now()
	if !limiter.allow("1.2.3.4", now) {
		t.Fatalf("first attempt should be allowed")
	}
	if limiter.allow("1.2.3.4", now) {
		t.Fatalf("burst should be limited")
	}
	if !limiter.allow("5.6.7.8", now) {
		t.Fatalf("different IP should be independent")
	}
	if !limiter.allow("1.2.3.4", now.Add(time.Second)) {
		t.Fatalf("token should refill after rate interval")
	}
}

func TestIPRateLimiterIdleEviction(t *testing.T) {
	limiter := newIPRateLimiter(1, 1, WithIPRateLimiterIdleTimeout(time.Second))
	now := time.Unix(0, 0)

	if !limiter.allow("1.1.1.1", now) {
		t.Fatalf("expected allow for first IP")
	}
	if !limiter.allow("2.2.2.2", now.Add(500*time.Millisecond)) {
		t.Fatalf("expected allow for second IP")
	}
	if !limiter.allow("3.3.3.3", now.Add(1500*time.Millisecond)) {
		t.Fatalf("expected allow for third IP after idle eviction")
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	if _, ok := limiter.limits["1.1.1.1"]; ok {
		t.Fatalf("idle IP should have been evicted")
	}
	if _, ok := limiter.limits["2.2.2.2"]; !ok {
		t.Fatalf("recent IP should remain in limiter")
	}
	if _, ok := limiter.limits["3.3.3.3"]; !ok {
		t.Fatalf("new IP should be present")
	}
}

func TestIPRateLimiterMaxEntries(t *testing.T) {
	const maxEntries = 10
	limiter := newIPRateLimiter(1, 1, WithIPRateLimiterMaxEntries(maxEntries))
	now := time.Unix(0, 0)

	for i := 0; i < 100; i++ {
		ip := fmt.Sprintf("192.0.2.%d", i)
		if !limiter.allow(ip, now.Add(time.Duration(i)*time.Millisecond)) {
			t.Fatalf("unexpected denial for ip %s", ip)
		}
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	if len(limiter.limits) > maxEntries {
		t.Fatalf("limiter should cap entries to %d, got %d", maxEntries, len(limiter.limits))
	}
	if limiter.order.Len() > maxEntries {
		t.Fatalf("order list should not exceed max entries, got %d", limiter.order.Len())
	}
}

func TestIPRateLimiterLRUEviction(t *testing.T) {
        limiter := newIPRateLimiter(10, 10, WithIPRateLimiterMaxEntries(3))
        now := time.Unix(0, 0)

        ips := []string{"1.1.1.1", "2.2.2.2", "3.3.3.3"}
        for i, ip := range ips {
                if !limiter.allow(ip, now.Add(time.Duration(i)*time.Millisecond)) {
			t.Fatalf("unexpected denial for %s", ip)
		}
	}

	// Touch the first IP so it becomes the most recently used.
	if !limiter.allow("1.1.1.1", now.Add(100*time.Millisecond)) {
		t.Fatalf("expected allow for refreshed IP")
	}

	if !limiter.allow("4.4.4.4", now.Add(200*time.Millisecond)) {
		t.Fatalf("expected allow for new IP")
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	if _, ok := limiter.limits["2.2.2.2"]; ok {
		t.Fatalf("least recently used IP should have been evicted")
	}
	if _, ok := limiter.limits["1.1.1.1"]; !ok {
		t.Fatalf("recently refreshed IP should remain")
	}
	if _, ok := limiter.limits["3.3.3.3"]; !ok {
		t.Fatalf("existing IP should remain")
	}
	if _, ok := limiter.limits["4.4.4.4"]; !ok {
		t.Fatalf("new IP should be present")
	}
}
