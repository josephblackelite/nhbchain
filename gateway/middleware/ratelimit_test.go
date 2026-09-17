package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
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
	req.Header.Set("X-API-Key", "tenant-A")
	res := httptest.NewRecorder()
	lendingHandler.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("expected lending request to succeed, got %d", res.Code)
	}

	swapReq := httptest.NewRequest(http.MethodGet, "/v1/swap/quotes", nil)
	swapReq.Header.Set("X-API-Key", "tenant-A")
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

func TestRateLimiterPrefersAPIKeyOverIP(t *testing.T) {
	limiter := NewRateLimiter(map[string]RateLimit{
		"lending": {RatePerSecond: 1, Burst: 1},
	}, nil)

	handler := limiter.Middleware("lending")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	reqA := httptest.NewRequest(http.MethodGet, "/v1/lending/positions", nil)
	reqA.Header.Set("X-API-Key", "tenant-A")
	resA := httptest.NewRecorder()
	handler.ServeHTTP(resA, reqA)
	if resA.Code != http.StatusOK {
		t.Fatalf("expected tenant A request to succeed, got %d", resA.Code)
	}

	reqB := httptest.NewRequest(http.MethodGet, "/v1/lending/positions", nil)
	reqB.Header.Set("X-API-Key", "tenant-B")
	resB := httptest.NewRecorder()
	handler.ServeHTTP(resB, reqB)
	if resB.Code != http.StatusOK {
		t.Fatalf("expected tenant B request to succeed, got %d", resB.Code)
	}
}
