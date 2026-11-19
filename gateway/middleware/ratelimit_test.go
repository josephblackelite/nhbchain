package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"
)

func TestRateLimiterBlocksAfterBurst(t *testing.T) {
	limiter := NewRateLimiter(map[string]RateLimit{
		"lending": {RatePerSecond: 1, Burst: 1},
	}, nil)

	handler := limiter.Middleware("lending")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/lending/accounts", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected first request to succeed, got %d", res.Code)
	}

	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second request to be rate limited, got %d", res.Code)
	}
}

func TestRateLimiterSeparatesRoutes(t *testing.T) {
	limiter := NewRateLimiter(map[string]RateLimit{
		"lending": {RatePerSecond: 1, Burst: 1},
		"swap":    {RatePerSecond: 1, Burst: 1},
	}, nil)

	lendingHandler := limiter.Middleware("lending")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	swapHandler := limiter.Middleware("swap")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/v1/lending/positions", nil)
	res := httptest.NewRecorder()
	lendingHandler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected lending request to succeed, got %d", res.Code)
	}

	swapReq := httptest.NewRequest(http.MethodGet, "/v1/swap/quotes", nil)
	swapRes := httptest.NewRecorder()
	swapHandler.ServeHTTP(swapRes, swapReq)
	if swapRes.Code != http.StatusOK {
		t.Fatalf("expected first swap request to succeed, got %d", swapRes.Code)
	}

	swapRes = httptest.NewRecorder()
	swapHandler.ServeHTTP(swapRes, swapReq)
	if swapRes.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second swap request to hit limit, got %d", swapRes.Code)
	}
}

func TestRateLimiterAppliesRouteTokens(t *testing.T) {
	limiter := NewRateLimiter(map[string]RateLimit{
		"lending": {
			RatePerSecond: 5,
			Burst:         5,
			DefaultTokens: 1,
			Tokens: map[string]int{
				"POST /v1/lending/supply": 3,
			},
		},
	}, nil)

	handler := limiter.Middleware("lending")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/lending/supply", nil)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected first supply request to succeed, got %d", res.Code)
	}

	res = httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("expected second supply request to consume burst and be rate limited, got %d", res.Code)
	}

	// A different route should still be able to proceed because it only
	// consumes the default token cost of 1.
	statusReq := httptest.NewRequest(http.MethodGet, "/v1/lending/status", nil)
	statusRes := httptest.NewRecorder()
	handler.ServeHTTP(statusRes, statusReq)
	if statusRes.Code != http.StatusOK {
		t.Fatalf("expected status route to succeed with default token cost, got %d", statusRes.Code)
	}
}

// The limiter runs before authentication, so a header the caller writes is only
// its own claim and must not choose the bucket it is counted in.
func TestRateLimiterIgnoresClientHeaders(t *testing.T) {
	for _, header := range []string{"X-API-Key", "X-Real-IP", "X-Forwarded-For"} {
		t.Run(header, func(t *testing.T) {
			limiter := NewRateLimiter(map[string]RateLimit{
				"lending": {RatePerSecond: 0.001, Burst: 2},
			}, nil)
			frozen := time.Unix(1700000000, 0)
			limiter.clockNow = func() time.Time { return frozen }

			handler := limiter.Middleware("lending")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			allowed := 0
			for i := 0; i < 50; i++ {
				req := httptest.NewRequest(http.MethodGet, "/v1/lending/positions", nil)
				req.Header.Set(header, fmt.Sprintf("10.9.%d.%d", i/250, i%250+1))
				res := httptest.NewRecorder()
				handler.ServeHTTP(res, req)
				if res.Code == http.StatusOK {
					allowed++
				}
			}
			if allowed != 2 {
				t.Fatalf("a different %s on each request let %d of 50 through from one peer, want the burst of 2", header, allowed)
			}
			if got := len(limiter.visitors); got != 1 {
				t.Fatalf("%d buckets held for one peer, want 1", got)
			}
		})
	}
}

// A bucket is plain data in a bounded table. It must not cost a goroutine, or a
// caller that never gets past the limit (or past authentication) could still
// pile them up.
func TestRateLimiterHoldsNoGoroutinePerClient(t *testing.T) {
	limiter := NewRateLimiter(map[string]RateLimit{
		"lending": {RatePerSecond: 1, Burst: 1},
	}, nil)
	handler := limiter.Middleware("lending")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	before := runtime.NumGoroutine()
	for i := 0; i < 2000; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v1/lending/positions", nil)
		req.RemoteAddr = fmt.Sprintf("10.%d.%d.%d:4000", (i>>16)&255, (i>>8)&255, i&255)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}
	if grew := runtime.NumGoroutine() - before; grew > 20 {
		t.Fatalf("goroutines grew by %d over 2000 clients, want no growth per client", grew)
	}
}

// The 429 carries a Retry-After hint (ledger PL-DC-53), and it survives the rewrite of the limiter that keys buckets on the
// connecting peer (PL-R5T-10): the two changes touched the same lines.
func TestRateLimiterRefusalCarriesRetryAfter(t *testing.T) {
	limiter := NewRateLimiter(map[string]RateLimit{
		"lending": {RatePerSecond: 0.5, Burst: 1},
	}, nil)
	handler := limiter.Middleware("lending")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/lending/accounts", nil)
	handler.ServeHTTP(httptest.NewRecorder(), req)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("expected the second request to be refused, got %d", res.Code)
	}
	if got := res.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("a 0.5 per second bucket needs 2 seconds for one token, Retry-After = %q", got)
	}
}
