package p2p

import (
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
