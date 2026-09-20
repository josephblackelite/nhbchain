package routes

// The /rpc compatibility endpoint goes through the gateway's authenticator and
// rate limit like the routes it stands in for, and passes the caller's own
// credential on to the service that answers.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"

	"nhbchain/gateway/compat"
	"nhbchain/gateway/middleware"
)

type upstreamCall struct {
	method, path, authorization string
}

type recordingUpstream struct {
	*httptest.Server
	mu    sync.Mutex
	calls []upstreamCall
}

func newRecordingUpstream(t *testing.T) *recordingUpstream {
	t.Helper()
	up := &recordingUpstream{}
	up.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		up.mu.Lock()
		up.calls = append(up.calls, upstreamCall{r.Method, r.URL.Path, r.Header.Get("Authorization")})
		up.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(up.Close)
	return up
}

func (u *recordingUpstream) seen() []upstreamCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]upstreamCall(nil), u.calls...)
}

const compatTestSecret = "compat-test-secret"

func compatToken(t *testing.T, scopes string) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss":   "gateway-tests",
		"aud":   "gateway",
		"scope": scopes,
		"exp":   time.Now().Add(time.Hour).Unix(),
	})
	signed, err := token.SignedString([]byte(compatTestSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return signed
}

// compatRouter builds the gateway router with the compatibility dispatcher in
// front of up, the way cmd/gateway does. configure, when not nil, adjusts the
// dispatcher (the scope guard) before it is mounted.
func compatRouter(t *testing.T, up *recordingUpstream, limits map[string]middleware.RateLimit, configure func(*compat.Dispatcher)) http.Handler {
	t.Helper()
	base, err := url.Parse(up.URL)
	if err != nil {
		t.Fatalf("parse upstream: %v", err)
	}
	services := []*compat.Service{
		{Name: "lendingd", BaseURL: base},
		{Name: "swapd", BaseURL: base},
	}
	dispatcher := compat.NewDispatcher(services, compat.DefaultMappings)
	if configure != nil {
		configure(dispatcher)
	}
	auth := middleware.NewAuthenticator(middleware.AuthConfig{
		Enabled:    true,
		HMACSecret: compatTestSecret,
		Issuer:     "gateway-tests",
		Audience:   "gateway",
	}, nil)
	router, err := New(Config{
		CompatHandler: dispatcher.Handler(),
		Authenticator: auth,
		RateLimiter:   middleware.NewRateLimiter(limits, nil),
	})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	return router
}

func postRPC(router http.Handler, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/rpc", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = "203.0.113.5:4000"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

const lendPoolsCall = `{"jsonrpc":"2.0","id":7,"method":"lend_getPools","params":[]}`

func TestCompatRPCNeedsAToken(t *testing.T) {
	up := newRecordingUpstream(t)
	router := compatRouter(t, up, nil, nil)

	for _, token := range []string{"", "not-a-token"} {
		rec := postRPC(router, token, lendPoolsCall)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: HTTP %d (%s), want 401", token, rec.Code, rec.Body.String())
		}
	}
	if calls := up.seen(); len(calls) != 0 {
		t.Fatalf("a caller with no valid token reached the service: %v", calls)
	}

	rec := postRPC(router, compatToken(t, "lending"), lendPoolsCall)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("with a token: HTTP %d (%s), want the service's answer", rec.Code, rec.Body.String())
	}
}

func TestCompatRPCForwardsTheCallersToken(t *testing.T) {
	up := newRecordingUpstream(t)
	router := compatRouter(t, up, nil, nil)
	token := compatToken(t, "lending")

	if rec := postRPC(router, token, lendPoolsCall); rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d (%s)", rec.Code, rec.Body.String())
	}
	calls := up.seen()
	if len(calls) != 1 || calls[0].path != "/v1/lending/pools" {
		t.Fatalf("the service saw %v, want one call to /v1/lending/pools", calls)
	}
	if calls[0].authorization != "Bearer "+token {
		t.Fatalf("the service saw Authorization %q, want the caller's own token", calls[0].authorization)
	}
}

func TestCompatRPCIsRateLimited(t *testing.T) {
	up := newRecordingUpstream(t)
	router := compatRouter(t, up, map[string]middleware.RateLimit{"compat": {RatePerSecond: 0.001, Burst: 2}}, nil)
	token := compatToken(t, "lending")

	for i := 0; i < 2; i++ {
		if rec := postRPC(router, token, lendPoolsCall); rec.Code != http.StatusOK {
			t.Fatalf("request %d: HTTP %d, want 200 within the burst", i, rec.Code)
		}
	}
	if rec := postRPC(router, token, lendPoolsCall); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("the request past the burst: HTTP %d, want 429", rec.Code)
	}
	if calls := up.seen(); len(calls) != 2 {
		t.Fatalf("the service saw %d calls, want the 2 that were within the limit", len(calls))
	}
}
