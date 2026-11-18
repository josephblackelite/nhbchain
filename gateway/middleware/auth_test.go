package middleware

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
)

const authTestSecret = "gateway-auth-test-secret"

// authTestStatus sends one request carrying a token signed with the given
// claims through an Authenticator built from cfg and returns the status the
// caller would see.
func authTestStatus(t *testing.T, cfg AuthConfig, claims jwt.MapClaims) int {
	t.Helper()
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(authTestSecret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	cfg.Enabled = true
	cfg.HMACSecret = authTestSecret
	auth := NewAuthenticator(cfg, log.New(io.Discard, "", 0))
	handler := auth.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/v1/lending/accounts", nil)
	req.Header.Set("Authorization", "Bearer "+signed)
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res.Code
}

// With an audience configured, a token must carry a matching audience, and
// every token must carry an expiry that has not passed.
func TestAuthenticatorChecksAudienceAndRequiresExpiry(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()
	past := time.Now().Add(-time.Hour).Unix()

	tests := []struct {
		name   string
		cfg    AuthConfig
		claims jwt.MapClaims
		want   int
	}{
		{
			name:   "string audience matches",
			cfg:    AuthConfig{Issuer: "gateway-tests", Audience: "gateway"},
			claims: jwt.MapClaims{"iss": "gateway-tests", "aud": "gateway", "exp": future},
			want:   http.StatusOK,
		},
		{
			name:   "list audience contains the configured one",
			cfg:    AuthConfig{Issuer: "gateway-tests", Audience: "gateway"},
			claims: jwt.MapClaims{"iss": "gateway-tests", "aud": []string{"other", "gateway"}, "exp": future},
			want:   http.StatusOK,
		},
		{
			name:   "audience absent",
			cfg:    AuthConfig{Issuer: "gateway-tests", Audience: "gateway"},
			claims: jwt.MapClaims{"iss": "gateway-tests", "exp": future},
			want:   http.StatusUnauthorized,
		},
		{
			name:   "audience is a number",
			cfg:    AuthConfig{Issuer: "gateway-tests", Audience: "gateway"},
			claims: jwt.MapClaims{"iss": "gateway-tests", "aud": 7, "exp": future},
			want:   http.StatusUnauthorized,
		},
		{
			name:   "audience is another one",
			cfg:    AuthConfig{Issuer: "gateway-tests", Audience: "gateway"},
			claims: jwt.MapClaims{"iss": "gateway-tests", "aud": "someone-else", "exp": future},
			want:   http.StatusUnauthorized,
		},
		{
			name:   "no expiry",
			cfg:    AuthConfig{Issuer: "gateway-tests", Audience: "gateway"},
			claims: jwt.MapClaims{"iss": "gateway-tests", "aud": "gateway"},
			want:   http.StatusUnauthorized,
		},
		{
			name:   "no expiry and no audience configured",
			cfg:    AuthConfig{},
			claims: jwt.MapClaims{"sub": "client"},
			want:   http.StatusUnauthorized,
		},
		{
			name:   "expired",
			cfg:    AuthConfig{Issuer: "gateway-tests", Audience: "gateway"},
			claims: jwt.MapClaims{"iss": "gateway-tests", "aud": "gateway", "exp": past},
			want:   http.StatusUnauthorized,
		},
		{
			name:   "no audience configured accepts a token without one",
			cfg:    AuthConfig{},
			claims: jwt.MapClaims{"sub": "client", "exp": future},
			want:   http.StatusOK,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := authTestStatus(t, tc.cfg, tc.claims); got != tc.want {
				t.Fatalf("expected status %d, got %d", tc.want, got)
			}
		})
	}
}
