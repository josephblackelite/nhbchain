package routes

// /metrics lists every route and status the gateway answers. With authentication
// on it takes a token that carries the metrics scope; with no authenticator, or
// one that is switched off, it stays open like every other route (ledger
// PL-EXT-48).

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"nhbchain/gateway/middleware"
)

func metricsRouter(t *testing.T, auth *middleware.Authenticator) http.Handler {
	t.Helper()
	obs := middleware.NewObservability(middleware.ObservabilityConfig{Enabled: true, MetricsPrefix: "gateway"}, nil)
	router, err := New(Config{Authenticator: auth, Observability: obs})
	if err != nil {
		t.Fatalf("new router: %v", err)
	}
	return router
}

func testAuthenticator(enabled bool) *middleware.Authenticator {
	return middleware.NewAuthenticator(middleware.AuthConfig{
		Enabled:    enabled,
		HMACSecret: compatTestSecret,
		Issuer:     "gateway-tests",
		Audience:   "gateway",
	}, nil)
}

func getMetrics(router http.Handler, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.RemoteAddr = "203.0.113.9:4000"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

func TestMetricsNeedsATokenWithTheMetricsScope(t *testing.T) {
	router := metricsRouter(t, testAuthenticator(true))

	// Something to report, so a scrape that is let through has a series to show.
	health := httptest.NewRecorder()
	router.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))

	for _, token := range []string{"", "not-a-token"} {
		if rec := getMetrics(router, token); rec.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: HTTP %d (%s), want 401", token, rec.Code, rec.Body.String())
		}
	}

	// A token for a service route is not a token for the metrics.
	if rec := getMetrics(router, compatToken(t, "lending swap")); rec.Code != http.StatusForbidden {
		t.Fatalf("a token without the metrics scope: HTTP %d (%s), want 403", rec.Code, rec.Body.String())
	}

	rec := getMetrics(router, compatToken(t, "lending "+MetricsScope))
	if rec.Code != http.StatusOK {
		t.Fatalf("a token with the metrics scope: HTTP %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "gateway_requests_total") {
		t.Fatalf("the scrape does not carry the request counter: %s", rec.Body.String())
	}
}

func TestMetricsScopeIsNamedAsDocumented(t *testing.T) {
	if MetricsScope != "metrics:read" {
		t.Fatalf("MetricsScope = %q; docs/gateway/overview.md and the scraper's token name metrics:read", MetricsScope)
	}
}

func TestMetricsStaysOpenWhenAuthenticationIsOff(t *testing.T) {
	for name, auth := range map[string]*middleware.Authenticator{
		"no authenticator":         nil,
		"authenticator switch off": testAuthenticator(false),
	} {
		router := metricsRouter(t, auth)
		if rec := getMetrics(router, ""); rec.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d (%s), want 200: with authentication off every route is open", name, rec.Code, rec.Body.String())
		}
	}
}

func TestMetricsAnswersGetOnlyThroughTheSameGate(t *testing.T) {
	router := metricsRouter(t, testAuthenticator(true))

	// A different method must not slip past the gate either.
	req := httptest.NewRequest(http.MethodPost, "/metrics", strings.NewReader("x"))
	req.RemoteAddr = "203.0.113.9:4000"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST /metrics without a token: HTTP %d, want 401", rec.Code)
	}
}
