package rpc

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"nhbchain/core"
	"nhbchain/crypto"
	"nhbchain/storage"
)

// TestStakeRateLimitExceededIncludesRetryAfter proves guardStakeRequest's
// "staking rate limit exceeded" 429 carries a Retry-After header once a
// caller is throttled, and that the header is absent from the preceding
// successful calls. guardStakeRequest is exercised directly (bypassing the
// request-level JWT auth every public handler that calls it requires) because
// the rate-limit guard itself, not authentication, is what this test proves.
func TestStakeRateLimitExceededIncludesRetryAfter(t *testing.T) {
	db := storage.NewMemDB()
	t.Cleanup(func() { db.Close() })
	validatorKey, err := crypto.GeneratePrivateKey()
	if err != nil {
		t.Fatalf("generate validator key: %v", err)
	}
	node, err := core.NewNode(db, validatorKey, "", true, false)
	if err != nil {
		t.Fatalf("new node: %v", err)
	}
	server := newTestServer(t, node, nil, ServerConfig{})
	limit := server.rateLimitMax[limiterScopeIP][""]
	window := server.rateLimitWindow
	remoteAddr := "203.0.113.77:5000"

	guard := func() (*httptest.ResponseRecorder, bool) {
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		req.RemoteAddr = remoteAddr
		recorder := httptest.NewRecorder()
		_, ok := server.guardStakeRequest(recorder, req, &RPCRequest{Method: "stake_getPosition", ID: 1})
		return recorder, ok
	}

	for i := 0; i < limit; i++ {
		recorder, ok := guard()
		if !ok {
			t.Fatalf("request %d unexpectedly rate limited before exhausting budget", i)
		}
		if recorder.Header().Get("Retry-After") != "" {
			t.Fatalf("request %d should not carry a Retry-After header", i)
		}
	}

	recorder, ok := guard()
	if ok {
		t.Fatalf("expected the request to be rate limited once the budget is exhausted")
	}
	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429 once budget is exhausted, got %d", recorder.Code)
	}
	retryAfter := recorder.Header().Get("Retry-After")
	if retryAfter == "" {
		t.Fatalf("expected Retry-After header on the staking rate-limited response")
	}
	seconds, err := strconv.Atoi(retryAfter)
	if err != nil {
		t.Fatalf("expected Retry-After to be an integer number of seconds, got %q: %v", retryAfter, err)
	}
	if seconds < 1 || time.Duration(seconds)*time.Second > window {
		t.Fatalf("expected Retry-After to be between 1 second and the rate limit window (%s), got %d seconds", window, seconds)
	}
}
