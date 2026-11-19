package routes

// The rate limit sits in front of the authenticator, so what it keys on has to
// be something the caller cannot choose, and a request that ends in 401 must not
// leave anything running behind it.

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"testing"

	"nhbchain/gateway/middleware"
)

func TestUnauthenticatedCallerCannotRotateItsRateLimitIdentity(t *testing.T) {
	up := newRecordingUpstream(t)
	router := compatRouter(t, up, map[string]middleware.RateLimit{"compat": {RatePerSecond: 0.001, Burst: 2}}, nil)

	before := runtime.NumGoroutine()
	statuses := map[int]int{}
	for i := 0; i < 300; i++ {
		req := httptest.NewRequest(http.MethodPost, "/rpc", nil)
		req.RemoteAddr = "203.0.113.5:4000"
		req.Header.Set("X-API-Key", fmt.Sprintf("key-%d", i))
		req.Header.Set("X-Real-IP", fmt.Sprintf("10.7.%d.%d", i/250, i%250+1))
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.8.%d.%d", i/250, i%250+1))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		statuses[rec.Code]++
	}

	if statuses[http.StatusUnauthorized] != 2 || statuses[http.StatusTooManyRequests] != 298 {
		t.Fatalf("statuses %v, want the burst of 2 to reach the authenticator (401) and the other 298 to be limited (429)", statuses)
	}
	if grew := runtime.NumGoroutine() - before; grew > 20 {
		t.Fatalf("goroutines grew by %d over 300 requests, want no growth per request", grew)
	}
	if calls := up.seen(); len(calls) != 0 {
		t.Fatalf("a caller with no token reached the service: %v", calls)
	}
}

func TestServiceRouteLimitIgnoresClientHeaders(t *testing.T) {
	up := newRecordingUpstream(t)
	target, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	router, err := New(Config{
		Routes: []ServiceRoute{{
			Name:         "consensus",
			Prefix:       "/v1/consensus",
			Target:       target,
			RequireAuth:  false,
			RateLimitKey: "consensus",
		}},
		RateLimiter: middleware.NewRateLimiter(map[string]middleware.RateLimit{"consensus": {RatePerSecond: 0.001, Burst: 3}}, nil),
	})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}

	limited := 0
	for i := 0; i < 100; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/consensus/status", nil)
		req.RemoteAddr = "203.0.113.5:4000"
		req.Header.Set("X-API-Key", fmt.Sprintf("key-%d", i))
		req.Header.Set("X-Forwarded-For", fmt.Sprintf("10.8.0.%d", i+1))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		if rec.Code == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited != 97 {
		t.Fatalf("%d of 100 requests were limited, want 97 (a burst of 3 from one peer)", limited)
	}
}
