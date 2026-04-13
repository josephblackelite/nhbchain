package webhook_test

import (
	"testing"
	"time"

	"nhbchain/services/webhook"
)

func TestRateLimiterPrunesStaleWindows(t *testing.T) {
	limiter := webhook.NewRateLimiter(
		webhook.WithRateWindow(100*time.Millisecond),
		webhook.WithRateTTL(250*time.Millisecond),
		webhook.WithRateCap(128),
	)

	now := time.Unix(0, 0)
	for i := 0; i < 32; i++ {
		if !limiter.Allow(int64(i), 5, now) {
			t.Fatalf("expected first delivery for subscriber %d to be allowed", i)
		}
		now = now.Add(2 * time.Millisecond)
	}
	if got := limiter.Len(); got != 32 {
		t.Fatalf("expected 32 tracked windows, got %d", got)
	}

	// Advance far beyond the TTL and touch a new subscription. All stale entries should be purged.
	now = now.Add(750 * time.Millisecond)
	if !limiter.Allow(99, 5, now) {
		t.Fatalf("expected delivery to be allowed after TTL expiry")
	}
	if got := limiter.Len(); got != 1 {
		t.Fatalf("expected stale windows to be pruned, got %d active entries", got)
	}
}

func TestRateLimiterCapsTrackedSubscriptions(t *testing.T) {
	limiter := webhook.NewRateLimiter(
		webhook.WithRateWindow(time.Second),
		webhook.WithRateTTL(time.Hour),
		webhook.WithRateCap(32),
	)

	now := time.Unix(0, 0)
	for i := 0; i < 256; i++ {
		if !limiter.Allow(int64(i), 10, now) {
			t.Fatalf("expected initial delivery for subscriber %d to be allowed", i)
		}
		if got := limiter.Len(); got > 32 {
			t.Fatalf("rate window map exceeded cap: %d", got)
		}
		now = now.Add(5 * time.Millisecond)
	}

	// The oldest entries should have been dropped to respect the cap. Touching a new id keeps us bounded.
	if !limiter.Allow(999, 10, now) {
		t.Fatalf("expected capped limiter to allow new subscriber")
	}
	if got := limiter.Len(); got > 32 {
		t.Fatalf("expected limiter to keep map size <= 32, got %d", got)
	}
}
